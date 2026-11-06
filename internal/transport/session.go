package transport

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/hashicorp/yamux"
	quic "github.com/quic-go/quic-go"
)

const MaxDatagram = 65507
const fragmentSize = 1000

type Session struct {
	TCP      *yamux.Session
	QUIC     *quic.Conn
	Network  string
	sendMu   sync.Mutex
	packetID atomic.Uint64
	partial  map[uint64]*assembly
}
type assembly struct {
	born  time.Time
	count int
	parts [][]byte
	size  int
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
func NewQUIC(c *quic.Conn) *Session {
	return &Session{QUIC: c, Network: "quic", partial: map[uint64]*assembly{}}
}
func QUICConfig() *quic.Config {
	return &quic.Config{EnableDatagrams: true, MaxIncomingStreams: 1024, MaxIdleTimeout: protocol.ControlIdleTimeout, KeepAlivePeriod: protocol.HeartbeatInterval, InitialStreamReceiveWindow: 64 << 10, MaxStreamReceiveWindow: 1 << 20, MaxConnectionReceiveWindow: 16 << 20}
}
func Dial(ctx context.Context, address, network string, tlsConfig *tls.Config) (*Session, error) {
	if network == "quic" {
		c, err := quic.DialAddr(ctx, address, tlsConfig, QUICConfig())
		if err != nil {
			return nil, err
		}
		return NewQUIC(c), nil
	}
	if network != "tcp" {
		return nil, errors.New("传输方式必须为 quic 或 tcp")
	}
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
	if s.QUIC != nil {
		return s.QUIC.OpenStreamSync(ctx)
	}
	return s.TCP.OpenStream()
}
func (s *Session) Accept(ctx context.Context) (protocol.Stream, error) {
	if s.QUIC != nil {
		return s.QUIC.AcceptStream(ctx)
	}
	return s.TCP.AcceptStream()
}
func (s *Session) Close() error {
	if s.QUIC != nil {
		return s.QUIC.CloseWithError(0, "会话结束")
	}
	return s.TCP.Close()
}
func Interrupt(s protocol.Stream) {
	s.SetDeadline(time.Now())
	if q, ok := s.(*quic.Stream); ok {
		q.CancelRead(1)
		q.CancelWrite(1)
	}
}
func Abort(s protocol.Stream) { Interrupt(s); s.Close() }
func HalfClose(w io.Writer) {
	switch c := w.(type) {
	case interface{ CloseWrite() error }:
		c.CloseWrite()
	case interface{ Close() error }:
		c.Close()
	}
}
func (s *Session) SendDatagram(data []byte) error {
	if s.QUIC == nil {
		return errors.New("当前传输不支持 QUIC 数据报")
	}
	if len(data) > MaxDatagram+8 {
		return errors.New("UDP 数据报超过大小限制")
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	id := s.packetID.Add(1)
	count := (len(data) + fragmentSize - 1) / fragmentSize
	for i := 0; i < count; i++ {
		end := min((i+1)*fragmentSize, len(data))
		b := make([]byte, 12+end-i*fragmentSize)
		binary.BigEndian.PutUint64(b, id)
		binary.BigEndian.PutUint16(b[8:], uint16(i))
		binary.BigEndian.PutUint16(b[10:], uint16(count))
		copy(b[12:], data[i*fragmentSize:end])
		if err := s.QUIC.SendDatagram(b); err != nil {
			return err
		}
	}
	return nil
}
func (s *Session) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	for {
		b, err := s.QUIC.ReceiveDatagram(ctx)
		if err != nil {
			return nil, err
		}
		if len(b) < 12 {
			continue
		}
		id := binary.BigEndian.Uint64(b)
		i := int(binary.BigEndian.Uint16(b[8:]))
		n := int(binary.BigEndian.Uint16(b[10:]))
		if n == 0 || n > 66 || i >= n || len(b) > 1012 {
			continue
		}
		now := time.Now()
		for key, a := range s.partial {
			if now.Sub(a.born) > 2*time.Second {
				delete(s.partial, key)
			}
		}
		a := s.partial[id]
		if a == nil {
			if len(s.partial) >= 128 {
				continue
			}
			a = &assembly{born: now, parts: make([][]byte, n)}
			s.partial[id] = a
		}
		if len(a.parts) != n || a.parts[i] != nil {
			continue
		}
		a.parts[i] = append([]byte(nil), b[12:]...)
		a.count++
		a.size += len(b) - 12
		if a.size > MaxDatagram+8 {
			delete(s.partial, id)
			continue
		}
		if a.count == n {
			delete(s.partial, id)
			out := make([]byte, 0, a.size)
			for _, part := range a.parts {
				out = append(out, part...)
			}
			return out, nil
		}
	}
}
