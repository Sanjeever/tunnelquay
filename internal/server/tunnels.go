package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/store"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

type runtimeTunnel struct {
	cfg      protocol.Config
	tcp      net.Listener
	udp      *net.UDPConn
	retired  bool
	sessions map[string]*activeConnection
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
func validateConfig(c protocol.Config) error {
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
	return nil
}
func bind(cfg protocol.Config) (*runtimeTunnel, error) {
	r := &runtimeTunnel{cfg: cfg, sessions: map[string]*activeConnection{}}
	addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(cfg.PublicPort))
	var err error
	lc := transport.ListenConfig()
	if cfg.Protocol == "tcp" || cfg.Protocol == "both" {
		r.tcp, err = lc.Listen(context.Background(), "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("TCP 公网端口监听失败：%w", err)
		}
	}
	if cfg.Protocol == "udp" || cfg.Protocol == "both" {
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
func (s *Server) portFreeLocked(cfg protocol.Config) bool {
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
		if t.Desired.PublicPort == cfg.PublicPort && protocolsOverlap(t.Desired.Protocol, cfg.Protocol) {
			return false
		}
		if t.Applied != nil && t.Applied.PublicPort == cfg.PublicPort && protocolsOverlap(t.Applied.Protocol, cfg.Protocol) {
			return false
		}
	}
	return true
}
func (s *Server) SaveTunnel(cfg protocol.Config) (store.Tunnel, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := validateConfig(cfg); err != nil {
		return store.Tunnel{}, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return store.Tunnel{}, errors.New("服务端正在退出")
	}
	if _, ok := s.devices[cfg.DeviceID]; !ok {
		s.mu.Unlock()
		return store.Tunnel{}, errors.New("设备不存在")
	}
	var t store.Tunnel
	if cfg.ID != "" {
		old := s.tunnels[cfg.ID]
		if old == nil {
			s.mu.Unlock()
			return t, errors.New("隧道不存在")
		}
		t = *old
		cfg.Revision = old.Desired.Revision + 1
		if old.Desired.DeviceID != cfg.DeviceID {
			s.mu.Unlock()
			return t, errors.New("已有隧道不能更换设备")
		}
	} else {
		cfg.ID = transport.RandomID()
		cfg.Revision = 1
	}
	var prepared *runtimeTunnel
	if cfg.PublicPort == 0 {
		minPort, maxPort, err := s.portRange()
		if err != nil {
			s.mu.Unlock()
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
			s.mu.Unlock()
			return t, errors.New("自动端口范围内没有可用端口")
		}
	} else if !s.portFreeLocked(cfg) {
		s.mu.Unlock()
		return t, errors.New("公网端口已分配或与服务端接入端口冲突")
	}
	t.Desired = cfg
	t.Error = ""
	if err := s.Store.Put("tunnels", cfg.ID, t); err != nil {
		if prepared != nil {
			prepared.close()
		}
		s.mu.Unlock()
		return t, err
	}
	s.tunnels[cfg.ID] = &t
	if !cfg.Enabled {
		s.stopLocked(cfg.ID)
		t.Status = "disabled"
		if prepared != nil {
			prepared.close()
		}
		s.mu.Unlock()
		return t, nil
	}
	p := s.peers[cfg.DeviceID]
	s.mu.Unlock()
	if p == nil {
		if prepared != nil {
			prepared.close()
		}
		s.mu.Lock()
		t.Status = "offline"
		s.mu.Unlock()
		return t, nil
	}
	s.apply(&t, p, prepared)
	s.mu.RLock()
	result := t
	s.mu.RUnlock()
	return result, nil
}
func (s *Server) apply(t *store.Tunnel, p *peer, prepared *runtimeTunnel) {
	s.mu.Lock()
	cfg := t.Desired
	old := s.running[cfg.ID]
	reuse := old != nil && old.cfg.Protocol == cfg.Protocol && old.cfg.PublicPort == cfg.PublicPort
	var err error
	if reuse && prepared != nil {
		prepared.close()
		prepared = nil
	}
	if !reuse && prepared == nil {
		prepared, err = bind(cfg)
	}
	s.mu.Unlock()
	if err == nil {
		_, err = s.request(p, protocol.Message{Type: "prepare", Config: &cfg, Revision: cfg.Revision})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if prepared != nil {
			prepared.close()
		}
		t.Error = err.Error()
		if old != nil {
			t.Status = "running"
		} else {
			t.Status = "failed"
		}
		if e := s.Store.Put("tunnels", cfg.ID, t); e != nil {
			t.Error += "；保存应用结果失败：" + e.Error()
		}
		return
	}
	updated := *t
	updated.Applied = &cfg
	updated.Error = ""
	updated.Status = "running"
	if t.Applied == nil || t.Applied.Host != cfg.Host || t.Applied.TargetPort != cfg.TargetPort || t.Applied.Protocol != cfg.Protocol {
		updated.TargetStatus = "unknown"
	}
	if err = s.Store.Put("tunnels", cfg.ID, updated); err != nil {
		if prepared != nil {
			prepared.close()
		}
		t.Error = "保存生效配置失败：" + err.Error()
		return
	}
	*t = updated
	if reuse {
		old.cfg = cfg
		return
	}
	if old != nil {
		old.retired = true
		if old.tcp != nil {
			old.tcp.Close()
		}
		if old.udp != nil && len(old.sessions) == 0 {
			old.udp.Close()
		}
	}
	s.running[cfg.ID] = prepared
	s.generations[cfg.ID] = append(s.generations[cfg.ID], prepared)
	s.startRuntime(prepared, p)
}
func (s *Server) reconcileDevice(id string) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.RLock()
	p := s.peers[id]
	var items []*store.Tunnel
	for _, t := range s.tunnels {
		if t.Desired.DeviceID == id && t.Desired.Enabled {
			items = append(items, t)
		}
	}
	s.mu.RUnlock()
	if p == nil {
		return
	}
	for _, t := range items {
		s.apply(t, p, nil)
		s.mu.RLock()
		fallback := s.running[t.Desired.ID] == nil && t.Applied != nil
		var applied protocol.Config
		if fallback {
			applied = *t.Applied
		}
		s.mu.RUnlock()
		if fallback {
			r, err := bind(applied)
			if err == nil {
				_, err = s.request(p, protocol.Message{Type: "prepare", Config: &applied})
			}
			if err != nil {
				if r != nil {
					r.close()
				}
				continue
			}
			s.mu.Lock()
			s.running[applied.ID] = r
			s.generations[applied.ID] = append(s.generations[applied.ID], r)
			t.Status = "running"
			s.startRuntime(r, p)
			s.mu.Unlock()
		}
	}
}
func (s *Server) stopLocked(id string) {
	for _, r := range s.generations[id] {
		r.retired = true
		r.close()
	}
	delete(s.generations, id)
	delete(s.running, id)
	for _, c := range s.connections {
		if c.record.TunnelID == id {
			c.close()
		}
	}
	if t := s.tunnels[id]; t != nil {
		if p := s.peers[t.Desired.DeviceID]; p != nil {
			message := protocol.Message{Type: "stop", Config: &protocol.Config{ID: id, Revision: t.Desired.Revision}}
			s.wg.Go(func() {
				if err := p.control.Send(message); err != nil {
					p.session.Close()
				}
			})
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
	return nil
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
		s.wg.Go(func() { s.acceptTCP(r, p) })
	}
	if r.udp != nil {
		s.wg.Go(func() { s.acceptUDP(r, p) })
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
			for id, all := range s.generations {
				next := all[:0]
				for _, r := range all {
					if r.retired && len(r.sessions) == 0 {
						r.close()
					} else {
						next = append(next, r)
					}
				}
				s.generations[id] = next
			}
			for _, t := range s.tunnels {
				var up, down int64
				for _, c := range s.connections {
					if c.record.TunnelID == t.Desired.ID {
						c.mu.Lock()
						up += c.record.Up - c.prevUp
						down += c.record.Down - c.prevDown
						c.prevUp = c.record.Up
						c.prevDown = c.record.Down
						c.mu.Unlock()
					}
				}
				t.UpRate = float64(up)
				t.DownRate = float64(down)
			}
			for key, v := range s.sessions {
				if now.After(v.Expires) {
					delete(s.sessions, key)
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
