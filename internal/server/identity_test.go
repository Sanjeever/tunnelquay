package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/client"
	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

func testDeviceKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func testDeviceHello(key ed25519.PrivateKey, secret string, capabilities []string) protocol.Hello {
	public := key.Public().(ed25519.PublicKey)
	return protocol.Hello{ID: protocol.DeviceID(public), PublicKey: base64.RawStdEncoding.EncodeToString(public), Secret: secret, Hostname: "设备密钥回归", Protocols: []int{protocol.Version}, Capabilities: capabilities}
}

func identityReply(t *testing.T, codec *protocol.Codec) protocol.Message {
	t.Helper()
	m, err := codec.Receive()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func testIdentityProof(t *testing.T, codec *protocol.Codec, hello protocol.Hello, key ed25519.PrivateKey, fingerprint string) protocol.Message {
	t.Helper()
	if err := codec.Send(protocol.Message{Type: "hello", Hello: &hello}); err != nil {
		t.Fatal(err)
	}
	challenge, err := codec.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Type == "error" {
		return challenge
	}
	if challenge.Type != "identity_challenge" || challenge.Challenge == "" || challenge.Protocol != protocol.Version {
		t.Fatal("没有执行密钥认证挑战", challenge)
	}
	payload, err := protocol.IdentityPayload(hello, challenge.Challenge, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if err = codec.Send(protocol.Message{Type: "identity_proof", Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(key, payload))}); err != nil {
		t.Fatal(err)
	}
	welcome, err := codec.Receive()
	if err != nil {
		t.Fatal(err)
	}
	return welcome
}

func deviceAdmin(t *testing.T, s *Server) func(string, string, any) *httptest.ResponseRecorder {
	t.Helper()
	call := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(data)
		}
		req := httptest.NewRequest(method, path, reader)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res := httptest.NewRecorder()
		s.Handler().ServeHTTP(res, req)
		return res
	}
	login := call("POST", "/api/auth/login", map[string]string{"password": s.TemporaryPassword}, nil)
	if login.Code != 200 {
		t.Fatal(login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	changed := call("POST", "/api/auth/password", map[string]string{"current": s.TemporaryPassword, "password": "identity-test-password-123"}, cookie)
	if changed.Code != 200 {
		t.Fatal(changed.Body.String())
	}
	return func(method, path string, body any) *httptest.ResponseRecorder {
		return call(method, path, body, cookie)
	}
}

func TestDeviceKeyRejectsIDClaimsAndReplay(t *testing.T) {
	s := testServer(t)
	key := testDeviceKey(t)
	attacker := testDeviceKey(t)
	hello := testDeviceHello(key, s.Identity.Secret, protocol.Capabilities)
	var savedSignature, previousChallenge string
	attempt := func(h protocol.Hello, signer ed25519.PrivateKey, replay bool) protocol.Message {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		session, err := transport.Dial(ctx, s.Addr(), transport.ClientTLS(s.Identity.Fingerprint))
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		stream, err := session.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		stream.SetDeadline(time.Now().Add(3 * time.Second))
		codec := &protocol.Codec{RW: stream}
		if err = codec.Send(protocol.Message{Type: "hello", Hello: &h}); err != nil {
			t.Fatal(err)
		}
		challenge := identityReply(t, codec)
		if challenge.Type == "error" {
			return challenge
		}
		if challenge.Type != "identity_challenge" {
			t.Fatal("未要求密钥证明", challenge)
		}
		var signature string
		if replay {
			if challenge.Challenge == previousChallenge {
				t.Fatal("复用了身份挑战")
			}
			signature = savedSignature
		} else {
			payload, err := protocol.IdentityPayload(h, challenge.Challenge, s.Identity.Fingerprint)
			if err != nil {
				t.Fatal(err)
			}
			signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(signer, payload))
		}
		if err = codec.Send(protocol.Message{Type: "identity_proof", Signature: signature}); err != nil {
			t.Fatal(err)
		}
		result := identityReply(t, codec)
		if result.Type == "welcome" {
			savedSignature, previousChallenge = signature, challenge.Challenge
		}
		return result
	}
	if result := attempt(hello, attacker, false); result.Code != "invalid_identity" {
		t.Fatal("登记未验证密钥持有者", result)
	}
	s.mu.RLock()
	registered := len(s.devices)
	s.mu.RUnlock()
	if registered != 0 {
		t.Fatal("无效签名创建了设备登记")
	}
	if result := attempt(hello, key, false); result.Type != "welcome" {
		t.Fatal(result)
	}
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.devices[hello.ID].Online })
	s.mu.RLock()
	before := s.devices[hello.ID]
	s.mu.RUnlock()
	claimed := testDeviceHello(attacker, s.Identity.Secret, protocol.Capabilities)
	claimed.ID = hello.ID
	if result := attempt(claimed, attacker, false); result.Code != "invalid_identity" {
		t.Fatal("共享凭据和 ID 可以冒领", result)
	}
	if result := attempt(hello, attacker, false); result.Code != "invalid_identity" {
		t.Fatal("仅公钥可以冒领", result)
	}
	if result := attempt(hello, key, true); result.Code != "invalid_identity" {
		t.Fatal("重放签名被接受", result)
	}
	s.mu.RLock()
	after := s.devices[hello.ID]
	s.mu.RUnlock()
	if after.PublicKey != before.PublicKey || after.LastSeen != before.LastSeen || after.Online {
		t.Fatal("失败认证修改了设备登记", after)
	}
	hello.Secret = "旧的共享凭据"
	if result := attempt(hello, key, false); result.Type != "welcome" {
		t.Fatal("已登记密钥仍依赖共享凭据", result)
	}
}

func TestDeviceRevocationStopsForwardingAndSurvivesRestart(t *testing.T) {
	s, err := New(Options{Listen: "127.0.0.1:0", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	opts := client.Options{Address: s.Addr(), Credential: s.Identity.Credential(), DataDir: t.TempDir()}
	c, err := client.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Connect(ctx) }()
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.devices[c.ID].Online })
	duplicate, err := client.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = duplicate.Connect(ctx); err == nil || !strings.Contains(err.Error(), "duplicate_identity") {
		t.Fatal("同密钥重复在线未拒绝", err)
	}
	target := tcpTarget(t, "A")
	cfg := cfgFor(c.ID, "both", target.Addr().String())
	tun, err := s.SaveTunnel(cfg)
	if err != nil || tun.Error != "" {
		t.Fatal(err, tun.Error)
	}
	conn := dialTunnel(t, tun.Desired.PublicPort)
	one := make([]byte, 1)
	if _, err = io.ReadFull(conn, one); err != nil || string(one) != "A" {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()
	unauthorized := httptest.NewRecorder()
	s.Handler().ServeHTTP(unauthorized, httptest.NewRequest("POST", "/api/devices/"+c.ID+"/revoke", nil))
	if unauthorized.Code != 401 {
		t.Fatal("未登录可以撤销身份", unauthorized.Code)
	}
	admin := deviceAdmin(t, s)
	result := admin("POST", "/api/devices/"+c.ID+"/revoke", nil)
	if result.Code != 200 {
		t.Fatal(result.Body.String())
	}
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return len(s.connections) == 0 && s.peers[c.ID] == nil
	})
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("撤销没有断开客户端")
	}
	if _, err = s.SaveTunnel(tun.Desired); err == nil {
		t.Fatal("已撤销设备可启用隧道")
	}
	dir := s.Options.DataDir
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
	blocked, err := client.New(opts)
	if err != nil || blocked.ID != c.ID {
		t.Fatal("设备密钥未持久化", err)
	}
	runCtx, runCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer runCancel()
	err = blocked.Run(runCtx)
	if err == nil || !strings.Contains(err.Error(), "identity_revoked") {
		t.Fatal("撤销后仍接入或继续重试", err)
	}
}

func TestRevocationDuringIdentityProofCannotRaceRegistration(t *testing.T) {
	s := testServer(t)
	key := testDeviceKey(t)
	hello := testDeviceHello(key, s.Identity.Secret, protocol.Capabilities)
	{
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		session, err := transport.Dial(ctx, s.Addr(), transport.ClientTLS(s.Identity.Fingerprint))
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		stream, err := session.Open(ctx)
		if err != nil {
			session.Close()
			cancel()
			t.Fatal(err)
		}
		stream.SetDeadline(time.Now().Add(3 * time.Second))
		if welcome := testIdentityProof(t, &protocol.Codec{RW: stream}, hello, key, s.Identity.Fingerprint); welcome.Type != "welcome" {
			session.Close()
			cancel()
			t.Fatal(welcome)
		}
		session.Close()
		cancel()
	}
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.devices[hello.ID].Online })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := transport.Dial(ctx, s.Addr(), transport.ClientTLS(s.Identity.Fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	stream, err := session.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream.SetDeadline(time.Now().Add(3 * time.Second))
	codec := &protocol.Codec{RW: stream}
	if err = codec.Send(protocol.Message{Type: "hello", Hello: &hello}); err != nil {
		t.Fatal(err)
	}
	challenge := identityReply(t, codec)
	if challenge.Type != "identity_challenge" {
		t.Fatal(challenge)
	}
	admin := deviceAdmin(t, s)
	if result := admin("POST", "/api/devices/"+hello.ID+"/revoke", nil); result.Code != 200 {
		t.Fatal(result.Body.String())
	}
	payload, err := protocol.IdentityPayload(hello, challenge.Challenge, s.Identity.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if err = codec.Send(protocol.Message{Type: "identity_proof", Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(key, payload))}); err != nil {
		t.Fatal(err)
	}
	if result := identityReply(t, codec); result.Code != "identity_revoked" {
		t.Fatal("撤销期间完成认证可重新上线", result)
	}
	s.mu.RLock()
	online := s.devices[hello.ID].Online
	s.mu.RUnlock()
	if online {
		t.Fatal("撤销身份被并发认证恢复")
	}
}
