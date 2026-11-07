package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const Version = 1
const MaxFrame = 1 << 20
const MaxPacket = 65507
const HeartbeatInterval = 10 * time.Second
const HeartbeatTimeout = 60 * time.Second
const ControlIdleTimeout = 90 * time.Second
const ControlRequestTimeout = 10 * time.Second

var Capabilities = []string{"tcp", "udp", "upgrade-v1"}

func NegotiateCapabilities(remote []string) []string {
	var negotiated []string
	for _, capability := range Capabilities {
		if Has(remote, capability) {
			negotiated = append(negotiated, capability)
		}
	}
	return negotiated
}

type Hello struct {
	Secret       string   `json:"secret"`
	ID           string   `json:"id"`
	PublicKey    string   `json:"public_key,omitempty"`
	Hostname     string   `json:"hostname"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Version      string   `json:"version"`
	Protocols    []int    `json:"protocols"`
	Capabilities []string `json:"capabilities"`
}

type Message struct {
	Type         string          `json:"type"`
	ID           string          `json:"id,omitempty"`
	Code         string          `json:"code,omitempty"`
	Error        string          `json:"error,omitempty"`
	Challenge    string          `json:"challenge,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	Protocol     int             `json:"protocol,omitempty"`
	Capabilities []string        `json:"capabilities,omitempty"`
	Hello        *Hello          `json:"hello,omitempty"`
	Network      string          `json:"network,omitempty"`
	Target       string          `json:"target,omitempty"`
	Size         int64           `json:"size,omitempty"`
	Task         json.RawMessage `json:"task,omitempty"`
}

type Stream interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
}

type Codec struct {
	RW io.ReadWriter
	mu sync.Mutex
}

func Write(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > MaxFrame {
		return errors.New("控制消息超过大小限制")
	}
	buf := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(buf, uint32(len(b)))
	copy(buf[4:], b)
	for len(buf) > 0 {
		n, e := w.Write(buf)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		buf = buf[n:]
	}
	return nil
}

func Read(r io.Reader, v any) error {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(h[:])
	if n == 0 || n > MaxFrame {
		return errors.New("控制消息长度无效")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func WritePacket(w io.Writer, b []byte) error {
	if len(b) > MaxPacket {
		return errors.New("UDP 数据报过大")
	}
	var h [2]byte
	binary.BigEndian.PutUint16(h[:], uint16(len(b)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func ReadPacket(r io.Reader) ([]byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	b := make([]byte, int(binary.BigEndian.Uint16(h[:])))
	_, err := io.ReadFull(r, b)
	return b, err
}

func (c *Codec) Send(v Message) error      { c.mu.Lock(); defer c.mu.Unlock(); return Write(c.RW, v) }
func (c *Codec) Receive() (Message, error) { var m Message; err := Read(c.RW, &m); return m, err }

func Negotiate(h Hello) error {
	found := false
	for _, v := range h.Protocols {
		if v == Version {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("通信协议不兼容，服务端要求协议 %d，请升级客户端", Version)
	}
	for _, required := range []string{"tcp", "udp"} {
		if !Has(h.Capabilities, required) {
			return fmt.Errorf("客户端缺少必要能力 %s", required)
		}
	}
	return nil
}

func Has(caps []string, value string) bool {
	for _, s := range caps {
		if s == value {
			return true
		}
	}
	return false
}
