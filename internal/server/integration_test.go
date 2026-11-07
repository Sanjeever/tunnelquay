package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"syscall"
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
	id, cancel := testClient(t, s)
	key := testDeviceKey(t)
	wrong := testDeviceHello(key, "wrong", protocol.Capabilities)
	incompatible := testDeviceHello(key, s.Identity.Secret, protocol.Capabilities)
	incompatible.Protocols = []int{99}
	claimed := testDeviceHello(key, s.Identity.Secret, protocol.Capabilities)
	claimed.ID = id
	for _, tc := range []struct {
		name  string
		hello protocol.Hello
		code  string
	}{
		{"错误秘密", wrong, "unauthorized"},
		{"旧协议拒绝", incompatible, "incompatible"},
		{"不能凭ID认领", claimed, "invalid_identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, err := transport.Dial(context.Background(), s.Addr(), transport.ClientTLS(s.Identity.Fingerprint))
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
	tun.Enabled = false
	updated, err := s.SaveTunnel(tun)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PublicPort != tun.PublicPort {
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

func TestClientReportsAuthenticationError(t *testing.T) {
	s := testServer(t)
	bad := *s.Identity
	bad.Secret = strings.Repeat("0", 48)
	c, err := client.New(client.Options{Address: s.Addr(), Credential: bad.Credential(), DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = c.Connect(ctx)
	if err == nil || !strings.Contains(err.Error(), "接入凭据错误") {
		t.Fatalf("认证错误未提供原因：%v", err)
	}
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
func testClient(t *testing.T, s *Server) (string, context.CancelFunc) {
	t.Helper()
	c, err := client.New(client.Options{Address: s.Addr(), Credential: s.Identity.Credential(), DataDir: t.TempDir()})
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
func cfgFor(id, network, target string) store.Tunnel {
	h, p, _ := net.SplitHostPort(target)
	var port int
	fmt.Sscan(p, &port)
	return store.Tunnel{Name: "本地转发", DeviceID: id, Protocol: network, Host: h, TargetPort: port, Enabled: true}
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
	s := testServer(t)
	id, _ := testClient(t, s)
	target := tcpTarget(t, "")
	tunnel, err := s.SaveTunnel(cfgFor(id, "tcp", target.Addr().String()))
	if err != nil || tunnel.Error != "" {
		t.Fatalf("隧道：%v %s", err, tunnel.Error)
	}
	c := dialTunnel(t, tunnel.PublicPort)
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
		conn, e := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", ut.PublicPort))
		if e != nil {
			t.Fatal(e)
		}
		defer conn.Close()
		for _, size := range []int{8192, 65507} {
			t.Run(fmt.Sprintf("source-%d/%d", i, size), func(t *testing.T) {
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				data := bytes.Repeat([]byte{byte(i + 1)}, size)
				if _, e = conn.Write(data); e != nil {
					if size == 65507 && errors.Is(e, syscall.EMSGSIZE) {
						t.Skipf("本机系统拒绝发送最大 UDP 数据报：%v", e)
					}
					t.Fatal(e)
				}
				b := make([]byte, 65535)
				n, e := conn.Read(b)
				if e != nil || !bytes.Equal(data, b[:n]) {
					t.Fatalf("UDP 来源 %d，大数据报失败：%v，%d", i, e, n)
				}
			})
		}
	}
	ut.Enabled = false
	if _, err = s.SaveTunnel(ut); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return len(s.connections) == 0 })
	if err = s.DeleteTunnel(tunnel.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.flushHistory(); err != nil {
		t.Fatal(err)
	}
	items, total, err := s.Store.History(store.Filter{Page: 1, Limit: 100})
	if err != nil || total != 4 {
		t.Fatalf("历史 %d：%v %#v", total, err, items)
	}
}
func TestConfigSwitchRollbackDisable(t *testing.T) {
	s := testServer(t)
	id, _ := testClient(t, s)
	a := tcpTarget(t, "A")
	b := tcpTarget(t, "B")
	tun, err := s.SaveTunnel(cfgFor(id, "tcp", a.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	old := dialTunnel(t, tun.PublicPort)
	defer old.Close()
	one := make([]byte, 1)
	if _, err = io.ReadFull(old, one); err != nil || string(one) != "A" {
		t.Fatal("旧目标失败", err)
	}
	update := cfgFor(id, "tcp", b.Addr().String())
	update.ID = tun.ID
	update.PublicPort = tun.PublicPort
	updated, err := s.SaveTunnel(update)
	if err != nil || updated.Error != "" {
		t.Fatal(err, updated.Error)
	}
	newConn := dialTunnel(t, updated.PublicPort)
	defer newConn.Close()
	if _, err = io.ReadFull(newConn, one); err != nil || string(one) != "B" {
		t.Fatal("新目标失败", err)
	}
	update.Protocol = "both"
	combined, err := s.SaveTunnel(update)
	if err != nil || combined.Error != "" || combined.Protocol != "both" {
		t.Fatal("同端口切换组合协议失败", err, combined.Error)
	}
	update.Protocol = "tcp"
	old.Write([]byte("x"))
	if _, err = io.ReadFull(old, one); err != nil || string(one) != "x" {
		t.Fatal("旧连接未保留", err)
	}
	occupied, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	_, p, _ := net.SplitHostPort(occupied.Addr().String())
	fmt.Sscan(p, &update.PublicPort)
	if _, err := s.SaveTunnel(update); err == nil {
		t.Fatal("失败更新应返回错误")
	}
	s.mu.RLock()
	retained := *s.tunnels[tun.ID]
	s.mu.RUnlock()
	if retained.PublicPort != tun.PublicPort {
		t.Fatalf("失败更新没有保留旧配置：%#v", retained)
	}
	retained.Enabled = false
	if _, err = s.SaveTunnel(retained); err != nil {
		t.Fatal(err)
	}
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
