// 统一域名池 (对齐 PHP app/Utils/DomainPool.php 中面板侧用到的部分)。
//
// 配置入口: config cloudflare.domain_pool (每域名: provider/zone_id/records_limit/
// expires_at/api_token/cdn)。
// Token 解析规则 (getTokenForDomain):
//  1. 域名配置 api_token 非空 → 直接使用 (每域名独立 CF 账号);
//  2. 否则 → 回退全局 cloudflare.api_token。
//
// 域名池选择: cdn=true 只给 xhttp-cdn 注册; cdn!=true 给普通节点注册。
package service

import (
	"fmt"
	"math/rand"
	"strings"

	"spanel-go/internal/config"
)

// DomainEntry 域名池归一化条目
type DomainEntry struct {
	Domain       string
	Provider     string // 默认 cloudflare
	ZoneID       string // 留空自动查询
	APIToken     string // 留空回退默认 token
	CDN          bool
	RecordsLimit int
	ExpiresAt    string
}

// DomainPoolParse 解析域名池配置 (nil 安全, 未配置返回空)
func DomainPoolParse() []DomainEntry {
	cfg := config.GlobalConfig
	if cfg == nil || len(cfg.Cloudflare.DomainPool) == 0 {
		return nil
	}
	out := make([]DomainEntry, 0, len(cfg.Cloudflare.DomainPool))
	for domain, item := range cfg.Cloudflare.DomainPool {
		entry := DomainEntry{
			Domain:       strings.TrimSpace(domain),
			Provider:     item.Provider,
			ZoneID:       item.ZoneID,
			APIToken:     item.APIToken,
			CDN:          item.CDN,
			RecordsLimit: item.RecordsLimit,
			ExpiresAt:    item.ExpiresAt,
		}
		if entry.Provider == "" {
			entry.Provider = "cloudflare"
		}
		out = append(out, entry)
	}
	return out
}

// DomainPoolNormal 普通域名 (cdn != true), 供节点注册选域
func DomainPoolNormal() []DomainEntry {
	var out []DomainEntry
	for _, e := range DomainPoolParse() {
		if !e.CDN {
			out = append(out, e)
		}
	}
	return out
}

// DomainPoolCDN CDN 域名 (cdn = true), 供 xhttp-cdn 注册选域
func DomainPoolCDN() []DomainEntry {
	var out []DomainEntry
	for _, e := range DomainPoolParse() {
		if e.CDN {
			out = append(out, e)
		}
	}
	return out
}

// TokenForDomain 域名对应 CF token: 域名独立 token 优先, 回退全局默认
func TokenForDomain(domain string) string {
	cfg := config.GlobalConfig
	if cfg == nil {
		return ""
	}
	for _, e := range DomainPoolParse() {
		if e.Domain == domain && e.APIToken != "" {
			return e.APIToken
		}
	}
	return cfg.Cloudflare.APIToken
}

// ZoneIDForDomain 域名 zone id: 配置优先, 空则 API 自动查询 (带缓存)
func ZoneIDForDomain(domain string) (string, error) {
	for _, e := range DomainPoolParse() {
		if e.Domain == domain {
			if e.ZoneID != "" {
				return e.ZoneID, nil
			}
			return CFZoneIDCached(domain, TokenForDomain(domain))
		}
	}
	// 不在池中的域名也可查询 (对齐 PHP getZoneId 传域名配置的语义)
	return CFZoneIDCached(domain, TokenForDomain(domain))
}

// PickDomain 从候选列表随机选一个域名 (注册选域用; 空池返回错误)
func PickDomain(entries []DomainEntry) (DomainEntry, error) {
	if len(entries) == 0 {
		return DomainEntry{}, fmt.Errorf("domain pool empty")
	}
	return entries[rand.Intn(len(entries))], nil
}
