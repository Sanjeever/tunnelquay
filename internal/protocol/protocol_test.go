package protocol

import (
	"bytes"
	"encoding/binary"
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
	h.Capabilities = []string{"tcp"}
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
