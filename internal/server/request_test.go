package server

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

func TestControlRequestTimesOutAndUnknownMessageReturnsError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		wire := &heartbeatWire{messages: make(chan protocol.Message, 1)}
		s := &Server{ctx: ctx}
		p := &peer{control: &protocol.Codec{RW: wire}, done: make(chan struct{})}
		finished := make(chan error, 1)
		started := time.Now()
		go func() {
			_, err := s.request(p, protocol.Message{Type: "ping"}, protocol.ControlRequestTimeout)
			finished <- err
		}()
		request := <-wire.messages
		err := <-finished
		if err == nil || time.Since(started) != 10*time.Second {
			t.Fatalf("控制请求超时语义变化：%v，%s", err, time.Since(started))
		}
		if _, present := p.pending.Load(request.ID); present {
			t.Fatal("超时请求没有从等待队列移除")
		}
	})
	srv := testServer(t)
	key := testDeviceKey(t)
	hello := testDeviceHello(key, srv.Identity.Secret, protocol.Capabilities)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := transport.Dial(ctx, srv.Addr(), transport.ClientTLS(srv.Identity.Fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	stream, err := session.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(3 * time.Second))
	codec := &protocol.Codec{RW: stream}
	if welcome := testIdentityProof(t, codec, hello, key, srv.Identity.Fingerprint); welcome.Type != "welcome" {
		t.Fatal(welcome)
	}
	if err := codec.Send(protocol.Message{Type: "unknown_future_command", ID: "req-1"}); err != nil {
		t.Fatal(err)
	}
	var reply protocol.Message
	for {
		reply, err = codec.Receive()
		if err != nil || reply.Type != "ping" {
			break
		}
	}
	if err != nil || reply.ID != "req-1" || reply.Code != "unsupported_message" {
		t.Fatalf("未知控制消息未明确返回错误：%v %+v", err, reply)
	}
}
