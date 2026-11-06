package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/buildinfo"
	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

type Options struct{ Address, Credential, Transport, DataDir string }
type Client struct {
	Options             Options
	ID                  string
	secret, fingerprint string
	mu                  sync.Mutex
	configs             map[string]protocol.Config
	udp                 map[uint64]*udpTarget
	forwards            map[protocol.Stream]forwardTarget
	session             *transport.Session
	wg                  sync.WaitGroup
	upgradeMu           sync.Mutex
	installed           chan struct{}
	upgradeTask         string
}

func New(opts Options) (*Client, error) {
	secret, fp, err := transport.ParseCredential(opts.Credential)
	if err != nil {
		return nil, err
	}
	if opts.Transport != "tcp" && opts.Transport != "quic" {
		return nil, errors.New("传输方式必须为 quic 或 tcp")
	}
	if err = os.MkdirAll(opts.DataDir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(opts.DataDir, "device-id")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		b = []byte(transport.RandomID())
		if err = os.WriteFile(path, b, 0600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(b) != 48 {
		return nil, errors.New("本地设备身份文件无效")
	}
	c := &Client{Options: opts, ID: string(b), secret: secret, fingerprint: fp, configs: map[string]protocol.Config{}, udp: map[uint64]*udpTarget{}, forwards: map[protocol.Stream]forwardTarget{}, installed: make(chan struct{})}
	return c, nil
}
func (c *Client) Run(ctx context.Context) error {
	delay := time.Second
	for {
		start := time.Now()
		err := c.Connect(ctx)
		select {
		case <-c.installed:
			return nil
		default:
		}
		if ctx.Err() != nil {
			return nil
		}
		slog.Error("接入断开，等待重连", "错误", err, "等待", delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil
		}
		if time.Since(start) > time.Minute {
			delay = time.Second
		} else {
			delay = min(delay*2, 30*time.Second)
		}
	}
}
func (c *Client) Connect(ctx context.Context) error {
	session, err := transport.Dial(ctx, c.Options.Address, c.Options.Transport, transport.ClientTLS(c.fingerprint))
	if err != nil {
		return fmt.Errorf("连接服务端失败：%w", err)
	}
	defer session.Close()
	stream, err := session.Open(ctx)
	if err != nil {
		return err
	}
	defer transport.Abort(stream)
	stream.SetDeadline(time.Now().Add(10 * time.Second))
	codec := &protocol.Codec{RW: stream}
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}
	h := protocol.Hello{Secret: c.secret, ID: c.ID, Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: buildinfo.Version, Protocols: []int{protocol.Version}, Capabilities: protocol.Capabilities}
	if err = codec.Send(protocol.Message{Type: "hello", Hello: &h}); err != nil {
		return err
	}
	m, err := codec.Receive()
	if err != nil {
		return err
	}
	if m.Type != "welcome" {
		return fmt.Errorf("接入被拒绝 [%s]：%s", m.Code, m.Error)
	}
	stream.SetDeadline(time.Time{})
	c.mu.Lock()
	c.session = session
	c.configs = map[string]protocol.Config{}
	c.mu.Unlock()
	slog.Info("设备已接入", "身份", c.ID, "传输", session.Network)
	sessionCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		session.Close()
		c.mu.Lock()
		for _, u := range c.udp {
			u.conn.Close()
		}
		c.mu.Unlock()
		c.wg.Wait()
	}()
	c.wg.Go(func() {
		select {
		case <-sessionCtx.Done():
			session.Close()
		case <-c.installed:
			session.Close()
		}
	})
	c.wg.Go(func() { c.acceptStreams(sessionCtx, session) })
	if session.QUIC != nil {
		c.wg.Go(func() { c.receiveUDP(sessionCtx, session) })
	}
	for {
		stream.SetDeadline(time.Now().Add(40 * time.Second))
		m, err = codec.Receive()
		if err != nil {
			return err
		}
		reply := protocol.Message{Type: "ack", ID: m.ID, Revision: m.Revision}
		switch m.Type {
		case "ping":
			reply.Type = "pong"
		case "prepare":
			if m.Config == nil {
				reply.Error = "缺少隧道配置"
			} else {
				cfg := *m.Config
				c.mu.Lock()
				c.configs[cfg.ID] = cfg
				c.mu.Unlock()
			}
		case "snapshot":
			c.mu.Lock()
			next := map[string]protocol.Config{}
			for _, cfg := range m.Configs {
				next[cfg.ID] = cfg
			}
			c.configs = next
			c.mu.Unlock()
		case "stop":
			if m.Config != nil {
				c.mu.Lock()
				for stream, target := range c.forwards {
					if target.tunnelID == m.Config.ID && target.revision <= m.Config.Revision {
						target.cancel()
						transport.Abort(stream)
					}
				}
				if cfg, ok := c.configs[m.Config.ID]; ok && cfg.Revision <= m.Config.Revision {
					delete(c.configs, m.Config.ID)
				}
				c.mu.Unlock()
			}
		default:
			reply.Code = "unsupported_message"
			reply.Error = "不支持的控制消息"
		}
		if err = codec.Send(reply); err != nil {
			return err
		}
	}
}
