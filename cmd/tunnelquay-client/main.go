package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Sanjeever/tunnelquay/internal/buildinfo"
)

func main() {
	version := flag.Bool("version", false, "显示版本")
	flag.Parse()
	if *version {
		fmt.Println("TunnelQuay 客户端", buildinfo.Version, buildinfo.Commit)
		return
	}
	fmt.Fprintln(os.Stderr, "客户端接入将在接入协议阶段实现")
	os.Exit(1)
}
