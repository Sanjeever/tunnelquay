package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
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
	t.Run("无ALPN探测连接立即断开", func(t *testing.T) {
		raw, err := tls.Dial("tcp", s.Addr(), &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(time.Second))
		_, _ = raw.Write([]byte("GET / HTTP/1.1\r\nHost: example\r\n\r\n"))
		buf := make([]byte, 16)
		if _, err := raw.Read(buf); err == nil {
			t.Fatal("未协商 tunnelquay/1 ALPN 的连接应被服务端立即关闭")
		}
	})
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
	settingsReq, _ := http.NewRequest("GET", base+"/api/settings", nil)
	settingsReq.Header.Set("X-Forwarded-For", "203.0.113.55, 10.0.0.1")
	res, err = h.Do(settingsReq)
	if err != nil || res.StatusCode != 200 {
		t.Fatal(err, res)
	}
	var settings map[string]any
	if err = json.NewDecoder(res.Body).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if settings["client_ip"] != "203.0.113.55" {
		t.Fatalf("反向代理客户端 IP 识别错误：%v", settings["client_ip"])
	}
	newTemp, err := s.ResetPassword()
	if err != nil || len(newTemp) != 20 {
		t.Fatalf("重置管理密码失败：%v %q", err, newTemp)
	}
	res, err = h.Get(base + "/api/tunnels")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("重置管理密码后旧会话应失效：%d", res.StatusCode)
	}
	if res = post("/api/auth/login", `{"password":"本地测试密码123456"}`); res.StatusCode != 401 {
		t.Fatalf("重置后旧管理密码应失效：%d", res.StatusCode)
	}
	if res = post("/api/auth/login", fmt.Sprintf(`{"password":%q}`, newTemp)); res.StatusCode != 200 {
		t.Fatalf("新临时密码登录失败：%d", res.StatusCode)
	}
	res, err = h.Get(base + "/api/tunnels")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("重置密码登录后应强制改密：%d", res.StatusCode)
	}
	postFromIP := func(ip, body string) *http.Response {
		t.Helper()
		req, e := http.NewRequest("POST", base+"/api/auth/login", strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", ip)
		res, e := h.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	for i := 0; i < 10; i++ {
		if r := postFromIP("198.51.100.10", `{"password":"wrong"}`); r.StatusCode != 401 {
			t.Fatalf("第 %d 次错误登录状态码异常：%d", i+1, r.StatusCode)
		}
	}
	if r := postFromIP("198.51.100.10", `{"password":"wrong"}`); r.StatusCode != 429 {
		t.Fatalf("同一来源 IP 超出阈值应返回 429，实际：%d", r.StatusCode)
	}
	if r := postFromIP("198.51.100.11", fmt.Sprintf(`{"password":%q}`, newTemp)); r.StatusCode != 200 {
		t.Fatalf("其他来源 IP 不应被连带限流，实际：%d", r.StatusCode)
	}
}

func TestProbeAllowCIDRsDeleteDeviceAndRotateCredential(t *testing.T) {
	s := testServer(t)
	id, cancel := testClient(t, s)
	target := tcpTarget(t, "P")
	cfg := cfgFor(id, "tcp", target.Addr().String())
	cfg.AllowCIDRs = "198.51.100.0/24"
	tun, err := s.SaveTunnel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	probed, err := s.ProbeTunnel(tun.ID)
	if err != nil || probed.TargetStatus != "reachable" {
		t.Fatalf("目标探测失败：%v %+v", err, probed)
	}
	blockedConn := dialTunnel(t, tun.PublicPort)
	defer blockedConn.Close()
	one := make([]byte, 1)
	if _, err = blockedConn.Read(one); err == nil {
		t.Fatal("非白名单来源 IP 应被拒绝")
	}
	tun.AllowCIDRs = "127.0.0.1/32, 10.0.0.0/8"
	tun, err = s.SaveTunnel(tun)
	if err != nil {
		t.Fatal(err)
	}
	allowedConn := dialTunnel(t, tun.PublicPort)
	defer allowedConn.Close()
	if _, err = io.ReadFull(allowedConn, one); err != nil || string(one) != "P" {
		t.Fatalf("白名单来源 IP 访问失败：%v %q", err, string(one))
	}
	var activeID string
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		for cid := range s.connections {
			activeID = cid
			return true
		}
		return false
	})
	if err = s.CloseConnection(activeID); err != nil {
		t.Fatalf("主动断开活动连接失败：%v", err)
	}
	if _, err = allowedConn.Read(one); err == nil {
		t.Fatal("主动断开后客户端连接应已关闭")
	}
	items, _, err := s.Store.History(store.Filter{TunnelID: tun.ID, State: "history", Page: 1, Limit: 10})
	if err != nil || len(items) == 0 || items[0].Reason != "管理员主动断开" {
		t.Fatalf("主动断开记录未写入历史：%v %+v", err, items)
	}
	oldCred := s.Identity.Credential()
	if err = s.Identity.RotateSecret(s.Options.DataDir); err != nil || s.Identity.Credential() == oldCred {
		t.Fatalf("重置登记凭据失败：%v", err)
	}
	if err = s.DeleteDevice(id); err == nil {
		t.Fatal("在线设备不应允许直接删除")
	}
	cancel()
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.devices[id].Online })
	if err = s.DeleteDevice(id); err != nil {
		t.Fatalf("离线设备删除失败：%v", err)
	}
	s.mu.RLock()
	_, hasDev := s.devices[id]
	_, hasTun := s.tunnels[tun.ID]
	s.mu.RUnlock()
	if hasDev || hasTun {
		t.Fatal("删除设备后未清理设备或关联隧道")
	}
}
