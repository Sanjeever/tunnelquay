package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/client"
	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/store"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

func udpModeTarget(t *testing.T, prefix string) *net.UDPConn {
	t.Helper()
	u, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { u.Close() })
	go func() {
		b := make([]byte, 65535)
		for {
			n, addr, err := u.ReadFromUDP(b)
			if err != nil {
				return
			}
			u.WriteToUDP(append([]byte(prefix), b[:n]...), addr)
		}
	}()
	return u
}

func udpModeSource(t *testing.T, port int) net.Conn {
	t.Helper()
	c, err := net.Dial("udp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func checkUDPMode(t *testing.T, c net.Conn, prefix string, size int) {
	t.Helper()
	payload := bytes.Repeat([]byte(c.LocalAddr().String()+"/UDP"), size)
	payload = payload[:size]
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(payload); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 65535)
	n, err := c.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], append([]byte(prefix), payload...)) {
		t.Fatalf("UDP 回包不符：%v，收到 %d 字节", err, n)
	}
}

func TestUDPModeSwitchRetainsSessionsAndStopsAllGenerations(t *testing.T) {
	for _, network := range []string{"quic", "tcp"} {
		for _, mode := range []string{"udp", "both"} {
			t.Run(network+"/"+mode, func(t *testing.T) {
				s := testServer(t)
				id, _ := testClient(t, s, network)
				a, b := udpModeTarget(t, "A"), udpModeTarget(t, "B")
				cfg := cfgFor(id, mode, a.LocalAddr().String())
				tun, err := s.SaveTunnel(cfg)
				if err != nil || tun.Error != "" || tun.Desired.UDPMode != protocol.UDPModeDatagram {
					t.Fatal("默认 DATAGRAM 配置失败", err, tun)
				}
				old := udpModeSource(t, tun.Desired.PublicPort)
				checkUDPMode(t, old, "A", 8192)
				sources := []net.Conn{old}
				cfg = tun.Desired
				cfg.Host, cfg.TargetPort = "127.0.0.1", b.LocalAddr().(*net.UDPAddr).Port
				for _, carrier := range []string{protocol.UDPModeStream, protocol.UDPModeDatagram, protocol.UDPModeStream} {
					cfg.UDPMode = carrier
					updated, err := s.SaveTunnel(cfg)
					if err != nil || updated.Error != "" || updated.Applied.UDPMode != carrier {
						t.Fatal("切换承载失败", err, updated)
					}
					for i := 0; i < 3; i++ {
						current := udpModeSource(t, cfg.PublicPort)
						checkUDPMode(t, current, "B", 8192)
						s.mu.RLock()
						active := s.running[cfg.ID].sessions[current.LocalAddr().String()]
						retained := s.running[cfg.ID].sessions[old.LocalAddr().String()]
						correct := active != nil && active.udpMode == carrier && retained != nil && retained.udpMode == protocol.UDPModeDatagram
						s.mu.RUnlock()
						if !correct {
							t.Fatal("新旧来源的承载未隔离")
						}
						sources = append(sources, current)
					}
					checkUDPMode(t, old, "A", 64)
				}
				cfg.PublicPort = 0
				moved, err := s.SaveTunnel(cfg)
				if err != nil || moved.Error != "" || moved.Desired.PublicPort == tun.Desired.PublicPort {
					t.Fatal("可靠承载迁移公网端口失败", err, moved)
				}
				checkUDPMode(t, old, "A", 64)
				checkUDPMode(t, sources[1], "B", 64)
				checkUDPMode(t, udpModeSource(t, moved.Desired.PublicPort), "B", 8192)
				cfg = moved.Desired
				cfg.Enabled = false
				if _, err = s.SaveTunnel(cfg); err != nil {
					t.Fatal(err)
				}
				waitFor(t, func() bool {
					s.mu.RLock()
					defer s.mu.RUnlock()
					return len(s.connections) == 0 && len(s.generations) == 0 && s.udpQueuedBytes.Load() == 0
				})
				for _, c := range []net.Conn{old, sources[1]} {
					c.SetDeadline(time.Now().Add(100 * time.Millisecond))
					c.Write([]byte("停用后"))
					if _, err = c.Read(make([]byte, 64)); err == nil {
						t.Fatal("停用后旧代次仍可转发")
					}
				}
				cfg.Enabled = true
				if restored, err := s.SaveTunnel(cfg); err != nil || restored.Error != "" {
					t.Fatal("重新启用失败", err, restored.Error)
				}
				checkUDPMode(t, udpModeSource(t, cfg.PublicPort), "B", 8192)
				if err = s.DeleteTunnel(cfg.ID); err != nil {
					t.Fatal(err)
				}
				waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return len(s.connections) == 0 })
			})
		}
	}
}

func TestUDPModeStreamMaximumDatagram(t *testing.T) {
	for _, network := range []string{"quic", "tcp"} {
		t.Run(network, func(t *testing.T) {
			s := testServer(t)
			id, _ := testClient(t, s, network)
			target := udpModeTarget(t, "")
			cfg := cfgFor(id, "udp", target.LocalAddr().String())
			cfg.UDPMode = protocol.UDPModeStream
			tun, err := s.SaveTunnel(cfg)
			if err != nil || tun.Error != "" {
				t.Fatal(err, tun.Error)
			}
			for source := 0; source < 3; source++ {
				c := udpModeSource(t, tun.Desired.PublicPort)
				c.SetDeadline(time.Now().Add(3 * time.Second))
				payload := bytes.Repeat([]byte{byte(source + 1)}, 65507)
				if _, err = c.Write(payload); err != nil {
					if errors.Is(err, syscall.EMSGSIZE) {
						t.Skipf("本机系统拒绝发送最大 UDP 数据报：%v", err)
					}
					t.Fatal(err)
				}
				buf := make([]byte, 65535)
				n, err := c.Read(buf)
				if err != nil || !bytes.Equal(buf[:n], payload) {
					t.Fatal("最大可靠 UDP 回包不符", err, n)
				}
			}
		})
	}
}

func TestUDPModeReconnectAndServerRestart(t *testing.T) {
	for _, network := range []string{"quic", "tcp"} {
		t.Run(network, func(t *testing.T) {
			dir := t.TempDir()
			s, err := New(Options{Listen: "127.0.0.1:0", DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			if err = s.Start(); err != nil {
				t.Fatal(err)
			}
			a, b := udpModeTarget(t, "A"), udpModeTarget(t, "B")
			opts := client.Options{Address: s.Addr(), Credential: s.Identity.Credential(), Transport: network, DataDir: t.TempDir()}
			var stop func()
			connect := func() string {
				c, err := client.New(opts)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() { defer close(done); c.Connect(ctx) }()
				stop = func() { cancel(); <-done }
				waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.devices[c.ID].Online })
				return c.ID
			}
			id := connect()
			defer func() { stop() }()
			cfg := cfgFor(id, "both", a.LocalAddr().String())
			cfg.UDPMode = protocol.UDPModeStream
			tun, err := s.SaveTunnel(cfg)
			if err != nil || tun.Error != "" {
				t.Fatal(err, tun.Error)
			}
			checkUDPMode(t, udpModeSource(t, tun.Desired.PublicPort), "A", 8192)
			stop()
			waitFor(t, func() bool {
				s.mu.RLock()
				defer s.mu.RUnlock()
				return !s.devices[id].Online && len(s.connections) == 0
			})
			cfg = tun.Desired
			cfg.UDPMode = protocol.UDPModeDatagram
			cfg.TargetPort = b.LocalAddr().(*net.UDPAddr).Port
			if _, err = s.SaveTunnel(cfg); err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = New(Options{Listen: "127.0.0.1:0", DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Start(); err != nil {
				t.Fatal(err)
			}
			opts.Address = s.Addr()
			if next := connect(); next != id {
				t.Fatal("重启后身份改变")
			}
			waitFor(t, func() bool {
				s.mu.RLock()
				defer s.mu.RUnlock()
				r := s.running[cfg.ID]
				return r != nil && r.cfg.UDPMode == protocol.UDPModeDatagram
			})
			checkUDPMode(t, udpModeSource(t, cfg.PublicPort), "B", 8192)
			stop()
			waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.devices[id].Online })
			cfg.UDPMode, cfg.Enabled = protocol.UDPModeStream, false
			if _, err = s.SaveTunnel(cfg); err != nil {
				t.Fatal(err)
			}
			connect()
			s.mu.RLock()
			running := s.running[cfg.ID]
			s.mu.RUnlock()
			if running != nil {
				t.Fatal("重连重新启用已停用的可靠承载隧道")
			}
			cfg.Enabled = true
			if applied, err := s.SaveTunnel(cfg); err != nil || applied.Error != "" {
				t.Fatal(err, applied.Error)
			}
			checkUDPMode(t, udpModeSource(t, cfg.PublicPort), "B", 8192)
		})
	}
}

func testUDPModePeer(t *testing.T, s *Server, key ed25519.PrivateKey, capabilities []string) (*transport.Session, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	session, err := transport.Dial(ctx, s.Addr(), "quic", transport.ClientTLS(s.Identity.Fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := session.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream.SetDeadline(time.Now().Add(3 * time.Second))
	codec := &protocol.Codec{RW: stream}
	welcome := testIdentityProof(t, codec, testDeviceHello(key, s.Identity.Secret, capabilities), key, s.Identity.Fingerprint)
	if welcome.Type != "welcome" || !slices.Equal(welcome.Capabilities, protocol.NegotiateCapabilities(capabilities)) {
		t.Fatal("能力协商响应错误", welcome)
	}
	stream.SetDeadline(time.Time{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			m, e := codec.Receive()
			if e != nil {
				return
			}
			kind := "ack"
			if m.Type == "ping" {
				kind = "pong"
			}
			if codec.Send(protocol.Message{Type: kind, ID: m.ID, Revision: m.Revision}) != nil {
				return
			}
		}
	}()
	stop := func() { cancel(); session.Close(); <-done }
	t.Cleanup(stop)
	return session, stop
}

func TestUDPModeUnsupportedPeerRetainsApplied(t *testing.T) {
	s := testServer(t)
	key := testDeviceKey(t)
	id := protocol.DeviceID(key.Public().(ed25519.PublicKey))
	_, stop := testUDPModePeer(t, s, key, []string{"config-v1", "tcp", "udp"})
	u := udpModeTarget(t, "")
	tun, err := s.SaveTunnel(cfgFor(id, "udp", u.LocalAddr().String()))
	if err != nil || tun.Error != "" {
		t.Fatal(err, tun.Error)
	}
	cfg := tun.Desired
	cfg.UDPMode = protocol.UDPModeStream
	failed, err := s.SaveTunnel(cfg)
	if err != nil || !strings.Contains(failed.Error, protocol.CapabilityUDPStream) || failed.Applied.UDPMode != protocol.UDPModeDatagram {
		t.Fatal("未保留旧生效承载", err, failed)
	}
	stop()
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.devices[id].Online })
	testUDPModePeer(t, s, key, []string{"config-v1", "tcp", "udp"})
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		r := s.running[cfg.ID]
		return r != nil && r.cfg.UDPMode == protocol.UDPModeDatagram
	})
	s.mu.RLock()
	retained := *s.tunnels[cfg.ID]
	s.mu.RUnlock()
	if retained.Desired.UDPMode != protocol.UDPModeStream || !strings.Contains(retained.Error, protocol.CapabilityUDPStream) {
		t.Fatal("恢复丢失期望模式或失败原因", retained)
	}
	bad := cfg
	bad.UDPMode = "unknown"
	if _, err = s.SaveTunnel(bad); err == nil {
		t.Fatal("接受未知 UDP 承载")
	}
}

func TestUDPStreamRequiresForwardConfirmation(t *testing.T) {
	s := testServer(t)
	key := testDeviceKey(t)
	id := protocol.DeviceID(key.Public().(ed25519.PublicKey))
	session, _ := testUDPModePeer(t, s, key, protocol.Capabilities)
	u := udpModeTarget(t, "")
	cfg := cfgFor(id, "udp", u.LocalAddr().String())
	cfg.UDPMode = protocol.UDPModeStream
	tun, err := s.SaveTunnel(cfg)
	if err != nil || tun.Error != "" {
		t.Fatal(err, tun.Error)
	}
	conn := udpModeSource(t, tun.Desired.PublicPort)
	if _, err = conn.Write([]byte("确认前不能发送")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := session.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var request protocol.Message
	if err = protocol.Read(stream, &request); err != nil || request.Config.UDPMode != protocol.UDPModeStream {
		t.Fatal(err, request)
	}
	if err = protocol.Write(stream, protocol.Message{Type: "ready"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return len(s.connections) == 0 })
	if err = s.flushHistory(); err != nil {
		t.Fatal(err)
	}
	items, total, err := s.Store.History(store.Filter{Page: 1, Limit: 10})
	if err != nil || total != 1 || items[0].Result != "failed" || !strings.Contains(items[0].Reason, "未确认可靠 UDP 承载") {
		t.Fatal("未确认模式却开始转发", err, items)
	}
}

func TestUDPModeOldPeerCannotRestoreStream(t *testing.T) {
	s := testServer(t)
	key := testDeviceKey(t)
	id := protocol.DeviceID(key.Public().(ed25519.PublicKey))
	_, stop := testUDPModePeer(t, s, key, protocol.Capabilities)
	target := udpModeTarget(t, "")
	cfg := cfgFor(id, "udp", target.LocalAddr().String())
	cfg.UDPMode = protocol.UDPModeStream
	tun, err := s.SaveTunnel(cfg)
	if err != nil || tun.Error != "" {
		t.Fatal(err, tun.Error)
	}
	stop()
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.devices[id].Online })
	testUDPModePeer(t, s, key, []string{"config-v1", "tcp", "udp"})
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return strings.Contains(s.tunnels[tun.Desired.ID].Error, protocol.CapabilityUDPStream)
	})
	s.mu.RLock()
	running := s.running[tun.Desired.ID]
	status := s.tunnels[tun.Desired.ID].Status
	s.mu.RUnlock()
	if running != nil || status != "failed" {
		t.Fatal("旧客户端恢复了无法识别的可靠模式", status)
	}
}
