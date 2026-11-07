package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

func TestDeviceKeyPersistsAndLegacyIDCannotSelectIdentity(t *testing.T) {
	identity, err := transport.LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	legacy := strings.Repeat("a", 48)
	if err = os.WriteFile(filepath.Join(dir, "device-id"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Address: "127.0.0.1:1", Credential: identity.Credential(), Transport: "tcp", DataDir: dir}
	first, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ID) != 64 || first.ID == legacy {
		t.Fatal("仍然使用可修改的旧 ID", first.ID)
	}
	if err = os.WriteFile(filepath.Join(dir, "device-id"), []byte(strings.Repeat("b", 48)), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := New(opts)
	if err != nil || again.ID != first.ID || !bytes.Equal(again.identityKey, first.identityKey) {
		t.Fatal("设备私钥身份未持久化", err)
	}
	info, err := os.Stat(filepath.Join(dir, "device-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("私钥文件权限过宽", info.Mode())
	}
	path := filepath.Join(dir, "device-key.pem")
	corrupt := []byte("损坏的设备密钥")
	if err = os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = New(opts); err == nil {
		t.Fatal("损坏密钥被静默替换")
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, corrupt) {
		t.Fatal("错误后覆盖了原密钥", err)
	}
}

func TestServerCannotSkipDeviceKeyAuthentication(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Connect(ctx) }()
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
	codec := &protocol.Codec{RW: stream}
	if _, err = codec.Receive(); err != nil {
		t.Fatal(err)
	}
	if err = codec.Send(protocol.Message{Type: "welcome", Protocol: protocol.Version, Capabilities: protocol.Capabilities}); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err == nil || !strings.Contains(err.Error(), "服务端未执行设备密钥认证") {
			t.Fatal("允许跳过身份签名", err)
		}
	case <-ctx.Done():
		t.Fatal("客户端没有拒绝跳过认证的服务端")
	}
}
