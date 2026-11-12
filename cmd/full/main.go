package main

import (
	"flag"
	"log"

	"spanel-go/internal/app/bootstrap"
	"spanel-go/internal/app/router"
)

// 全功能实例 (full)：用户端全部功能 + 管理端 (admin 路由 + admin 静态页面)
// 建议仅部署在内网或网关 (nginx) 白名单之后，不直接暴露公网
func main() {
	port := flag.Int("port", 0, "覆盖监听端口 (默认读取 config.yaml 的 fullPort/port)")
	flag.Parse()

	bootstrap.Run(bootstrap.Options{
		Instance: "full",
		Port:     *port,
		Router:   router.SetupRouter(),
	})

	log.Println("SPanel-Go full instance stopped.")
}
