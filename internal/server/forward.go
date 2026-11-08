package server

import (
	"context"
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
	mu        sync.Mutex
	record    store.Connection
	stream    protocol.Stream
	local     net.Conn
	runtime   *runtimeTunnel
	source    *net.UDPAddr
	publicUDP *net.UDPConn
	last      time.Time
	once      sync.Once
	packets   chan []byte
	done      chan struct{}
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
			transport.Interrupt(c.stream)
		}
	})
}

func (s *Server) newConnection(r *runtimeTunnel, network, source string, local net.Conn, addr *net.UDPAddr) *activeConnection {
	s.mu.Lock()
	defer s.mu.Unlock()
	full := s.historyBacklog.Load() >= 4096
	if s.closed || r.retired || !r.allowsSource(source) || (r.cfg.Protocol != "both" && r.cfg.Protocol != network) || len(s.connections) >= 1024 || full {
		return nil
	}
	cfg := r.cfg
	device := s.devices[cfg.DeviceID]
	c := &activeConnection{
		record: store.Connection{
			ID:         transport.RandomID(),
			DeviceID:   cfg.DeviceID,
			DeviceName: device.Name,
			TunnelID:   cfg.ID,
			TunnelName: cfg.Name,
			Protocol:   network,
			Source:     source,
			Target:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.TargetPort)),
			Started:    time.Now().UTC(),
			Result:     "active",
		},
		local:   local,
		runtime: r,
		source:  addr,
		last:    time.Now(),
		done:    make(chan struct{}),
	}
	if network == "udp" {
		if len(r.sessions) >= 256 {
			return nil
		}
		c.publicUDP = r.udp
		c.packets = make(chan []byte, 64)
		r.sessions[source] = c
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
	if c.record.Protocol == "udp" {
		c.mu.Lock()
		for len(c.packets) > 0 {
			b := <-c.packets
			s.udpQueuedBytes.Add(-int64(len(b)))
		}
		c.mu.Unlock()
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
		target := net.JoinHostPort(t.Host, strconv.Itoa(t.TargetPort))
		if result == "failed" && target == rec.Target {
			t.TargetStatus = "failed"
			t.TargetError = reason
		} else if rec.Up+rec.Down > 0 && target == rec.Target {
			t.TargetStatus = "reachable"
			t.TargetError = ""
		}
	}
	s.historyMu.Lock()
	s.finished = append(s.finished, rec)
	s.historyBacklog.Add(1)
	s.historyMu.Unlock()
}

func (s *Server) CloseConnection(id string) error {
	s.mu.RLock()
	c := s.connections[id]
	s.mu.RUnlock()
	if c == nil {
		return errors.New("当前连接不存在或已结束")
	}
	s.finish(c, "closed", "管理员主动断开")
	return s.flushHistory()
}

func (s *Server) openForward(c *activeConnection, p *peer) (protocol.Stream, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
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
	stream.SetDeadline(time.Now().Add(20 * time.Second))
	m := protocol.Message{Type: "dial", Network: network, Target: target}
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

func (s *Server) acceptTCP(r *runtimeTunnel, p *peer, listener net.Listener) {
	for {
		conn, err := listener.Accept()
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
	defer stream.Close()
	s.mu.Lock()
	if t := s.tunnels[c.record.TunnelID]; t != nil && net.JoinHostPort(t.Host, strconv.Itoa(t.TargetPort)) == c.record.Target {
		t.TargetStatus = "reachable"
		t.TargetError = ""
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	var firstErr error
	var failedDirection string
	var errMu sync.Mutex
	copyOne := func(dst io.Writer, src io.Reader, up bool) {
		defer wg.Done()
		_, e := io.CopyBuffer(counterWriter{dst, c, up}, src, make([]byte, 32<<10))
		if e == nil {
			transport.HalfClose(dst)
		} else {
			errMu.Lock()
			if firstErr == nil {
				firstErr = e
				failedDirection = "客户端到来源"
				if up {
					failedDirection = "来源到客户端"
				}
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
		reason = "连接中断（" + failedDirection + "）：" + firstErr.Error()
	}
	s.finish(c, result, reason)
}

func (s *Server) acceptUDP(r *runtimeTunnel, p *peer, conn *net.UDPConn) {
	b := make([]byte, 65535)
	for {
		n, addr, err := conn.ReadFromUDP(b)
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
		if s.udpQueuedBytes.Add(int64(n)) > 16<<20 {
			s.udpQueuedBytes.Add(-int64(n))
			continue
		}
		c.mu.Lock()
		select {
		case <-c.done:
			s.udpQueuedBytes.Add(-int64(n))
		default:
			packet := append([]byte(nil), b[:n]...)
			select {
			case c.packets <- packet:
			default:
				s.udpQueuedBytes.Add(-int64(n))
			}
		}
		c.mu.Unlock()
	}
}

func (s *Server) forwardUDP(c *activeConnection, p *peer) {
	stream, err := s.openForward(c, p)
	if err != nil {
		s.finish(c, "failed", "目标连接失败："+err.Error())
		return
	}
	defer stream.Close()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			b, e := protocol.ReadPacket(stream)
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
			s.udpQueuedBytes.Add(-int64(len(b)))
			err = protocol.WritePacket(stream, b)
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
	n, err := c.publicUDP.WriteToUDP(b, c.source)
	if err != nil {
		return
	}
	c.mu.Lock()
	c.record.Down += int64(n)
	c.last = time.Now()
	c.mu.Unlock()
	s.mu.Lock()
	if t := s.tunnels[c.record.TunnelID]; t != nil && net.JoinHostPort(t.Host, strconv.Itoa(t.TargetPort)) == c.record.Target {
		t.TargetStatus = "reachable"
		t.TargetError = ""
	}
	s.mu.Unlock()
}

func (s *Server) flushHistory() error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.RLock()
	items := make([]store.Connection, 0, len(s.connections))
	totals := map[string][2]int64{}
	for id, t := range s.tunnels {
		totals[id] = [2]int64{t.Up, t.Down}
	}
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
	s.historyBacklog.Add(-int64(len(ended)))
	return s.Store.SaveTotals(totals)
}
