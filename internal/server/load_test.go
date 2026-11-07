package server

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRepresentativeLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("代表性负载单独执行")
	}
	s := testServer(t)
	ids := make([]string, 5)
	for i := range ids {
		ids[i], _ = testClient(t, s)
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			c, e := target.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	baseline := runtime.NumGoroutine()
	ports := []int{}
	tunnelIDs := []string{}
	for i := 0; i < 20; i++ {
		tun, e := s.SaveTunnel(cfgFor(ids[i%5], "tcp", target.Addr().String()))
		if e != nil || tun.Error != "" {
			t.Fatal(e, tun.Error)
		}
		ports = append(ports, tun.PublicPort)
		tunnelIDs = append(tunnelIDs, tun.ID)
	}
	var peak atomic.Uint64
	stop := make(chan struct{})
	sampleDone := make(chan struct{})
	go func() {
		defer close(sampleDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}
			select {
			case <-ticker.C:
			case <-stop:
				return
			}
		}
	}()
	started := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	payload := bytes.Repeat([]byte("tunnelquay-load-"), 1<<18)
	for _, port := range ports {
		wg.Go(func() {
			conn, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
			if e != nil {
				errs <- e
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(20 * time.Second))
			writeDone := make(chan error, 1)
			go func() { _, e := conn.Write(payload); conn.(*net.TCPConn).CloseWrite(); writeDone <- e }()
			n, e := io.Copy(io.Discard, conn)
			if e != nil {
				errs <- e
			} else if n != int64(len(payload)) {
				errs <- fmt.Errorf("回包长度不符 %d", n)
			}
			if e = <-writeDone; e != nil {
				errs <- e
			}
		})
	}
	wg.Wait()
	duration := time.Since(started)
	close(stop)
	<-sampleDone
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	latencies := []time.Duration{}
	for i := 0; i < 100; i++ {
		conn, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", ports[i%20]), 3*time.Second)
		if e != nil {
			t.Fatal(e)
		}
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		start := time.Now()
		conn.Write([]byte("r"))
		b := make([]byte, 1)
		_, e = io.ReadFull(conn, b)
		latencies = append(latencies, time.Since(start))
		conn.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return len(s.connections) == 0 })
	for _, id := range tunnelIDs {
		if err = s.DeleteTunnel(id); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return runtime.NumGoroutine() <= baseline+15 })
	t.Logf("环境 %s/%s，Go %s，CPU %d；5 设备（TCP），20 TCP 隧道，20 并发，单连接 %.2f MiB，双向 %.2f MiB/s，耗时 %s；100 次新连接首字节 p50=%s，p95=%s；采样峰值 Go HeapAlloc %.2f MiB（同一测试进程包含服务端、客户端和负载），清理后 goroutine %d，基线 %d", runtime.GOOS, runtime.GOARCH, runtime.Version(), runtime.NumCPU(), float64(len(payload))/(1<<20), float64(len(payload)*40)/(1<<20)/duration.Seconds(), duration, latencies[49], latencies[94], float64(peak.Load())/(1<<20), runtime.NumGoroutine(), baseline)
}
