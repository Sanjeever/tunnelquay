package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/store"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

type runtimeTunnel struct {
	cfg      store.Tunnel
	allow    []netip.Prefix
	tcp      net.Listener
	udp      *net.UDPConn
	retired  bool
	sessions map[string]*activeConnection
}

func parseAllowCIDRs(raw string) ([]netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
	prefixes := make([]netip.Prefix, 0, len(fields))
	for _, token := range fields {
		if strings.Contains(token, "/") {
			p, err := netip.ParsePrefix(token)
			if err != nil {
				return nil, fmt.Errorf("来源 CIDR 格式无效：%s", token)
			}
			prefixes = append(prefixes, p.Masked())
		} else {
			addr, err := netip.ParseAddr(token)
			if err != nil {
				return nil, fmt.Errorf("来源 IP 格式无效：%s", token)
			}
			addr = addr.Unmap()
			prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
		}
	}
	return prefixes, nil
}

func (r *runtimeTunnel) allowsSource(source string) bool {
	if len(r.allow) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(source)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range r.allow {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func (r *runtimeTunnel) close() {
	if r.tcp != nil {
		r.tcp.Close()
	}
	if r.udp != nil {
		r.udp.Close()
	}
}

func protocolsOverlap(a, b string) bool { return a == b || a == "both" || b == "both" }

func validateConfig(c store.Tunnel) error {
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 128 {
		return errors.New("隧道名称须为 1 至 128 字符")
	}
	if c.Protocol != "tcp" && c.Protocol != "udp" && c.Protocol != "both" {
		return errors.New("协议必须为 TCP、UDP 或 TCP + UDP")
	}
	if strings.TrimSpace(c.Host) == "" || len(c.Host) > 253 || strings.ContainsAny(c.Host, " /\\\r\n\t") {
		return errors.New("目标地址无效，请填写 IP 或域名")
	}
	if c.TargetPort < 1 || c.TargetPort > 65535 {
		return errors.New("目标端口须为 1 至 65535")
	}
	if c.PublicPort < 0 || c.PublicPort > 65535 {
		return errors.New("公网端口须为 1 至 65535，自动分配填写 0")
	}
	if _, err := parseAllowCIDRs(c.AllowCIDRs); err != nil {
		return err
	}
	return nil
}

func bind(cfg store.Tunnel) (*runtimeTunnel, error) {
	return prepareBind(cfg, nil)
}

func prepareBind(cfg store.Tunnel, old *runtimeTunnel) (*runtimeTunnel, error) {
	allow, err := parseAllowCIDRs(cfg.AllowCIDRs)
	if err != nil {
		return nil, err
	}
	r := &runtimeTunnel{cfg: cfg, allow: allow, sessions: map[string]*activeConnection{}}
	addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(cfg.PublicPort))
	lc := transport.ListenConfig()
	if (cfg.Protocol == "tcp" || cfg.Protocol == "both") && (old == nil || old.tcp == nil) {
		r.tcp, err = lc.Listen(context.Background(), "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("TCP 公网端口监听失败：%w", err)
		}
	}
	if (cfg.Protocol == "udp" || cfg.Protocol == "both") && (old == nil || old.udp == nil) {
		p, e := lc.ListenPacket(context.Background(), "udp", addr)
		if e == nil {
			r.udp = p.(*net.UDPConn)
		}
		if e != nil {
			r.close()
			return nil, fmt.Errorf("UDP 公网端口监听失败：%w", e)
		}
	}
	return r, nil
}

func (s *Server) portFreeLocked(cfg store.Tunnel) bool {
	if s.listener != nil {
		_, port, _ := net.SplitHostPort(s.Addr())
		if strconv.Itoa(cfg.PublicPort) == port {
			return false
		}
	}
	for id, t := range s.tunnels {
		if id == cfg.ID {
			continue
		}
		if t.PublicPort == cfg.PublicPort && protocolsOverlap(t.Protocol, cfg.Protocol) {
			return false
		}
	}
	return true
}

func (s *Server) SaveTunnel(cfg store.Tunnel) (store.Tunnel, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := validateConfig(cfg); err != nil {
		return store.Tunnel{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return store.Tunnel{}, errors.New("服务端正在退出")
	}
	if _, ok := s.devices[cfg.DeviceID]; !ok {
		return store.Tunnel{}, errors.New("设备不存在")
	}
	if cfg.Enabled && s.devices[cfg.DeviceID].Revoked {
		return store.Tunnel{}, errors.New("设备身份已撤销，无法启用隧道")
	}
	var t store.Tunnel
	if cfg.ID != "" {
		old := s.tunnels[cfg.ID]
		if old == nil {
			return t, errors.New("隧道不存在")
		}
		if old.DeviceID != cfg.DeviceID {
			return t, errors.New("已有隧道不能更换设备")
		}
		t = *old
		if old.Host != cfg.Host || old.TargetPort != cfg.TargetPort || old.Protocol != cfg.Protocol {
			t.TargetStatus = "unknown"
			t.TargetError = ""
		}
	} else {
		cfg.ID = transport.RandomID()
		t.TargetStatus = "unknown"
	}
	var prepared *runtimeTunnel
	if cfg.PublicPort == 0 {
		minPort, maxPort, err := s.portRange()
		if err != nil {
			return t, err
		}
		for p := minPort; p <= maxPort; p++ {
			cfg.PublicPort = p
			if !s.portFreeLocked(cfg) {
				continue
			}
			prepared, err = bind(cfg)
			if err == nil {
				break
			}
		}
		if prepared == nil {
			return t, errors.New("自动端口范围内没有可用端口")
		}
	} else if !s.portFreeLocked(cfg) {
		return t, errors.New("公网端口已分配或与服务端接入端口冲突")
	}
	t.ID = cfg.ID
	t.Name = cfg.Name
	t.DeviceID = cfg.DeviceID
	t.Protocol = cfg.Protocol
	t.Host = cfg.Host
	t.TargetPort = cfg.TargetPort
	t.PublicPort = cfg.PublicPort
	t.AllowCIDRs = strings.TrimSpace(cfg.AllowCIDRs)
	t.Enabled = cfg.Enabled
	t.Error = ""

	if !t.Enabled {
		if prepared != nil {
			prepared.close()
		}
		t.Status = "disabled"
		if err := s.Store.Put("tunnels", t.ID, t); err != nil {
			return t, err
		}
		s.stopLocked(t.ID)
		s.tunnels[t.ID] = &t
		return t, nil
	}
	p := s.peers[t.DeviceID]
	if p == nil {
		if prepared != nil {
			prepared.close()
		}
		t.Status = "offline"
		if err := s.Store.Put("tunnels", t.ID, t); err != nil {
			return t, err
		}
		s.tunnels[t.ID] = &t
		return t, nil
	}
	oldRun := s.running[t.ID]
	reuse := oldRun != nil && oldRun.cfg.PublicPort == t.PublicPort
	if reuse && prepared != nil {
		prepared.close()
		prepared = nil
	}
	if prepared == nil {
		var shared *runtimeTunnel
		if reuse {
			shared = oldRun
		}
		var err error
		prepared, err = prepareBind(t, shared)
		if err != nil {
			return t, err
		}
	}
	t.Status = "running"
	if err := s.Store.Put("tunnels", t.ID, t); err != nil {
		if prepared != nil {
			prepared.close()
		}
		return t, err
	}
	s.tunnels[t.ID] = &t
	if reuse {
		oldRun.cfg = t
		oldRun.allow = prepared.allow
		if prepared.tcp != nil {
			oldRun.tcp = prepared.tcp
			listener := oldRun.tcp
			s.wg.Go(func() { s.acceptTCP(oldRun, p, listener) })
		}
		if prepared.udp != nil {
			oldRun.udp = prepared.udp
			conn := oldRun.udp
			s.wg.Go(func() { s.acceptUDP(oldRun, p, conn) })
		}
		if t.Protocol == "udp" && oldRun.tcp != nil {
			oldRun.tcp.Close()
			oldRun.tcp = nil
		}
		if t.Protocol == "tcp" && oldRun.udp != nil {
			oldRun.udp.Close()
			oldRun.udp = nil
			for _, c := range oldRun.sessions {
				c.close()
			}
		}
		return t, nil
	}
	if oldRun != nil {
		oldRun.retired = true
		oldRun.close()
		for _, c := range oldRun.sessions {
			c.close()
		}
	}
	s.running[t.ID] = prepared
	s.startRuntime(prepared, p)
	return t, nil
}

func (s *Server) reconcileDevice(id string) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	p := s.peers[id]
	if p == nil || s.devices[id].Revoked {
		s.mu.Unlock()
		return
	}
	var probeIDs []string
	for _, t := range s.tunnels {
		if t.DeviceID != id || !t.Enabled || s.running[t.ID] != nil {
			continue
		}
		r, err := bind(*t)
		if err != nil {
			t.Status = "failed"
			t.Error = err.Error()
			_ = s.Store.Put("tunnels", t.ID, *t)
			continue
		}
		t.Status = "running"
		t.Error = ""
		_ = s.Store.Put("tunnels", t.ID, *t)
		s.running[t.ID] = r
		s.startRuntime(r, p)
		probeIDs = append(probeIDs, t.ID)
	}
	s.mu.Unlock()
	for _, tid := range probeIDs {
		tunnelID := tid
		s.wg.Go(func() { _, _ = s.ProbeTunnel(tunnelID) })
	}
}

func (s *Server) stopLocked(id string) {
	if r := s.running[id]; r != nil {
		r.retired = true
		r.close()
		delete(s.running, id)
	}
	for _, c := range s.connections {
		if c.record.TunnelID == id {
			c.close()
		}
	}
}

func (s *Server) DeleteTunnel(id string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tunnels[id] == nil {
		return errors.New("隧道不存在")
	}
	if err := s.Store.DeleteTunnel(id); err != nil {
		return err
	}
	s.stopLocked(id)
	delete(s.tunnels, id)
	delete(s.rateSamples, id)
	return nil
}

func (s *Server) ProbeTunnel(id string) (store.Tunnel, error) {
	s.mu.RLock()
	existing := s.tunnels[id]
	if existing == nil {
		s.mu.RUnlock()
		return store.Tunnel{}, errors.New("隧道不存在")
	}
	t := *existing
	p := s.peers[t.DeviceID]
	s.mu.RUnlock()
	if p == nil {
		return store.Tunnel{}, errors.New("设备当前离线，无法探测目标")
	}
	network := "tcp"
	if t.Protocol == "udp" {
		network = "udp"
	}
	target := net.JoinHostPort(t.Host, strconv.Itoa(t.TargetPort))
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	stream, err := p.session.Open(ctx)
	if err == nil {
		stream.SetDeadline(time.Now().Add(3 * time.Second))
		if err = protocol.Write(stream, protocol.Message{Type: "dial", Network: network, Target: target}); err == nil {
			var reply protocol.Message
			if err = protocol.Read(stream, &reply); err == nil && reply.Error != "" {
				err = errors.New(reply.Error)
			}
		}
		stream.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.tunnels[id]
	if current == nil {
		return store.Tunnel{}, errors.New("隧道不存在")
	}
	if err != nil {
		current.TargetStatus = "failed"
		current.TargetError = "目标连接失败：" + err.Error()
	} else {
		current.TargetStatus = "reachable"
		current.TargetError = ""
	}
	_ = s.Store.Put("tunnels", current.ID, *current)
	return *current, nil
}

func (s *Server) portRange() (int, int, error) {
	v, err := s.Store.Get("port_range")
	if err != nil {
		return 0, 0, err
	}
	if v == "" {
		return 20000, 30000, nil
	}
	var a, b int
	if _, err = fmt.Sscanf(v, "%d-%d", &a, &b); err != nil {
		return 0, 0, err
	}
	return a, b, nil
}

func (s *Server) startRuntime(r *runtimeTunnel, p *peer) {
	if r.tcp != nil {
		listener := r.tcp
		s.wg.Go(func() { s.acceptTCP(r, p, listener) })
	}
	if r.udp != nil {
		conn := r.udp
		s.wg.Go(func() { s.acceptUDP(r, p, conn) })
	}
}

func (s *Server) maintenance() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	count := 0
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			count++
			s.mu.Lock()
			now := time.Now()
			for _, c := range s.connections {
				c.mu.Lock()
				idle := c.record.Protocol == "udp" && now.Sub(c.last) > 60*time.Second
				c.mu.Unlock()
				if idle {
					c.close()
				}
			}
			live := map[string][2]int64{}
			for _, c := range s.connections {
				c.mu.Lock()
				total := live[c.record.TunnelID]
				total[0] += c.record.Up
				total[1] += c.record.Down
				live[c.record.TunnelID] = total
				c.mu.Unlock()
			}
			elapsed := now.Sub(s.rateSampleAt).Seconds()
			for id, t := range s.tunnels {
				total := live[id]
				total[0] += t.Up
				total[1] += t.Down
				previous := s.rateSamples[id]
				t.UpRate = float64(max(0, total[0]-previous[0])) / elapsed
				t.DownRate = float64(max(0, total[1]-previous[1])) / elapsed
				s.rateSamples[id] = total
			}
			s.rateSampleAt = now
			for key, v := range s.sessions {
				if now.After(v.Expires) {
					delete(s.sessions, key)
				}
			}
			for ip, v := range s.loginAttempts {
				if now.Sub(v.window) > time.Minute {
					delete(s.loginAttempts, ip)
				}
			}
			s.mu.Unlock()
			if err := s.flushHistory(); err != nil {
				reportError("保存连接记录失败", err)
			}
			if count%60 == 0 {
				if err := s.Store.Prune(now); err != nil {
					reportError("清理连接记录失败", err)
				}
			}
		}
	}
}
