package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/client"
	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/store"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待状态超时")
}
func TestAuthenticationRejectsAndOfflineRecovery(t *testing.T) {
	s := testServer(t)
	id, cancel := testClient(t, s, "tcp")
	for _, tc := range []struct {
		name  string
		hello protocol.Hello
		code  string
	}{
		{"错误秘密", protocol.Hello{ID: transport.RandomID(), Secret: "wrong", Protocols: []int{1}, Capabilities: protocol.Capabilities}, "unauthorized"},
		{"协议不兼容", protocol.Hello{ID: transport.RandomID(), Secret: s.Identity.Secret, Protocols: []int{9}, Capabilities: protocol.Capabilities}, "incompatible"},
		{"重复身份", protocol.Hello{ID: id, Secret: s.Identity.Secret, Protocols: []int{1}, Capabilities: protocol.Capabilities}, "duplicate_identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, err := transport.Dial(context.Background(), s.Addr(), "tcp", transport.ClientTLS(s.Identity.Fingerprint))
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			stream, err := session.Open(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			stream.SetDeadline(time.Now().Add(time.Second))
			protocol.Write(stream, protocol.Message{Type: "hello", Hello: &tc.hello})
			var m protocol.Message
			if err = protocol.Read(stream, &m); err != nil || m.Code != tc.code {
				t.Fatal(m, err)
			}
		})
	}
	target := tcpTarget(t, "R")
	tun, err := s.SaveTunnel(cfgFor(id, "both", target.Addr().String()))
	if err != nil || tun.Error != "" {
		t.Fatal(err, tun.Error)
	}
	cancel()
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.devices[id].Online })
	tun.Desired.Enabled = false
	updated, err := s.SaveTunnel(tun.Desired)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Desired.PublicPort != tun.Desired.PublicPort {
		t.Fatal("离线丢失端口")
	}
	occupied, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	cfg := cfgFor(id, "both", target.Addr().String())
	cfg.PublicPort = occupied.LocalAddr().(*net.UDPAddr).Port
	prepared, err := bind(cfg)
	if err == nil {
		prepared.close()
		t.Fatal("组合模式应整体失败")
	}
	probe, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", cfg.PublicPort))
	if err != nil {
		t.Fatal("失败后 TCP 资源未释放", err)
	}
	probe.Close()
}
func testServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{Listen: "127.0.0.1:0", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}
func testClient(t *testing.T, s *Server, network string) (string, context.CancelFunc) {
	t.Helper()
	c, err := client.New(client.Options{Address: s.Addr(), Credential: s.Identity.Credential(), Transport: network, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Connect(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("客户端未退出")
		}
	})
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.devices[c.ID].Online && s.devices[c.ID].RTT != nil
	})
	return c.ID, cancel
}
func tcpTarget(t *testing.T, prefix string) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				if prefix == "" {
					b, _ := io.ReadAll(c)
					c.Write(append([]byte("回复:"), b...))
					return
				}
				c.Write([]byte(prefix))
				io.Copy(c, c)
			}()
		}
	}()
	return l
}
func cfgFor(id, network, target string) protocol.Config {
	h, p, _ := net.SplitHostPort(target)
	var port int
	fmt.Sscan(p, &port)
	return protocol.Config{Name: "本地转发", DeviceID: id, Protocol: network, Host: h, TargetPort: port, Enabled: true}
}
func dialTunnel(t *testing.T, port int) *net.TCPConn {
	t.Helper()
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	return c.(*net.TCPConn)
}
func TestTransportsTCPUDPAndHistory(t *testing.T) {
	for _, network := range []string{"tcp", "quic"} {
		t.Run(network, func(t *testing.T) {
			s := testServer(t)
			id, _ := testClient(t, s, network)
			target := tcpTarget(t, "")
			tunnel, err := s.SaveTunnel(cfgFor(id, "tcp", target.Addr().String()))
			if err != nil || tunnel.Error != "" {
				t.Fatalf("隧道：%v %s", err, tunnel.Error)
			}
			c := dialTunnel(t, tunnel.Desired.PublicPort)
			payload := bytes.Repeat([]byte("双向传输"), 10000)
			if _, err = c.Write(payload); err != nil {
				t.Fatal(err)
			}
			c.CloseWrite()
			got, err := io.ReadAll(c)
			c.Close()
			if err != nil || !bytes.Equal(got, append([]byte("回复:"), payload...)) {
				s.historyMu.Lock()
				t.Logf("已结束连接：%+v", s.finished)
				s.historyMu.Unlock()
				t.Fatalf("半关闭回包错误：%v 长度 %d", err, len(got))
			}
			u, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer u.Close()
			go func() {
				b := make([]byte, 65535)
				for {
					n, a, e := u.ReadFromUDP(b)
					if e != nil {
						return
					}
					u.WriteToUDP(b[:n], a)
				}
			}()
			ut, err := s.SaveTunnel(cfgFor(id, "udp", u.LocalAddr().String()))
			if err != nil || ut.Error != "" {
				t.Fatalf("UDP 隧道：%v %s", err, ut.Error)
			}
			for i := 0; i < 3; i++ {
				conn, e := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", ut.Desired.PublicPort))
				if e != nil {
					t.Fatal(e)
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				data := bytes.Repeat([]byte{byte(i + 1)}, 32000)
				if _, e = conn.Write(data); e != nil {
					t.Fatal(e)
				}
				b := make([]byte, 65535)
				n, e := conn.Read(b)
				if e != nil || !bytes.Equal(data, b[:n]) {
					t.Fatalf("UDP 来源 %d，大数据报失败：%v，%d", i, e, n)
				}
			}
			ut.Desired.Enabled = false
			if _, err = s.SaveTunnel(ut.Desired); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return len(s.connections) == 0 })
			if err = s.DeleteTunnel(tunnel.Desired.ID); err != nil {
				t.Fatal(err)
			}
			if err = s.flushHistory(); err != nil {
				t.Fatal(err)
			}
			items, total, err := s.Store.History(store.Filter{Page: 1, Limit: 100})
			if err != nil || total != 4 {
				t.Fatalf("历史 %d：%v %#v", total, err, items)
			}
		})
	}
}
func TestConfigSwitchRollbackDisable(t *testing.T) {
	s := testServer(t)
	id, _ := testClient(t, s, "tcp")
	a := tcpTarget(t, "A")
	b := tcpTarget(t, "B")
	tun, err := s.SaveTunnel(cfgFor(id, "tcp", a.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	old := dialTunnel(t, tun.Desired.PublicPort)
	defer old.Close()
	one := make([]byte, 1)
	if _, err = io.ReadFull(old, one); err != nil || string(one) != "A" {
		t.Fatal("旧目标失败", err)
	}
	update := cfgFor(id, "tcp", b.Addr().String())
	update.ID = tun.Desired.ID
	update.PublicPort = tun.Desired.PublicPort
	updated, err := s.SaveTunnel(update)
	if err != nil || updated.Error != "" {
		t.Fatal(err, updated.Error)
	}
	newConn := dialTunnel(t, updated.Desired.PublicPort)
	defer newConn.Close()
	if _, err = io.ReadFull(newConn, one); err != nil || string(one) != "B" {
		t.Fatal("新目标失败", err)
	}
	old.Write([]byte("x"))
	if _, err = io.ReadFull(old, one); err != nil || string(one) != "x" {
		t.Fatal("旧连接未保留", err)
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	_, p, _ := net.SplitHostPort(occupied.Addr().String())
	fmt.Sscan(p, &update.PublicPort)
	failed, err := s.SaveTunnel(update)
	if err != nil || failed.Error == "" || failed.Applied.PublicPort != tun.Desired.PublicPort {
		t.Fatalf("失败更新没有保留旧配置：%v %#v", err, failed)
	}
	failed.Desired.Enabled = false
	s.SaveTunnel(failed.Desired)
	if _, err = old.Read(one); err == nil {
		t.Fatal("停用后旧连接未关闭")
	}
	if _, err = newConn.Read(one); err == nil {
		t.Fatal("停用后新连接未关闭")
	}
}
func TestLoginPasswordAndOrigin(t *testing.T) {
	s := testServer(t)
	jar, _ := cookiejar.New(nil)
	h := &http.Client{Jar: jar}
	base := "http://" + s.Addr()
	post := func(path, body string) *http.Response {
		t.Helper()
		res, e := h.Post(base+path, "application/json", strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	if res := post("/api/auth/login", `{"password":"wrong"}`); res.StatusCode != 401 {
		t.Fatal(res.StatusCode)
	}
	res := post("/api/auth/login", fmt.Sprintf(`{"password":%q}`, s.TemporaryPassword))
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	res, err := h.Get(base + "/api/tunnels")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("首次登录须强制改密")
	}
	res = post("/api/auth/password", fmt.Sprintf(`{"current":%q,"password":"本地测试密码123456"}`, s.TemporaryPassword))
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatal(res.StatusCode, string(b))
	}
	res, err = h.Get(base + "/api/tunnels")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	req, _ := http.NewRequest("PUT", base+"/api/settings", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://evil.example")
	res, err = h.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("应拒绝跨站写入")
	}
}
