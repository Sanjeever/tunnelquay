package client

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

type forwardTarget struct {
	tunnelID string
	revision int64
	cancel   context.CancelFunc
}

func (c *Client) acceptStreams(ctx context.Context, session *transport.Session) {
	sem := make(chan struct{}, 1024)
	for {
		stream, err := session.Accept(ctx)
		if err != nil {
			return
		}
		select {
		case sem <- struct{}{}:
			c.wg.Go(func() { defer func() { <-sem }(); c.forward(ctx, session, stream) })
		default:
			transport.Abort(stream)
		}
	}
}
func (c *Client) forward(ctx context.Context, session *transport.Session, stream protocol.Stream) {
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(10 * time.Second))
	var m protocol.Message
	if protocol.Read(stream, &m) != nil {
		return
	}
	if m.Type != "forward" {
		protocol.Write(stream, protocol.Message{Error: "转发请求无效"})
		return
	}
	if m.Network != "tcp" && m.Network != "udp" {
		protocol.Write(stream, protocol.Message{Error: "转发协议无效"})
		return
	}
	if m.Config == nil {
		protocol.Write(stream, protocol.Message{Error: "缺少隧道配置版本"})
		return
	}
	forwardCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	cfg, ok := c.configs[m.Config.ID]
	if !ok || cfg.Revision < m.Config.Revision {
		c.mu.Unlock()
		protocol.Write(stream, protocol.Message{Error: "隧道配置已撤销"})
		return
	}
	c.forwards[stream] = forwardTarget{m.Config.ID, m.Config.Revision, cancel}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.forwards, stream); c.mu.Unlock() }()
	d := net.Dialer{Timeout: 8 * time.Second}
	target, err := d.DialContext(forwardCtx, m.Network, m.Target)
	if err != nil {
		protocol.Write(stream, protocol.Message{Error: "无法连接目标：" + err.Error()})
		return
	}
	defer target.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-forwardCtx.Done():
			target.Close()
			transport.Abort(stream)
		case <-done:
		}
	}()
	if protocol.Write(stream, protocol.Message{Type: "ready", Config: m.Config}) != nil {
		return
	}
	stream.SetDeadline(time.Time{})
	if m.Network == "tcp" {
		var wg sync.WaitGroup
		wg.Add(2)
		copyOne := func(dst io.Writer, src io.Reader) {
			defer wg.Done()
			_, e := io.CopyBuffer(dst, src, make([]byte, 32<<10))
			if e == nil {
				transport.HalfClose(dst)
			} else {
				target.Close()
				transport.Abort(stream)
			}
		}
		go copyOne(target, stream)
		go copyOne(stream, target)
		wg.Wait()
		return
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		b := make([]byte, 65535)
		for {
			target.SetReadDeadline(time.Now().Add(65 * time.Second))
			n, e := target.Read(b)
			if e != nil {
				transport.Abort(stream)
				return
			}
			if e = writePacket(stream, b[:n]); e != nil {
				transport.Abort(stream)
				return
			}
		}
	}()
	for {
		b, e := readPacket(stream)
		if e != nil {
			break
		}
		target.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, e = target.Write(b); e != nil {
			break
		}
	}
	target.Close()
	<-readDone
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
	b := make([]byte, int(binary.BigEndian.Uint16(h[:])))
	_, err := io.ReadFull(r, b)
	return b, err
}
