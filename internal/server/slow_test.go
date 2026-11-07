package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/client"
)

func TestSlowLinkTransferAndControl(t *testing.T) {
	if os.Getenv("TUNNELQUAY_SLOW_TEST") != "1" {
		t.Skip("需要隔离网络空间内的慢链路配置")
	}
	size := int64(10 << 20)
	if v := os.Getenv("TUNNELQUAY_SLOW_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			t.Fatal("测试数据大小无效")
		}
		size = n
	}
	for _, network := range []string{"tcp"} {
		t.Run(network, func(t *testing.T) {
			s, err := New(Options{Listen: "127.0.0.1:7000", DataDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Store.Set("port_range", "27000-27009"); err != nil {
				t.Fatal(err)
			}
			if err = s.Start(); err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			target, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			go func() {
				for {
					conn, e := target.Accept()
					if e != nil {
						return
					}
					go func() { defer conn.Close(); io.Copy(conn, conn) }()
				}
			}()
			c, err := client.New(client.Options{Address: s.Addr(), Credential: s.Identity.Credential(), DataDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); c.Run(ctx) }()
			defer func() { cancel(); <-done }()
			deadline := time.Now().Add(45 * time.Second)
			var original *peer
			ready := false
			for time.Now().Before(deadline) {
				s.mu.RLock()
				original = s.peers[c.ID]
				d := s.devices[c.ID]
				s.mu.RUnlock()
				if original != nil && d.RTT != nil {
					ready = true
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if !ready {
				t.Fatal("慢链路接入失败")
			}
			tun, err := s.SaveTunnel(cfgFor(c.ID, "tcp", target.Addr().String()))
			if err != nil || tun.Error != "" {
				t.Fatal("创建慢链路隧道失败", err, tun.Error)
			}
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tun.Desired.PublicPort), 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(6 * time.Minute))
			var received atomic.Int64
			controlDone := make(chan error, 1)
			go func() {
				for received.Load() < 64<<10 {
					select {
					case <-ctx.Done():
						controlDone <- ctx.Err()
						return
					case <-time.After(50 * time.Millisecond):
					}
				}
				start := time.Now()
				res, e := (&http.Client{Timeout: 30 * time.Second}).Get("http://" + s.Addr() + "/api/version")
				if e != nil {
					controlDone <- e
					return
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
				next := tun.Desired
				next.Name = "慢链路传输中更新"
				updated, e := s.SaveTunnel(next)
				if e == nil && updated.Error != "" {
					e = fmt.Errorf("传输中配置应用失败：%s", updated.Error)
				}
				t.Logf("传输中管理查询与配置应用耗时 %s", time.Since(start))
				controlDone <- e
			}()
			upHash, downHash := sha256.New(), sha256.New()
			writeDone := make(chan error, 1)
			started := time.Now()
			go func() {
				_, e := io.Copy(conn, io.TeeReader(io.LimitReader(rand.New(rand.NewSource(42)), size), upHash))
				conn.(*net.TCPConn).CloseWrite()
				writeDone <- e
			}()
			buf := make([]byte, 32<<10)
			var readErr error
			for {
				n, e := conn.Read(buf)
				if n > 0 {
					downHash.Write(buf[:n])
					received.Add(int64(n))
				}
				if e != nil {
					if e != io.EOF {
						readErr = e
					}
					break
				}
			}
			if readErr != nil || received.Load() != size {
				conn.Close()
			}
			writeErr := <-writeDone
			if os.Getenv("TUNNELQUAY_SLOW_EXPECT_DROP") == "1" {
				if received.Load() == size {
					t.Fatal("预期的持续断网没有中断原业务连接")
				}
				if e := <-controlDone; e != nil {
					t.Fatal(e)
				}
				waitSlowState(t, 45*time.Second, "设备离线", func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.devices[c.ID].Online })
				s.mu.RLock()
				disabled := s.tunnels[tun.Desired.ID].Desired
				s.mu.RUnlock()
				disabled.Enabled = false
				if _, e := s.SaveTunnel(disabled); e != nil {
					t.Fatal(e)
				}
				waitSlowState(t, 90*time.Second, "同一身份恢复", func() bool {
					s.mu.RLock()
					defer s.mu.RUnlock()
					return s.devices[c.ID].Online && s.devices[c.ID].RTT != nil
				})
				s.mu.RLock()
				restored := s.peers[c.ID]
				running := s.running[tun.Desired.ID]
				active := len(s.connections)
				latest := s.tunnels[tun.Desired.ID].Desired
				s.mu.RUnlock()
				if restored == original || running != nil || active != 0 || latest.Enabled {
					t.Fatal("重连没有清理原连接或重新启用了停用隧道")
				}
				latest.Enabled = true
				applied, e := s.SaveTunnel(latest)
				if e != nil || applied.Error != "" {
					t.Fatal(e, applied.Error)
				}
				probe, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", latest.PublicPort), 10*time.Second)
				if e != nil {
					t.Fatal(e)
				}
				probe.SetDeadline(time.Now().Add(10 * time.Second))
				message := []byte("持续断线后的新连接")
				if _, e = probe.Write(message); e != nil {
					probe.Close()
					t.Fatal(e)
				}
				reply := make([]byte, len(message))
				_, e = io.ReadFull(probe, reply)
				probe.Close()
				if e != nil || string(reply) != string(message) {
					t.Fatal("重新启用后转发失败", e)
				}
				t.Logf("%s 持续断线关闭原连接，同身份恢复且尊重停用，新连接校验通过", network)
				return
			}
			if readErr != nil || writeErr != nil || received.Load() != size {
				cancel()
				<-controlDone
				t.Fatalf("慢链路中断：接收 %d/%d，读 %v，写 %v", received.Load(), size, readErr, writeErr)
			}
			if fmt.Sprintf("%x", upHash.Sum(nil)) != fmt.Sprintf("%x", downHash.Sum(nil)) {
				t.Fatal("传输校验失败")
			}
			select {
			case e := <-controlDone:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(35 * time.Second):
				t.Fatal("管理操作未完成")
			}
			s.mu.RLock()
			current := s.peers[c.ID]
			s.mu.RUnlock()
			if current != original {
				t.Fatal("传输期间发生接入重连")
			}
			t.Logf("%s 完成 %d 字节双向校验，耗时 %s，接入未重连", network, size, time.Since(started))
		})
	}
}

func waitSlowState(t *testing.T, timeout time.Duration, name string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("慢链路状态未完成：%s", name)
}
