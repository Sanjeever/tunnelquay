package server

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
)

func TestConfigurationResponseStillTimesOutAfterTenSeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		wire := &heartbeatWire{messages: make(chan protocol.Message, 1)}
		s := &Server{ctx: ctx}
		p := &peer{control: &protocol.Codec{RW: wire}, done: make(chan struct{})}
		finished := make(chan error, 1)
		started := time.Now()
		go func() {
			_, err := s.request(p, protocol.Message{Type: "prepare"}, protocol.ControlRequestTimeout)
			finished <- err
		}()
		request := <-wire.messages
		err := <-finished
		if err == nil || time.Since(started) != 10*time.Second {
			t.Fatalf("配置响应超时语义变化：%v，%s", err, time.Since(started))
		}
		if _, present := p.pending.Load(request.ID); present {
			t.Fatal("超时请求没有从等待队列移除")
		}
	})
}
