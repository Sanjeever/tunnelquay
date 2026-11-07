package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/Sanjeever/tunnelquay/internal/buildinfo"
	"github.com/Sanjeever/tunnelquay/internal/client"
)

func main() {
	if err := run(); err != nil {
		slog.Error("客户端退出", "错误", err)
		os.Exit(1)
	}
}
func run() error {
	buildinfo.ConfigureHelp("tunnelquay-client")
	address := flag.String("address", "", "服务端直连 IP 或域名")
	port := flag.Int("port", 0, "服务端接入端口")
	credential := flag.String("credential", "", "包含登记秘密和服务端指纹的接入凭据")
	dir := flag.String("data-dir", "", "设备私钥与升级状态目录，默认用户配置目录下的 tunnelquay")
	version := flag.Bool("version", false, "显示版本")
	flag.Parse()
	if *version {
		fmt.Println("TunnelQuay 客户端", buildinfo.Version, buildinfo.Commit)
		return nil
	}
	if *address == "" || *port < 1 || *port > 65535 || *credential == "" {
		flag.Usage()
		return errors.New("必须提供服务端地址、端口和接入凭据")
	}
	if *dir == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		*dir = filepath.Join(config, "tunnelquay")
	}
	c, err := client.New(client.Options{Address: net.JoinHostPort(*address, strconv.Itoa(*port)), Credential: *credential, DataDir: *dir})
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return c.Run(ctx)
}
