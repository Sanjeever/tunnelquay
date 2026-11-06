package server

import (
	"io"
	"net"
	"testing"
	"time"
)

type blockedCloseStream struct {
	gate     chan struct{}
	deadline time.Time
}

func (s *blockedCloseStream) Read([]byte) (int, error)             { return 0, io.EOF }
func (s *blockedCloseStream) Write(b []byte) (int, error)          { return len(b), nil }
func (s *blockedCloseStream) SetDeadline(deadline time.Time) error { s.deadline = deadline; return nil }
func (s *blockedCloseStream) Close() error                         { <-s.gate; return nil }

func TestStopInterruptsPublicConnectionWithoutWaitingForStreamClose(t *testing.T) {
	local, external := net.Pipe()
	defer external.Close()
	stream := &blockedCloseStream{gate: make(chan struct{})}
	c := &activeConnection{local: local, stream: stream, done: make(chan struct{})}
	finished := make(chan struct{})
	go func() { defer close(finished); c.close() }()
	defer func() { close(stream.gate); <-finished }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("停用被逻辑流的网络关闭阻塞")
	}
	if stream.deadline.IsZero() {
		t.Fatal("流读写未被中断")
	}
	if _, err := external.Read(make([]byte, 1)); err != io.EOF {
		t.Fatal("公网连接未立即关闭", err)
	}
}
