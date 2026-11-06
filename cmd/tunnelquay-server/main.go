package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Sanjeever/tunnelquay/internal/buildinfo"
	"github.com/Sanjeever/tunnelquay/internal/server"
)

func main() {
	if err := run(); err != nil {
		slog.Error("服务端退出", "错误", err)
		os.Exit(1)
	}
}
func run() error {
	buildinfo.ConfigureHelp("tunnelquay-server")
	listen := flag.String("listen", ":7000", "HTTP、TLS 和 QUIC 共用的监听地址和端口")
	dir := flag.String("data-dir", "data/server", "数据目录，保存数据库与服务端身份")
	version := flag.Bool("version", false, "显示版本")
	flag.Parse()
	if *version {
		fmt.Println("TunnelQuay 服务端", buildinfo.Version, buildinfo.Commit)
		return nil
	}
	s, err := server.New(server.Options{Listen: *listen, DataDir: *dir})
	if err != nil {
		return err
	}
	defer s.Close()
	if s.TemporaryPassword != "" {
		fmt.Println("临时管理密码：", s.TemporaryPassword, "\n首次登录后请修改密码")
	}
	fmt.Println("客户端接入凭据：", s.Identity.Credential())
	if err = s.Start(); err != nil {
		return err
	}
	slog.Info("服务端已启动", "监听", s.Addr())
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	select {
	case <-ctx.Done():
	case <-s.Done():
	}
	return nil
}
