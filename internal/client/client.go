package client

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/buildinfo"
	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

type Options struct{ Address, Credential, DataDir string }
type Client struct {
	Options             Options
	ID                  string
	secret, fingerprint string
	identityKey         ed25519.PrivateKey
	mu                  sync.Mutex
	configs             map[string]protocol.Config
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
	if err = os.MkdirAll(opts.DataDir, 0700); err != nil {
		return nil, err
	}
	key, err := loadDeviceKey(opts.DataDir)
	if err != nil {
		return nil, err
	}
	c := &Client{Options: opts, ID: protocol.DeviceID(key.Public().(ed25519.PublicKey)), identityKey: key, secret: secret, fingerprint: fp, configs: map[string]protocol.Config{}, forwards: map[protocol.Stream]forwardTarget{}, installed: make(chan struct{})}
	state, err := c.readUpgrade()
	if err != nil {
		return nil, fmt.Errorf("读取升级状态失败：%w", err)
	}
	c.upgradeTask = state.ID
	return c, nil
}
func (c *Client) Run(ctx context.Context) error {
	delay := time.Second
	for {
		start := time.Now()
		err := c.Connect(ctx)
		var denied *accessError
		if errors.As(err, &denied) && denied.code == "identity_revoked" {
			return err
		}
		select {
		case <-c.installed:
			return nil
		default:
		}
		if ctx.Err() != nil {
			return nil
		}
		slog.Error("接入断开，等待重连", "错误类型", transport.ErrorKind(err), "错误", err, "等待", delay)
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
	session, err := transport.Dial(ctx, c.Options.Address, transport.ClientTLS(c.fingerprint))
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
	h := protocol.Hello{Secret: c.secret, ID: c.ID, Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: buildinfo.Version, Protocols: []int{protocol.Version}, Capabilities: protocol.Capabilities, UpgradeTask: c.upgradeTask}
	m, err := c.authenticate(codec, h)
	if err != nil {
		return err
	}
	if err = protocol.Negotiate(protocol.Hello{Protocols: []int{m.Protocol}, Capabilities: m.Capabilities}); err != nil {
		return fmt.Errorf("服务端协商结果无效：%w", err)
	}
	stream.SetDeadline(time.Time{})
	c.mu.Lock()
	c.session = session
	c.configs = map[string]protocol.Config{}
	c.mu.Unlock()
	slog.Info("设备已接入", "身份", c.ID)
	sessionCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		session.Close()
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
	for {
		stream.SetDeadline(time.Now().Add(protocol.ControlIdleTimeout))
		m, err = codec.Receive()
		if err != nil {
			if ctx.Err() == nil {
				kind := transport.ErrorKind(err)
				level := slog.LevelWarn
				if kind == "eof" || kind == "connection_closed" {
					level = slog.LevelInfo
				}
				slog.Log(ctx, level, "服务端控制连接已结束", "错误类型", kind, "错误", err, "接入诊断", session.Diagnostics())
			}
			return fmt.Errorf("控制消息接收失败：%w", err)
		}
		reply := protocol.Message{Type: "ack", ID: m.ID, Revision: m.Revision}
		switch m.Type {
		case "ping":
			reply.Type = "pong"
		case "upgrade":
			message := m
			c.wg.Go(func() { c.upgrade(sessionCtx, session, codec, message) })
			continue
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
						transport.Interrupt(stream)
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
