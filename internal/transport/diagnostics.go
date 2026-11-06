package transport

import (
	"errors"
	"io"
	"net"

	"github.com/hashicorp/yamux"
	quic "github.com/quic-go/quic-go"
)

func ErrorKind(err error) string {
	if errors.Is(err, yamux.ErrKeepAliveTimeout) {
		return "transport_keepalive_timeout"
	}
	if errors.Is(err, yamux.ErrConnectionWriteTimeout) {
		return "transport_write_timeout"
	}
	var idle *quic.IdleTimeoutError
	if errors.As(err, &idle) {
		return "transport_idle_timeout"
	}
	var application *quic.ApplicationError
	if errors.As(err, &application) {
		return "application_close"
	}
	var stream *quic.StreamError
	if errors.As(err, &stream) {
		return "stream_reset"
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
	if s.QUIC != nil {
		stats := s.QUIC.ConnectionStats()
		return map[string]any{"发送字节": stats.BytesSent, "接收字节": stats.BytesReceived, "丢失包": stats.PacketsLost, "发送包": stats.PacketsSent, "拥塞窗口": stats.CongestionWindow, "平滑RTT": stats.SmoothedRTT.String()}
	}
	return map[string]any{"逻辑流": s.TCP.NumStreams(), "传输已关闭": s.TCP.IsClosed()}
}
