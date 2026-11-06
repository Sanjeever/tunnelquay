package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/Sanjeever/tunnelquay/internal/buildinfo"
	"github.com/Sanjeever/tunnelquay/internal/webui"
)

func main() {
	listen := flag.String("listen", ":7000", "服务端监听地址和端口")
	version := flag.Bool("version", false, "显示版本")
	flag.Parse()
	if *version {
		fmt.Println("TunnelQuay 服务端", buildinfo.Version, buildinfo.Commit)
		return
	}
	log.Println("管理界面监听", *listen)
	log.Fatal(http.ListenAndServe(*listen, webui.Handler()))
}
