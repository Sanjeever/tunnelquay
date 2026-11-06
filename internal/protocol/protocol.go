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
const HeartbeatInterval = 10 * time.Second
const HeartbeatTimeout = 60 * time.Second
const ControlIdleTimeout = 90 * time.Second
const ControlRequestTimeout = 10 * time.Second

var Capabilities = []string{"config-v1", "tcp", "udp", "upgrade-v1"}

type Config struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	DeviceID   string `json:"device_id"`
	Protocol   string `json:"protocol"`
	Host       string `json:"host"`
	TargetPort int    `json:"target_port"`
	PublicPort int    `json:"public_port"`
	Enabled    bool   `json:"enabled"`
	Revision   int64  `json:"revision"`
}

type Hello struct {
	Secret       string   `json:"secret"`
	ID           string   `json:"id"`
	Hostname     string   `json:"hostname"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Version      string   `json:"version"`
	Protocols    []int    `json:"protocols"`
	Capabilities []string `json:"capabilities"`
	UpgradeTask  string   `json:"upgrade_task,omitempty"`
}

type Message struct {
	Type         string          `json:"type"`
	ID           string          `json:"id,omitempty"`
	Code         string          `json:"code,omitempty"`
	Error        string          `json:"error,omitempty"`
	Protocol     int             `json:"protocol,omitempty"`
	Capabilities []string        `json:"capabilities,omitempty"`
	Hello        *Hello          `json:"hello,omitempty"`
	Configs      []Config        `json:"configs,omitempty"`
	Config       *Config         `json:"config,omitempty"`
	Revision     int64           `json:"revision,omitempty"`
	Network      string          `json:"network,omitempty"`
	Target       string          `json:"target,omitempty"`
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
		return errors.New("通信协议不兼容，服务端要求协议 1")
	}
	for _, required := range []string{"config-v1", "tcp", "udp"} {
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
