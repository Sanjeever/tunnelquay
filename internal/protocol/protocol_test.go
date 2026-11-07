package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"slices"
	"testing"
)

func TestNegotiationAndOptionalFields(t *testing.T) {
	h := Hello{Protocols: []int{Version}, Capabilities: Capabilities}
	if err := Negotiate(h); err != nil {
		t.Fatal(err)
	}
	h.Protocols = []int{999}
	if Negotiate(h) == nil {
		t.Fatal("应拒绝不兼容协议")
	}
	h.Protocols = []int{Version}
	h.Capabilities = []string{"tcp", "udp"}
	if Negotiate(h) == nil {
		t.Fatal("应拒绝缺少必要能力")
	}
	var b bytes.Buffer
	payload := []byte(`{"type":"ping","future_optional":true}`)
	binary.Write(&b, binary.BigEndian, uint32(len(payload)))
	b.Write(payload)
	var m Message
	if err := Read(&b, &m); err != nil || m.Type != "ping" {
		t.Fatalf("可选字段兼容失败：%v", err)
	}
	b.Reset()
	binary.Write(&b, binary.BigEndian, uint32(MaxFrame+1))
	if Read(&b, &m) == nil {
		t.Fatal("应拒绝过大消息")
	}
}

func TestUDPModeCapabilityNegotiation(t *testing.T) {
	legacy := []string{"config-v1", "tcp", "udp", "upgrade-v1", "future"}
	negotiated := NegotiateCapabilities(legacy)
	if Has(negotiated, CapabilityUDPStream) || Has(negotiated, "future") || Negotiate(Hello{Protocols: []int{Version}, Capabilities: negotiated}) != nil {
		t.Fatal("旧能力协商失败", negotiated)
	}
	if !slices.Equal(NegotiateCapabilities(Capabilities), Capabilities) {
		t.Fatal("新能力未协商")
	}
	var old Config
	if err := json.Unmarshal([]byte(`{"protocol":"udp"}`), &old); err != nil || old.ValidateUDPMode(legacy) != nil {
		t.Fatal("旧配置默认承载不兼容", err)
	}
	for _, mode := range []string{"", UDPModeDatagram} {
		if err := (Config{Protocol: "udp", UDPMode: mode}).ValidateUDPMode(negotiated); err != nil {
			t.Fatal(err)
		}
	}
	stream := Config{Protocol: "both", UDPMode: UDPModeStream}
	if stream.ValidateUDPMode(negotiated) == nil || stream.ValidateUDPMode(Capabilities) != nil {
		t.Fatal("可靠承载未按能力校验")
	}
	if (Config{UDPMode: "unknown"}).ValidateUDPMode(Capabilities) == nil {
		t.Fatal("接受了未知承载")
	}
}
