package server

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestUDPSessionsRetainTargetAndOldPublicPort(t *testing.T) {
	s := testServer(t)
	id, _ := testClient(t, s)
	target := func(prefix string) *net.UDPConn {
		u, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { u.Close() })
		go func() {
			b := make([]byte, 65535)
			for {
				n, a, e := u.ReadFromUDP(b)
				if e != nil {
					return
				}
				u.WriteToUDP(append([]byte(prefix), b[:n]...), a)
			}
		}()
		return u
	}
	a, b := target("A"), target("B")
	tun, err := s.SaveTunnel(cfgFor(id, "udp", a.LocalAddr().String()))
	if err != nil || tun.Error != "" {
		t.Fatal(err, tun.Error)
	}
	external := func(port int) net.Conn {
		c, e := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", port))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	check := func(c net.Conn, want string) {
		t.Helper()
		c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, e := c.Write([]byte("数据")); e != nil {
			t.Fatal(e)
		}
		buf := make([]byte, 64)
		n, e := c.Read(buf)
		if e != nil || string(buf[:n]) != want+"数据" {
			t.Fatal("UDP 会话目标不符", e, string(buf[:n]))
		}
	}
	old := external(tun.Desired.PublicPort)
	check(old, "A")
	cfg := cfgFor(id, "udp", b.LocalAddr().String())
	cfg.ID = tun.Desired.ID
	cfg.PublicPort = tun.Desired.PublicPort
	updated, err := s.SaveTunnel(cfg)
	if err != nil || updated.Error != "" {
		t.Fatal(err, updated.Error)
	}
	check(old, "A")
	current := external(tun.Desired.PublicPort)
	check(current, "B")
	cfg.PublicPort = 0
	moved, err := s.SaveTunnel(cfg)
	if err != nil || moved.Error != "" || moved.Desired.PublicPort == tun.Desired.PublicPort {
		t.Fatal("更新公网端口失败", err, moved.Error)
	}
	check(old, "A")
	check(current, "B")
	check(external(moved.Desired.PublicPort), "B")
	moved.Desired.Enabled = false
	if _, err = s.SaveTunnel(moved.Desired); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return len(s.connections) == 0 && len(s.generations) == 0
	})
	if s.udpQueuedBytes.Load() != 0 {
		t.Fatal("停用后 UDP 队列字节没有归零")
	}
}
