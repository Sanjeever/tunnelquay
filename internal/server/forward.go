package server

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/store"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

type activeConnection struct {
	mu               sync.Mutex
	record           store.Connection
	stream           protocol.Stream
	local            net.Conn
	runtime          *runtimeTunnel
	udpID            uint64
	source           *net.UDPAddr
	last             time.Time
	prevUp, prevDown int64
	once             sync.Once
	packets          chan []byte
	done             chan struct{}
}

func reportError(msg string, err error) { slog.Error(msg, "错误", err) }
func (c *activeConnection) close() {
	c.once.Do(func() {
		close(c.done)
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.local != nil {
			c.local.Close()
		}
		if c.stream != nil {
			transport.Abort(c.stream)
		}
	})
}
func (s *Server) newConnection(r *runtimeTunnel, network, source string, local net.Conn, addr *net.UDPAddr) *activeConnection {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyMu.Lock()
	full := len(s.finished) >= 4096
	s.historyMu.Unlock()
	if s.closed || r.retired || len(s.connections) >= 1024 || full {
		return nil
	}
	cfg := r.cfg
	device := s.devices[cfg.DeviceID]
	c := &activeConnection{record: store.Connection{ID: transport.RandomID(), DeviceID: cfg.DeviceID, DeviceName: device.Name, TunnelID: cfg.ID, TunnelName: cfg.Name, Revision: cfg.Revision, Protocol: network, Source: source, Target: net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.TargetPort)), Started: time.Now().UTC(), Result: "active"}, local: local, runtime: r, source: addr, last: time.Now(), done: make(chan struct{})}
	if network == "udp" {
		if len(r.sessions) >= 256 {
			return nil
		}
		c.udpID = s.nextUDP.Add(1)
		c.packets = make(chan []byte, 64)
		r.sessions[source] = c
		s.udpConnections[c.udpID] = c
	}
	s.connections[c.record.ID] = c
	if t := s.tunnels[cfg.ID]; t != nil {
		t.Connections++
	}
	return c
}
func (s *Server) finish(c *activeConnection, result, reason string) {
	c.close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.connections[c.record.ID]; !ok {
		return
	}
	delete(s.connections, c.record.ID)
	if c.udpID != 0 {
		delete(s.udpConnections, c.udpID)
		delete(c.runtime.sessions, c.record.Source)
	}
	c.mu.Lock()
	now := time.Now().UTC()
	c.record.Ended = &now
	c.record.Result = result
	c.record.Reason = reason
	rec := c.record
	c.mu.Unlock()
	if t := s.tunnels[rec.TunnelID]; t != nil {
		t.Connections--
		t.Up += rec.Up
		t.Down += rec.Down
		if result == "failed" && t.Applied != nil && t.Applied.Revision == rec.Revision {
			t.TargetStatus = "failed"
			t.Error = reason
		} else if rec.Up+rec.Down > 0 && t.Applied != nil && t.Applied.Revision == rec.Revision {
			t.TargetStatus = "reachable"
		}
	}
	s.historyMu.Lock()
	s.finished = append(s.finished, rec)
	s.historyMu.Unlock()
}
func (s *Server) openForward(c *activeConnection, p *peer) (protocol.Stream, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	stream, err := p.session.Open(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.stream = stream
	target := c.record.Target
	network := c.record.Protocol
	c.mu.Unlock()
	select {
	case <-c.done:
		transport.Abort(stream)
		return nil, errors.New("连接已关闭")
	default:
	}
	stream.SetDeadline(time.Now().Add(10 * time.Second))
	m := protocol.Message{Type: "forward", ID: strconv.FormatUint(c.udpID, 10), Network: network, Target: target, Config: &protocol.Config{ID: c.record.TunnelID, Revision: c.record.Revision}}
	if err = protocol.Write(stream, m); err != nil {
		transport.Abort(stream)
		return nil, err
	}
	var reply protocol.Message
	if err = protocol.Read(stream, &reply); err != nil {
		transport.Abort(stream)
		return nil, err
	}
	if reply.Error != "" {
		transport.Abort(stream)
		return nil, errors.New(reply.Error)
	}
	stream.SetDeadline(time.Time{})
	return stream, nil
}
func (s *Server) acceptTCP(r *runtimeTunnel, p *peer) {
	for {
		conn, err := r.tcp.Accept()
		if err != nil {
			return
		}
		c := s.newConnection(r, "tcp", conn.RemoteAddr().String(), conn, nil)
		if c == nil {
			conn.Close()
			continue
		}
		s.wg.Go(func() { s.forwardTCP(c, p) })
	}
}

type counterWriter struct {
	w  io.Writer
	c  *activeConnection
	up bool
}

func (w counterWriter) Write(b []byte) (int, error) {
	n, e := w.w.Write(b)
	w.c.mu.Lock()
	if w.up {
		w.c.record.Up += int64(n)
	} else {
		w.c.record.Down += int64(n)
	}
	w.c.last = time.Now()
	w.c.mu.Unlock()
	return n, e
}
func (s *Server) forwardTCP(c *activeConnection, p *peer) {
	stream, err := s.openForward(c, p)
	if err != nil {
		s.finish(c, "failed", "目标连接失败："+err.Error())
		return
	}
	s.mu.Lock()
	if t := s.tunnels[c.record.TunnelID]; t != nil && t.Applied != nil && t.Applied.Revision == c.record.Revision {
		t.TargetStatus = "reachable"
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	copyOne := func(dst io.Writer, src io.Reader, up bool) {
		defer wg.Done()
		_, e := io.CopyBuffer(counterWriter{dst, c, up}, src, make([]byte, 32<<10))
		transport.HalfClose(dst)
		if e != nil {
			errMu.Lock()
			if firstErr == nil {
				firstErr = e
			}
			errMu.Unlock()
			c.close()
		}
	}
	wg.Add(2)
	go copyOne(stream, c.local, true)
	go copyOne(c.local, stream, false)
	wg.Wait()
	result, reason := "closed", "连接结束"
	if firstErr != nil {
		result = "interrupted"
		reason = "连接中断：" + firstErr.Error()
	}
	s.finish(c, result, reason)
}
func (s *Server) acceptUDP(r *runtimeTunnel, p *peer) {
	b := make([]byte, 65535)
	for {
		n, addr, err := r.udp.ReadFromUDP(b)
		if err != nil {
			return
		}
		key := addr.String()
		s.mu.RLock()
		c := r.sessions[key]
		retired := r.retired
		s.mu.RUnlock()
		if c == nil {
			if retired {
				continue
			}
			c = s.newConnection(r, "udp", key, nil, addr)
			if c == nil {
				continue
			}
			s.wg.Go(func() { s.forwardUDP(c, p) })
		}
		packet := append([]byte(nil), b[:n]...)
		select {
		case c.packets <- packet:
		default:
		}
	}
}
func writePacket(w io.Writer, b []byte) error {
	if len(b) > 65507 {
		return errors.New("UDP 数据报过大")
	}
	var h [2]byte
	binary.BigEndian.PutUint16(h[:], uint16(len(b)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}
func readPacket(r io.Reader) ([]byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(h[:]))
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}
func (s *Server) forwardUDP(c *activeConnection, p *peer) {
	stream, err := s.openForward(c, p)
	if err != nil {
		s.finish(c, "failed", "目标连接失败："+err.Error())
		return
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		if p.session.QUIC != nil {
			io.Copy(io.Discard, stream)
			c.close()
			return
		}
		for {
			b, e := readPacket(stream)
			if e != nil {
				c.close()
				return
			}
			s.deliverUDP(c, b)
		}
	}()
	for {
		select {
		case b := <-c.packets:
			if p.session.QUIC != nil {
				data := make([]byte, 8+len(b))
				binary.BigEndian.PutUint64(data, c.udpID)
				copy(data[8:], b)
				err = p.session.SendDatagram(data)
			} else {
				err = writePacket(stream, b)
			}
			if err != nil {
				c.close()
			} else {
				c.mu.Lock()
				c.record.Up += int64(len(b))
				c.last = time.Now()
				c.mu.Unlock()
			}
		case <-c.done:
			<-readDone
			s.finish(c, "closed", "UDP 会话结束")
			return
		case <-p.done:
			c.close()
		case <-s.ctx.Done():
			c.close()
		}
	}
}
func (s *Server) deliverUDP(c *activeConnection, b []byte) {
	select {
	case <-c.done:
		return
	default:
	}
	n, err := c.runtime.udp.WriteToUDP(b, c.source)
	if err != nil {
		return
	}
	c.mu.Lock()
	c.record.Down += int64(n)
	c.last = time.Now()
	c.mu.Unlock()
	s.mu.Lock()
	if t := s.tunnels[c.record.TunnelID]; t != nil && t.Applied != nil && t.Applied.Revision == c.record.Revision {
		t.TargetStatus = "reachable"
	}
	s.mu.Unlock()
}
func (s *Server) receiveUDP(p *peer) {
	for {
		b, err := p.session.ReceiveDatagram(s.ctx)
		if err != nil {
			return
		}
		if len(b) < 8 {
			continue
		}
		id := binary.BigEndian.Uint64(b)
		s.mu.RLock()
		c := s.udpConnections[id]
		valid := c != nil && c.record.DeviceID == p.device.ID
		s.mu.RUnlock()
		if valid {
			s.deliverUDP(c, b[8:])
		}
	}
}
func (s *Server) flushHistory() error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.RLock()
	items := make([]store.Connection, 0, len(s.connections))
	for _, c := range s.connections {
		c.mu.Lock()
		items = append(items, c.record)
		c.mu.Unlock()
	}
	s.mu.RUnlock()
	s.historyMu.Lock()
	ended := s.finished
	s.finished = nil
	s.historyMu.Unlock()
	items = append(items, ended...)
	if err := s.Store.SaveConnections(items); err != nil {
		s.historyMu.Lock()
		s.finished = append(ended, s.finished...)
		s.historyMu.Unlock()
		return err
	}
	return nil
}
