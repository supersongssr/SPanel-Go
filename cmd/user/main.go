package main

import (
	"flag"
	"log"

	"spanel-go/internal/app/bootstrap"
	"spanel-go/internal/app/router"
)

// 用户端 (minimal)：公网最小实例
// 仅包含用户运行所需功能：auth / user / 节点通信 (node+mu) / 订阅 (link) + 用户端静态页面
// 不注册任何 admin 路由，扫描 /api/v1/admin/* 为真实 404
func main() {
	port := flag.Int("port", 0, "覆盖监听端口 (默认读取 config.yaml 的 userPort/port)")
	flag.Parse()

	bootstrap.Run(bootstrap.Options{
		Instance: "user",
		Port:     *port,
		Router:   router.SetupUserRouter(),
	})

	log.Println("SPanel-Go user instance stopped.")
}
