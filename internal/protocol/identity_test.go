package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func TestIdentitySignatureBindsChallengeServerAndHello(t *testing.T) {
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hello := Hello{ID: DeviceID(public), PublicKey: base64.RawStdEncoding.EncodeToString(public), Hostname: "原设备", Protocols: []int{Version}, Capabilities: Capabilities}
	payload, err := IdentityPayload(hello, "first-challenge", "trusted-server")
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(key, payload)
	changed := hello
	changed.Hostname = "伪造设备"
	for _, item := range []struct {
		hello         Hello
		nonce, server string
	}{{hello, "other-challenge", "trusted-server"}, {hello, "first-challenge", "other-server"}, {changed, "first-challenge", "trusted-server"}} {
		modified, err := IdentityPayload(item.hello, item.nonce, item.server)
		if err != nil {
			t.Fatal(err)
		}
		if ed25519.Verify(public, modified, signature) {
			t.Fatal("签名未绑定挑战、服务端或握手内容")
		}
	}
}
