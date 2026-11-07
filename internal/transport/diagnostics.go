package transport

import (
	"errors"
	"io"
	"net"

	"github.com/hashicorp/yamux"
)

func ErrorKind(err error) string {
	if errors.Is(err, yamux.ErrKeepAliveTimeout) {
		return "transport_keepalive_timeout"
	}
	if errors.Is(err, yamux.ErrConnectionWriteTimeout) {
		return "transport_write_timeout"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "io_timeout"
	}
	if errors.Is(err, io.EOF) {
		return "eof"
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, yamux.ErrSessionShutdown) {
		return "connection_closed"
	}
	return "io_error"
}

func (s *Session) Diagnostics() map[string]any {
	return map[string]any{"逻辑流": s.TCP.NumStreams(), "传输已关闭": s.TCP.IsClosed()}
}
