package transport

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/hashicorp/yamux"
)

const MaxDatagram = 65507

type Session struct {
	TCP     *yamux.Session
	Network string
}

func NewTCP(c net.Conn, server bool) (*Session, error) {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = nil
	logger := slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn)
	logger.SetPrefix("TCP 复用传输：")
	cfg.Logger = logger
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = protocol.HeartbeatInterval
	cfg.ConnectionWriteTimeout = protocol.HeartbeatTimeout
	cfg.StreamOpenTimeout = protocol.ControlRequestTimeout
	cfg.StreamCloseTimeout = 0
	var s *yamux.Session
	var err error
	if server {
		s, err = yamux.Server(c, cfg)
	} else {
		s, err = yamux.Client(c, cfg)
	}
	if err != nil {
		return nil, err
	}
	return &Session{TCP: s, Network: "tcp"}, nil
}

func Dial(ctx context.Context, address string, tlsConfig *tls.Config) (*Session, error) {
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: tlsConfig}
	c, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	s, err := NewTCP(c, false)
	if err != nil {
		c.Close()
	}
	return s, err
}

func (s *Session) Open(ctx context.Context) (protocol.Stream, error) {
	return s.TCP.OpenStream()
}

func (s *Session) Accept(ctx context.Context) (protocol.Stream, error) {
	return s.TCP.AcceptStream()
}

func (s *Session) Close() error {
	return s.TCP.Close()
}

func Interrupt(s protocol.Stream) {
	s.SetDeadline(time.Now())
}

func Abort(s protocol.Stream) {
	Interrupt(s)
	s.Close()
}

func HalfClose(w io.Writer) {
	switch c := w.(type) {
	case interface{ CloseWrite() error }:
		c.CloseWrite()
	case interface{ Close() error }:
		c.Close()
	}
}
