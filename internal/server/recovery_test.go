package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/client"
)

func TestRestartRetainsConfigWhenUpdateCannotBind(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{Listen: "127.0.0.1:0", DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	target := tcpTarget(t, "保留")
	opts := client.Options{Address: s.Addr(), Credential: s.Identity.Credential(), DataDir: t.TempDir()}
	c, err := client.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Connect(ctx) }()
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.devices[c.ID].Online })
	tun, err := s.SaveTunnel(cfgFor(c.ID, "tcp", target.Addr().String()))
	if err != nil || tun.Error != "" {
		t.Fatal(err, tun.Error)
	}
	occupied, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	desired := tun
	_, port, _ := net.SplitHostPort(occupied.Addr().String())
	fmt.Sscan(port, &desired.PublicPort)
	if _, err := s.SaveTunnel(desired); err == nil {
		t.Fatal("更新占用端口应返回错误")
	}
	cancel()
	<-done
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	s, err = New(Options{Listen: "127.0.0.1:0", DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	opts.Address = s.Addr()
	again, err := client.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done = make(chan struct{})
	go func() { defer close(done); again.Connect(ctx) }()
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		r := s.running[tun.ID]
		return r != nil && r.cfg.PublicPort == tun.PublicPort
	})
	conn := dialTunnel(t, tun.PublicPort)
	defer conn.Close()
	b := make([]byte, len("保留"))
	if _, err = io.ReadFull(conn, b); err != nil || string(b) != "保留" {
		t.Fatal("重启未保留最近生效配置", err, string(b))
	}
	s.mu.RLock()
	current := *s.tunnels[tun.ID]
	s.mu.RUnlock()
	if current.PublicPort != tun.PublicPort || current.Error != "" {
		t.Fatal("重启未保留原有端口配置")
	}
}

func TestRestartRecoversIdentityPortsAndLatestConfig(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{Listen: "127.0.0.1:0", DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	target := tcpTarget(t, "旧")
	next := tcpTarget(t, "新")
	clientDir := t.TempDir()
	opts := client.Options{Address: s.Addr(), Credential: s.Identity.Credential(), DataDir: clientDir}
	c, err := client.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Connect(ctx) }()
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.devices[c.ID].Online })
	tun, err := s.SaveTunnel(cfgFor(c.ID, "tcp", target.Addr().String()))
	if err != nil || tun.Error != "" {
		t.Fatal(err, tun.Error)
	}
	cancel()
	<-done
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.devices[c.ID].Online })
	updated := cfgFor(c.ID, "tcp", next.Addr().String())
	updated.ID = tun.ID
	updated.PublicPort = tun.PublicPort
	if _, err = s.SaveTunnel(updated); err != nil {
		t.Fatal(err)
	}
	credential := s.Identity.Credential()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(Options{Listen: "127.0.0.1:0", DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.TemporaryPassword != "" || s.Identity.Credential() != credential {
		t.Fatal("重启丢失管理员或服务端身份")
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	opts.Address = s.Addr()
	again, err := client.New(opts)
	if err != nil || again.ID != c.ID {
		t.Fatal("设备身份未恢复", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done = make(chan struct{})
	go func() { defer close(done); again.Connect(ctx) }()
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		r := s.running[tun.ID]
		return r != nil && r.cfg.TargetPort == updated.TargetPort
	})
	conn := dialTunnel(t, tun.PublicPort)
	b := make([]byte, len("新"))
	_, err = io.ReadFull(conn, b)
	conn.Close()
	if err != nil || string(b) != "新" {
		t.Fatal("重启后未应用最新配置", err, string(b))
	}
	updated.Enabled = false
	if _, err = s.SaveTunnel(updated); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.devices[c.ID].Online })
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	defer cancel()
	go func() { defer close(done); again.Connect(ctx) }()
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.devices[c.ID].Online })
	s.mu.RLock()
	running := s.running[tun.ID]
	s.mu.RUnlock()
	if running != nil {
		t.Fatal("重连重新启用了已停用隧道")
	}
}

func TestConcurrentPortsAndUDPTimeout(t *testing.T) {
	s := testServer(t)
	id, _ := testClient(t, s)
	target := tcpTarget(t, "并发")
	var wg sync.WaitGroup
	ports := make(chan int, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			tun, err := s.SaveTunnel(cfgFor(id, "both", target.Addr().String()))
			if err != nil {
				errs <- err
				return
			}
			if tun.Error != "" {
				t.Errorf("应用失败：%s", tun.Error)
			}
			ports <- tun.PublicPort
		})
	}
	wg.Wait()
	close(ports)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	seen := map[int]bool{}
	for port := range ports {
		if seen[port] {
			t.Error("重复分配端口", port)
		}
		seen[port] = true
	}
	u, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	go func() {
		b := make([]byte, 1024)
		for {
			n, a, e := u.ReadFromUDP(b)
			if e != nil {
				return
			}
			u.WriteToUDP(b[:n], a)
		}
	}()
	tun, err := s.SaveTunnel(cfgFor(id, "udp", u.LocalAddr().String()))
	if err != nil {
		t.Fatal(err)
	}
	address := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: tun.PublicPort}
	external, err := net.DialUDP("udp", nil, address)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	external.SetDeadline(time.Now().Add(3 * time.Second))
	external.Write([]byte("回包"))
	b := make([]byte, 64)
	if _, err = external.Read(b); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		for _, c := range s.connections {
			if c.record.Protocol == "udp" {
				c.mu.Lock()
				ready := c.record.Up > 0 && c.record.Down > 0
				c.mu.Unlock()
				if ready {
					return true
				}
			}
		}
		return false
	})
	s.mu.Lock()
	for _, c := range s.connections {
		if c.record.Protocol == "udp" {
			c.mu.Lock()
			c.last = time.Now().Add(-61 * time.Second)
			c.mu.Unlock()
		}
	}
	s.mu.Unlock()
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return len(s.connections) == 0 })
	s.mu.RLock()
	cfg := *s.tunnels[tun.ID]
	s.mu.RUnlock()
	cfg.Enabled = false
	if _, err = s.SaveTunnel(cfg); err != nil {
		t.Fatal(err)
	}
}
