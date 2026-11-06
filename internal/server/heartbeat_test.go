package server

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/store"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

type heartbeatWire struct{ messages chan protocol.Message }

func (w *heartbeatWire) Read(b []byte) (int, error) { return 0, io.EOF }
func (w *heartbeatWire) Write(b []byte) (int, error) {
	var m protocol.Message
	if err := protocol.Read(bytes.NewReader(b), &m); err != nil {
		return 0, err
	}
	w.messages <- m
	return len(b), nil
}

func heartbeatTransport(t *testing.T) *transport.Session {
	t.Helper()
	left, right := net.Pipe()
	a, err := transport.NewTCP(left, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := transport.NewTCP(right, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a
}
func heartbeatFixture(t *testing.T, a *transport.Session) (*Server, *peer, *heartbeatWire, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	wire := &heartbeatWire{messages: make(chan protocol.Message, 8)}
	d := store.Device{ID: "heartbeat-test", Online: true}
	s := &Server{ctx: ctx, devices: map[string]store.Device{d.ID: d}}
	p := &peer{session: a, control: &protocol.Codec{RW: wire}, device: d, done: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); s.heartbeat(p) }()
	cleanup := func() { cancel(); <-done }
	return s, p, wire, cleanup, done
}

func TestHeartbeatAllowsDelayedReply(t *testing.T) {
	session := heartbeatTransport(t)
	synctest.Test(t, func(t *testing.T) {
		s, p, wire, cleanup, _ := heartbeatFixture(t, session)
		defer cleanup()
		request := <-wire.messages
		time.Sleep(45 * time.Second)
		if p.session.TCP.IsClosed() {
			t.Fatal("45 秒响应停顿内接入被心跳提前关闭")
		}
		channel, ok := p.pending.Load(request.ID)
		if !ok {
			t.Fatal("迟延响应对应的请求已被提前移除")
		}
		channel.(chan protocol.Message) <- protocol.Message{Type: "pong", ID: request.ID}
		synctest.Wait()
		s.mu.RLock()
		rtt := s.devices[p.device.ID].RTT
		s.mu.RUnlock()
		if rtt == nil || *rtt != 45000 {
			t.Fatalf("迟延响应的真实 RTT 未更新：%v", rtt)
		}
	})
}

func TestHeartbeatClosesUnresponsivePeer(t *testing.T) {
	session := heartbeatTransport(t)
	synctest.Test(t, func(t *testing.T) {
		_, p, wire, cleanup, done := heartbeatFixture(t, session)
		defer cleanup()
		<-wire.messages
		start := time.Now()
		<-done
		elapsed := time.Since(start)
		if elapsed < 60*time.Second || elapsed > 65*time.Second {
			t.Fatalf("失联判定时间 %s，期望 60 至 65 秒", elapsed)
		}
		if !p.session.TCP.IsClosed() {
			t.Fatal("持续失联没有关闭接入")
		}
	})
}
