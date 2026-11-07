package client

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

func TestServerWithoutUDPStreamCannotApplyUnnegotiatedMode(t *testing.T) {
	identity, err := transport.LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp4", "127.0.0.1:0", identity.TLS())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c, err := New(Options{Address: listener.Addr().String(), Credential: identity.Credential(), Transport: "tcp", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Connect(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("客户端未退出")
		}
	}()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	session, err := transport.NewTCP(conn, true)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	stream, err := session.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream.SetDeadline(time.Now().Add(3 * time.Second))
	codec := &protocol.Codec{RW: stream}
	hello, err := codec.Receive()
	if err != nil || !protocol.Has(hello.Hello.Capabilities, protocol.CapabilityUDPStream) {
		t.Fatal(err, hello)
	}
	challenge := "udp-mode-test-auth-challenge"
	if err = codec.Send(protocol.Message{Type: "identity_challenge", Challenge: challenge, Protocol: protocol.Version, Capabilities: []string{"config-v1", "tcp", "udp"}}); err != nil {
		t.Fatal(err)
	}
	proof, err := codec.Receive()
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := base64.RawStdEncoding.DecodeString(hello.Hello.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := protocol.IdentityPayload(*hello.Hello, challenge, identity.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := base64.RawStdEncoding.DecodeString(proof.Signature)
	if err != nil || !ed25519.Verify(publicKey, payload, signature) {
		t.Fatal("客户端密钥证明错误", err)
	}
	if err = codec.Send(protocol.Message{Type: "welcome", Protocol: protocol.Version, Capabilities: []string{"config-v1", "tcp", "udp"}}); err != nil {
		t.Fatal(err)
	}
	previous := protocol.Config{ID: "old-config", Protocol: "udp", Revision: 1}
	if err = codec.Send(protocol.Message{Type: "prepare", ID: "old", Config: &previous, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	reply, err := codec.Receive()
	if err != nil || reply.Error != "" {
		t.Fatal("旧配置应继续支持", reply, err)
	}
	next := previous
	next.UDPMode, next.Revision = protocol.UDPModeStream, 2
	for _, message := range []protocol.Message{
		{Type: "prepare", ID: "prepare-stream", Config: &next, Revision: 2},
		{Type: "snapshot", ID: "snapshot-stream", Configs: []protocol.Config{{ID: "extra", Protocol: "udp"}, next}},
	} {
		if err = codec.Send(message); err != nil {
			t.Fatal(err)
		}
		reply, err = codec.Receive()
		if err != nil || reply.Code != "unsupported_udp_mode" || !strings.Contains(reply.Error, protocol.CapabilityUDPStream) {
			t.Fatal("未拒绝未协商配置", reply, err)
		}
		c.mu.Lock()
		retained := c.configs[previous.ID]
		count := len(c.configs)
		c.mu.Unlock()
		if count != 1 || retained.Revision != 1 || retained.UDPMode != "" {
			t.Fatal("失败配置覆盖了旧配置", retained, count)
		}
	}
	forward, err := session.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer forward.Close()
	forward.SetDeadline(time.Now().Add(3 * time.Second))
	next.Revision = 1
	if err = protocol.Write(forward, protocol.Message{Type: "forward", Network: "udp", Config: &next, Target: net.JoinHostPort("127.0.0.1", "1")}); err != nil {
		t.Fatal(err)
	}
	if err = protocol.Read(forward, &reply); err != nil || !strings.Contains(reply.Error, protocol.CapabilityUDPStream) {
		t.Fatal("未拒绝未协商数据通道", reply, err)
	}
}
