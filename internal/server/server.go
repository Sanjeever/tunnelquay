package server

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/store"
	"github.com/Sanjeever/tunnelquay/internal/transport"
	"github.com/Sanjeever/tunnelquay/internal/webui"
	quic "github.com/quic-go/quic-go"
	"github.com/soheilhy/cmux"
)

type Options struct{ Listen, DataDir string }
type peer struct {
	session *transport.Session
	control *protocol.Codec
	stream  protocol.Stream
	pending sync.Map
	done    chan struct{}
	device  store.Device
}
type Server struct {
	Options           Options
	Store             *store.Store
	Identity          *transport.Identity
	mu                sync.RWMutex
	opMu              sync.Mutex
	packageMu         sync.Mutex
	upgradeMu         sync.Mutex
	peers             map[string]*peer
	devices           map[string]store.Device
	tunnels           map[string]*store.Tunnel
	running           map[string]*runtimeTunnel
	generations       map[string][]*runtimeTunnel
	udpConnections    map[uint64]*activeConnection
	nextUDP           atomic.Uint64
	udpQueuedBytes    atomic.Int64
	historyBacklog    atomic.Int64
	peerSlots         chan struct{}
	fileSlots         chan struct{}
	rateSamples       map[string][2]int64
	rateSampleAt      time.Time
	connections       map[string]*activeConnection
	sessions          map[string]loginSession
	loginAttempts     int
	loginWindow       time.Time
	listener          net.Listener
	packet            net.PacketConn
	quicListener      *quic.Listener
	http              *http.Server
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	closed            bool
	TemporaryPassword string
	historyMu         sync.Mutex
	flushMu           sync.Mutex
	finished          []store.Connection
}

func New(opts Options) (*Server, error) {
	id, err := transport.LoadIdentity(opts.DataDir)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(opts.DataDir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{Options: opts, Store: db, Identity: id, peers: map[string]*peer{}, devices: map[string]store.Device{}, tunnels: map[string]*store.Tunnel{}, running: map[string]*runtimeTunnel{}, generations: map[string][]*runtimeTunnel{}, udpConnections: map[uint64]*activeConnection{}, connections: map[string]*activeConnection{}, sessions: map[string]loginSession{}, ctx: ctx, cancel: cancel}
	s.peerSlots = make(chan struct{}, 128)
	s.fileSlots = make(chan struct{}, 2)
	s.rateSamples = map[string][2]int64{}
	s.rateSampleAt = time.Now()
	fail := func(e error) (*Server, error) { cancel(); db.Close(); return nil, e }
	var devices []store.Device
	if err = db.All("devices", &devices); err != nil {
		return fail(err)
	}
	for _, d := range devices {
		d.Online = false
		d.RTT = nil
		s.devices[d.ID] = d
	}
	var tunnels []store.Tunnel
	if err = db.All("tunnels", &tunnels); err != nil {
		return fail(err)
	}
	for _, t := range tunnels {
		t.Status = "offline"
		t.TargetStatus = "unknown"
		t.Connections = 0
		t.UpRate = 0
		t.DownRate = 0
		s.rateSamples[t.Desired.ID] = [2]int64{t.Up, t.Down}
		s.tunnels[t.Desired.ID] = &t
	}
	if err = s.initAdmin(); err != nil {
		return fail(err)
	}
	return s, nil
}
func (s *Server) Start() error {
	lc := transport.ListenConfig()
	l, err := lc.Listen(s.ctx, "tcp", s.Options.Listen)
	if err != nil {
		return err
	}
	s.listener = l
	packet, err := lc.ListenPacket(s.ctx, "udp", l.Addr().String())
	if err != nil {
		l.Close()
		return fmt.Errorf("UDP 接入监听失败：%w", err)
	}
	s.packet = packet
	ql, err := quic.Listen(packet, s.Identity.TLS(), transport.QUICConfig())
	if err != nil {
		l.Close()
		packet.Close()
		return fmt.Errorf("QUIC 监听失败：%w", err)
	}
	s.quicListener = ql
	mux := cmux.New(l)
	mux.SetReadTimeout(10 * time.Second)
	tlsListener := tls.NewListener(mux.Match(cmux.TLS()), s.Identity.TLS())
	httpListener := mux.Match(cmux.Any())
	s.http = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	s.wg.Go(func() {
		if e := s.http.Serve(httpListener); e != nil && !errors.Is(e, http.ErrServerClosed) && s.ctx.Err() == nil {
			slog.Error("管理服务退出", "错误", e)
			s.cancel()
		}
	})
	s.wg.Go(func() {
		if e := mux.Serve(); e != nil && s.ctx.Err() == nil {
			slog.Error("端口分流退出", "错误", e)
			s.cancel()
		}
	})
	s.wg.Go(func() {
		for {
			c, e := tlsListener.Accept()
			if e != nil {
				return
			}
			select {
			case s.peerSlots <- struct{}{}:
			default:
				c.Close()
				continue
			}
			s.wg.Go(func() {
				defer func() { <-s.peerSlots }()
				c.SetDeadline(time.Now().Add(10 * time.Second))
				tc := c.(*tls.Conn)
				if e := tc.HandshakeContext(s.ctx); e != nil {
					c.Close()
					return
				}
				c.SetDeadline(time.Time{})
				session, e := transport.NewTCP(c, true)
				if e != nil {
					c.Close()
					return
				}
				s.servePeer(session)
			})
		}
	})
	s.wg.Go(func() {
		for {
			c, e := ql.Accept(s.ctx)
			if e != nil {
				return
			}
			select {
			case s.peerSlots <- struct{}{}:
			default:
				c.CloseWithError(1, "设备接入达到并发上限")
				continue
			}
			s.wg.Go(func() { defer func() { <-s.peerSlots }(); s.servePeer(transport.NewQUIC(c)) })
		}
	})
	s.wg.Go(s.maintenance)
	return nil
}
func (s *Server) Addr() string          { return s.listener.Addr().String() }
func (s *Server) Done() <-chan struct{} { return s.ctx.Done() }
func (s *Server) Close() error {
	s.cancel()
	if s.listener != nil {
		s.listener.Close()
	}
	if s.quicListener != nil {
		s.quicListener.Close()
	}
	if s.packet != nil {
		s.packet.Close()
	}
	if s.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		s.http.Shutdown(ctx)
		cancel()
	}
	s.opMu.Lock()
	s.mu.Lock()
	s.closed = true
	for id := range s.generations {
		s.stopLocked(id)
	}
	for _, p := range s.peers {
		p.session.Close()
	}
	s.mu.Unlock()
	s.opMu.Unlock()
	s.wg.Wait()
	err := s.flushHistory()
	if err != nil {
		slog.Error("保存连接记录失败", "错误", err)
	}
	return errors.Join(err, s.Store.Close())
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	mux.Handle("/", webui.Handler())
	return mux
}
func (s *Server) servePeer(session *transport.Session) {
	defer session.Close()
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	stream, err := session.Accept(ctx)
	cancel()
	if err != nil {
		return
	}
	defer transport.Abort(stream)
	stream.SetDeadline(time.Now().Add(10 * time.Second))
	codec := &protocol.Codec{RW: stream}
	m, err := codec.Receive()
	if err != nil {
		return
	}
	reject := func(code, msg string) {
		codec.Send(protocol.Message{Type: "error", Code: code, Error: msg})
		if session.QUIC != nil {
			session.QUIC.CloseWithError(1, code+"："+msg)
		}
	}
	if m.Type != "hello" || m.Hello == nil {
		reject("invalid_hello", "接入握手无效")
		return
	}
	h := *m.Hello
	if subtle.ConstantTimeCompare([]byte(h.Secret), []byte(s.Identity.Secret)) != 1 {
		reject("unauthorized", "接入凭据错误")
		return
	}
	if err = protocol.Negotiate(h); err != nil {
		reject("incompatible", err.Error())
		return
	}
	if len(h.ID) != 48 || len(h.Hostname) > 256 {
		reject("invalid_identity", "设备身份无效")
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if _, ok := s.peers[h.ID]; ok {
		s.mu.Unlock()
		reject("duplicate_identity", "同一设备身份已有在线实例")
		return
	}
	d := s.devices[h.ID]
	d.ID = h.ID
	d.Hostname = h.Hostname
	if d.Name == "" {
		d.Name = h.Hostname
	}
	d.OS = h.OS
	d.Arch = h.Arch
	d.Version = h.Version
	d.Capabilities = h.Capabilities
	d.Online = true
	d.Transport = session.Network
	d.RTT = nil
	d.LastSeen = time.Now().UTC()
	if err = s.Store.Put("devices", d.ID, d); err != nil {
		s.mu.Unlock()
		reject("storage_failed", "保存设备失败")
		return
	}
	p := &peer{session: session, control: codec, stream: stream, done: make(chan struct{}), device: d}
	s.peers[d.ID] = p
	s.devices[d.ID] = d
	s.mu.Unlock()
	defer func() {
		close(p.done)
		s.opMu.Lock()
		defer s.opMu.Unlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.peers, d.ID)
		dev := s.devices[d.ID]
		dev.Online = false
		dev.RTT = nil
		s.devices[d.ID] = dev
		for id, t := range s.tunnels {
			if t.Desired.DeviceID == d.ID {
				s.stopLocked(id)
				t.Status = "offline"
			}
		}
	}()
	if codec.Send(protocol.Message{Type: "welcome", Protocol: protocol.Version, Capabilities: protocol.Capabilities}) != nil {
		return
	}
	stream.SetDeadline(time.Time{})
	s.wg.Go(func() { s.reconcileDevice(d.ID) })
	s.wg.Go(func() { s.heartbeat(p) })
	s.wg.Go(func() { s.acceptClientStreams(p) })
	s.wg.Go(func() { s.confirmUpgrade(d.ID, h.Version, h.UpgradeTask) })
	if session.QUIC != nil {
		s.wg.Go(func() { s.receiveUDP(p) })
	}
	for {
		stream.SetDeadline(time.Now().Add(protocol.ControlIdleTimeout))
		msg, e := codec.Receive()
		if e != nil {
			if s.ctx.Err() == nil {
				kind := transport.ErrorKind(e)
				level := slog.LevelWarn
				if kind == "eof" || kind == "connection_closed" {
					level = slog.LevelInfo
				}
				slog.Log(s.ctx, level, "设备控制连接已结束", "设备", d.ID, "传输", session.Network, "错误类型", kind, "错误", e, "接入诊断", session.Diagnostics())
			}
			return
		}
		if ch, ok := p.pending.Load(msg.ID); ok {
			select {
			case ch.(chan protocol.Message) <- msg:
			default:
			}
		}
		if msg.Type == "upgrade_status" {
			s.upgradeStatus(d.ID, msg)
		}
	}
}
func (s *Server) request(p *peer, m protocol.Message, timeout time.Duration) (protocol.Message, error) {
	started := time.Now()
	if m.ID == "" {
		m.ID = transport.RandomID()
	}
	ch := make(chan protocol.Message, 1)
	p.pending.Store(m.ID, ch)
	defer p.pending.Delete(m.ID)
	if err := p.control.Send(m); err != nil {
		return protocol.Message{}, fmt.Errorf("发送控制消息 %s 失败（请求 %s，耗时 %s）：%w", m.Type, m.ID, time.Since(started), err)
	}
	select {
	case reply := <-ch:
		if reply.Error != "" {
			return reply, errors.New(reply.Error)
		}
		return reply, nil
	case <-time.After(timeout):
		return protocol.Message{}, fmt.Errorf("等待客户端 %s 响应超时（请求 %s，耗时 %s）", m.Type, m.ID, time.Since(started))
	case <-p.done:
		return protocol.Message{}, errors.New("设备离线")
	case <-s.ctx.Done():
		return protocol.Message{}, s.ctx.Err()
	}
}
func (s *Server) heartbeat(p *peer) {
	timer := time.NewTicker(protocol.HeartbeatInterval)
	defer timer.Stop()
	for {
		start := time.Now()
		_, err := s.request(p, protocol.Message{Type: "ping"}, protocol.HeartbeatTimeout)
		if err != nil {
			select {
			case <-p.done:
				return
			case <-s.ctx.Done():
				return
			default:
			}
			if s.ctx.Err() == nil {
				s.mu.RLock()
				var active int
				var last time.Time
				var up, down int64
				for _, c := range s.connections {
					if c.record.DeviceID == p.device.ID {
						active++
						c.mu.Lock()
						up += c.record.Up
						down += c.record.Down
						if c.last.After(last) {
							last = c.last
						}
						c.mu.Unlock()
					}
				}
				s.mu.RUnlock()
				slog.Warn("设备心跳失败，关闭接入", "设备", p.device.ID, "传输", p.session.Network, "错误", err, "等待", time.Since(start), "活动连接", active, "最近业务活动", last, "业务上行字节", up, "业务下行字节", down, "接入诊断", p.session.Diagnostics())
			}
			if p.session.QUIC != nil {
				p.session.QUIC.CloseWithError(2, "心跳检测失败："+err.Error())
			}
			p.session.Close()
			return
		}
		rtt := float64(time.Since(start).Microseconds()) / 1000
		s.mu.Lock()
		d := s.devices[p.device.ID]
		d.RTT = &rtt
		d.LastSeen = time.Now().UTC()
		s.devices[d.ID] = d
		s.mu.Unlock()
		select {
		case <-timer.C:
		case <-p.done:
			return
		case <-s.ctx.Done():
			return
		}
	}
}
