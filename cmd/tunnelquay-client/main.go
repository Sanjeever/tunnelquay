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
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

func main() {
	if err := run(); err != nil {
		slog.Error("客户端退出", "错误", err)
		os.Exit(1)
	}
}
func run() error {
	buildinfo.ConfigureHelp("tunnelquay-client")
	connect := flag.String("connect", os.Getenv("TUNNELQUAY_CONNECT"), "单一接入连接串（tq://凭据@地址:端口），也可通过环境变量 TUNNELQUAY_CONNECT 设置")
	address := flag.String("address", "", "服务端直连 IP 或域名")
	port := flag.Int("port", 0, "服务端接入端口")
	credential := flag.String("credential", os.Getenv("TUNNELQUAY_CREDENTIAL"), "包含登记秘密和服务端指纹的接入凭据")
	dir := flag.String("data-dir", "", "设备私钥目录，默认用户配置目录下的 tunnelquay")
	service := flag.String("service", "", "管理系统常驻服务：install（安装并启动）或 uninstall（停止并卸载）")
	version := flag.Bool("version", false, "显示版本")
	flag.Parse()
	if *version {
		fmt.Println("TunnelQuay 客户端", buildinfo.Version, buildinfo.Commit)
		return nil
	}
	if *service != "" && *service != "install" && *service != "uninstall" {
		return errors.New("不支持的服务操作，请使用 install 或 uninstall")
	}
	if *service == "uninstall" {
		if err := client.ManageService("uninstall", client.Options{}); err != nil {
			return err
		}
		fmt.Println("已卸载客户端常驻服务")
		return nil
	}
	var targetAddr, targetCred string
	if *connect != "" {
		var err error
		targetAddr, targetCred, err = transport.ParseConnect(*connect)
		if err != nil {
			return err
		}
	} else {
		if *address == "" || *port < 1 || *port > 65535 || *credential == "" {
			flag.Usage()
			return errors.New("请提供 -connect 连接串（或环境变量 TUNNELQUAY_CONNECT），或同时提供 -address、-port、-credential")
		}
		targetAddr = net.JoinHostPort(*address, strconv.Itoa(*port))
		targetCred = *credential
	}
	if *dir == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		*dir = filepath.Join(config, "tunnelquay")
	}
	if *service != "" {
		if err := client.ManageService(*service, client.Options{Address: targetAddr, Credential: targetCred, DataDir: *dir}); err != nil {
			return err
		}
		fmt.Println("已安装并启动客户端常驻服务")
		return nil
	}
	c, err := client.New(client.Options{Address: targetAddr, Credential: targetCred, DataDir: *dir})
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return c.Run(ctx)
}
