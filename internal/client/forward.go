package client

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

func (c *Client) acceptStreams(ctx context.Context, session *transport.Session) {
	sem := make(chan struct{}, 1024)
	for {
		stream, err := session.Accept(ctx)
		if err != nil {
			return
		}
		select {
		case sem <- struct{}{}:
			c.wg.Go(func() { defer func() { <-sem }(); c.forward(ctx, stream) })
		default:
			transport.Abort(stream)
		}
	}
}

func (c *Client) forward(ctx context.Context, stream protocol.Stream) {
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(10 * time.Second))
	var m protocol.Message
	if protocol.Read(stream, &m) != nil {
		return
	}
	if m.Type != "dial" {
		protocol.Write(stream, protocol.Message{Error: "拨号请求无效"})
		return
	}
	if m.Network != "tcp" && m.Network != "udp" {
		protocol.Write(stream, protocol.Message{Error: "转发协议无效"})
		return
	}
	if m.Target == "" {
		protocol.Write(stream, protocol.Message{Error: "缺少目标地址"})
		return
	}
	d := net.Dialer{Timeout: 8 * time.Second}
	target, err := d.DialContext(ctx, m.Network, m.Target)
	if err != nil {
		protocol.Write(stream, protocol.Message{Error: "无法连接目标：" + err.Error()})
		return
	}
	defer target.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			target.Close()
			transport.Abort(stream)
		case <-done:
		}
	}()
	if protocol.Write(stream, protocol.Message{Type: "ready"}) != nil {
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
			if e = protocol.WritePacket(stream, b[:n]); e != nil {
				transport.Abort(stream)
				return
			}
		}
	}()
	for {
		b, e := protocol.ReadPacket(stream)
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
