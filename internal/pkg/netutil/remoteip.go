// Package netutil 网络侧工具
package netutil

import (
	"net"
	"net/http"
)

// RemoteIP 取请求的直连地址（对齐 PHP $_SERVER["REMOTE_ADDR"]）。
//
// 安全口径：clonepay 的 IP 白名单、backend 的 api_ip_allowlist、节点上报者身份比对
// 等安全决策一律使用直连地址，绝不信任 X-Forwarded-For 等可伪造头。
func RemoteIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// IPAllowed 直连 IP 是否命中白名单（逗号分隔的 IP / CIDR 列表）。
// 空白名单表示不限制；单 IP 支持 IPv4/IPv6；网段支持 CIDR 表示法。
func IPAllowed(remoteIP, allowlist string) bool {
	if allowlist == "" {
		return true
	}
	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return false
	}
	for _, item := range splitComma(allowlist) {
		if item == "" {
			continue
		}
		if _, cidr, err := net.ParseCIDR(item); err == nil {
			if cidr.Contains(ip) {
				return true
			}
		} else if item == remoteIP {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
