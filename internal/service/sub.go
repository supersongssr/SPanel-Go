package service

import (
	"fmt"
	"strings"

	"spanel-go/internal/model"
)

// SubscriptionService 提供客户端配置聚合生成服务
type SubscriptionService struct{}

var DefaultSubscriptionService = &SubscriptionService{}

// BuildClashConfig 组装生成 Clash YAML 订阅配置
func (s *SubscriptionService) BuildClashConfig(user *model.User, nodes []model.Node) string {
	var builder strings.Builder

	// Clash 基础配置头部声明
	builder.WriteString("# SPanel-Go Clash Subscription Config\n")
	builder.WriteString("port: 7890\n")
	builder.WriteString("socks-port: 7891\n")
	builder.WriteString("allow-lan: false\n")
	builder.WriteString("mode: Rule\n")
	builder.WriteString("log-level: info\n")
	builder.WriteString("external-controller: 127.0.0.1:9090\n\n")

	builder.WriteString("proxies:\n")

	proxyNames := []string{}

	// 遍历已授权节点并格式化为 Clash Proxy 节点结构
	for _, n := range nodes {
		// 仅添加可用节点
		if n.Type != 1 {
			continue
		}

		name := strings.ReplaceAll(n.Name, " ", "_")
		proxyNames = append(proxyNames, name)

		// 格式化输出 (此处以 Shadowsocks 协议作为初版示范)
		builder.WriteString(fmt.Sprintf("  - name: \"%s\"\n", name))
		builder.WriteString("    type: ss\n")
		builder.WriteString(fmt.Sprintf("    server: %s\n", n.Server))
		builder.WriteString(fmt.Sprintf("    port: %d\n", user.Port))
		builder.WriteString(fmt.Sprintf("    cipher: %s\n", user.Method))
		builder.WriteString(fmt.Sprintf("    password: \"%s\"\n", user.Passwd))
		builder.WriteString("    udp: true\n\n")
	}

	// 写入 Proxies Groups
	builder.WriteString("proxy-groups:\n")
	builder.WriteString("  - name: Proxy\n")
	builder.WriteString("    type: select\n")
	builder.WriteString("    proxies:\n")
	if len(proxyNames) == 0 {
		builder.WriteString("      - DIRECT\n")
	} else {
		for _, name := range proxyNames {
			builder.WriteString(fmt.Sprintf("      - \"%s\"\n", name))
		}
	}

	// 规则集合
	builder.WriteString("\nrules:\n")
	builder.WriteString("  - DOMAIN-SUFFIX,google.com,Proxy\n")
	builder.WriteString("  - MATCH,DIRECT\n")

	return builder.String()
}
