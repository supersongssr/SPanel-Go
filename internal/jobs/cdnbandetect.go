// cdnbandetect.go 对齐 PHP Job::CdnBanDetect() (crontab `*/10 * * * *`, php8)。
//
// 防封号方案 P0-5 (2026-11-05): CDN 节点实弹探测 CF 边缘封禁状态。
//  1. 收集 type=1 且 server 含 v2_name=xhttp-cdn 的节点, parse server 取 cdn==='1',
//     按 host 根域名分组, 每域名取一个样本探测;
//  2. 解析优选域名 (cloudflare_cdn_preferred_domains) 得 CF anycast 边缘 IP;
//  3. 定向拨号 edgeIP:443 + TLS SNI=host (等价 PHP CURLOPT_RESOLVE), GET https://host/;
//  4. 判定 (严格双条件防源站 403 误判): HTTP 403 且 body 含 "Website Access Blocked"
//     或 (cf-wrapper + cloudflare) → 该域名全部 CDN 节点 type=0 下线 + TG 告警;
//     探测异常 (超时/拒连) ≠ 封禁, 只记日志。
//
// 幂等: 下线后 (type=0) 下一轮不再入选, 自然跳过。
package jobs

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/notify"
	"spanel-go/internal/service"
)

func registerCdnBanDetect() {
	Register(&Job{
		Name:     "cdnbandetect",
		Title:    "CDN 封禁实弹探测 (403+特征串双条件 → 域名整组下线 + 告警)",
		Schedule: "*/10 * * * *",
		Timeout:  8 * time.Minute,
		Fn:       cdnBanDetectFn,
	})
}

// cdnDomainGroup 同一根域名下的 CDN 节点组
type cdnDomainGroup struct {
	sample  string  // 样本 host (整组共用封禁判定)
	nodeIDs []int32 // 组内全部节点 id
}

const cdnProbeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36"

func cdnBanDetectFn(ctx context.Context, rc *RunContext) error {
	const prefix = "[CdnBanDetect]"
	stats := map[string]int{
		"domains": 0, "probed": 0, "ok": 0, "banned": 0, "offlined": 0, "errors": 0,
	}

	// 1. 收集活跃 CDN 节点, 按根域名分组 (查询口径与 PHP/RotateCdnIps 一致)
	var nodes []model.Node
	if err := database.DB.WithContext(ctx).
		Select("id", "server").
		Where("server LIKE ? AND type = 1", "%v2_name=xhttp-cdn%").
		Find(&nodes).Error; err != nil {
		return fmt.Errorf("query cdn nodes: %w", err)
	}
	domains := map[string]*cdnDomainGroup{}
	var order []string
	for i := range nodes {
		raw, err := url.ParseQuery(nodes[i].Server)
		if err != nil || raw.Get("cdn") != "1" {
			continue // parse 失败或严格 cdn!=='1' 跳过
		}
		host := raw.Get("host")
		parts := strings.SplitN(host, ".", 2)
		if host == "" || len(parts) < 2 || parts[1] == "" {
			continue
		}
		root := parts[1]
		g := domains[root]
		if g == nil {
			g = &cdnDomainGroup{sample: host}
			domains[root] = g
			order = append(order, root)
		}
		g.nodeIDs = append(g.nodeIDs, nodes[i].ID)
	}
	stats["domains"] = len(domains)
	if len(domains) == 0 {
		rc.Logf("%s 无活跃 CDN 节点, 跳过.", prefix)
		rc.SetSummary("stats", stats)
		return nil
	}

	// 2. 优选域名随机取一个 → 解析 CF anycast 边缘 IP (职责仅是解析出 CF 的 IP)
	preferred := service.PreferredCDNDomains()
	if len(preferred) == 0 {
		rc.Logf("%s 优选域名池为空, 无法取 CF 边缘 IP, 放弃探测 (配置 cloudflare_cdn_preferred_domains).", prefix)
		rc.SetSummary("stats", stats)
		return nil
	}
	pick := preferred[rand.Intn(len(preferred))]
	edgeIP := resolveFirstIPv4(ctx, pick)
	if edgeIP == "" {
		rc.Logf("%s 优选域名 %s 解析失败, 放弃探测.", prefix, pick)
		stats["errors"]++
		rc.SetSummary("stats", stats)
		return nil
	}

	// 3. 定向探测客户端: 拨 edgeIP:443 + SNI=host (绕过本机 DNS, 等价 CURLOPT_RESOLVE)
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse // allow_redirects=false
		},
		Transport: &http.Transport{
			DialTLSContext: func(dctx context.Context, _ string, addr string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				d := net.Dialer{Timeout: 5 * time.Second}
				rawConn, err := d.DialContext(dctx, "tcp", net.JoinHostPort(edgeIP, "443"))
				if err != nil {
					return nil, err
				}
				// verify=false: 探测只关心状态码/body, 证书链与判定无关
				tlsConn := tls.Client(rawConn, &tls.Config{
					ServerName:         host,
					InsecureSkipVerify: true,
				})
				if err := tlsConn.HandshakeContext(dctx); err != nil {
					_ = rawConn.Close()
					return nil, err
				}
				return tlsConn, nil
			},
			DisableKeepAlives: true,
		},
	}

	// 4. 逐域名探测
	for _, root := range order {
		g := domains[root]
		stats["probed"]++

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+g.sample+"/", nil)
		if err != nil {
			stats["errors"]++
			rc.Logf("%s %s (%s) 构造请求失败: %v", prefix, root, g.sample, err)
			continue
		}
		req.Header.Set("User-Agent", cdnProbeUA)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

		resp, err := client.Do(req)
		if err != nil {
			// 探测异常 (超时/连接拒绝/DNS) ≠ 封禁 —— 源站故障等, 仅记录不触发下线
			stats["errors"]++
			rc.Logf("%s %s (%s) 探测异常: %v", prefix, root, g.sample, err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
		_ = resp.Body.Close()
		code := resp.StatusCode

		lower := strings.ToLower(string(body))
		isBanPage := strings.Contains(string(body), "Website Access Blocked") ||
			(strings.Contains(lower, "cf-wrapper") && strings.Contains(lower, "cloudflare"))

		if code == http.StatusForbidden && isBanPage {
			stats["banned"]++
			var idStrs []string
			for _, id := range g.nodeIDs {
				idStrs = append(idStrs, strconv.Itoa(int(id)))
				if rc.DryRun {
					continue
				}
				if err := database.DB.WithContext(ctx).
					Model(&model.Node{}).
					Where("id = ?", id).
					Update("type", 0).Error; err != nil {
					log.Printf("[CdnBanDetect] offline node %d failed: %v", id, err)
					continue
				}
				stats["offlined"]++
			}
			msg := "⛔CDN 域名被 CF 封禁: " + root + " (探测 " + g.sample + " → 403 Website Access Blocked)" +
				", 已下线节点 " + strconv.Itoa(len(g.nodeIDs)) + " 个 (" + strings.Join(idStrs, ",") + ")"
			if rc.DryRun {
				msg += " [DRY-RUN]"
			}
			rc.Logf("%s %s", prefix, msg)
			log.Printf("[CdnBanDetect] %s", msg)
			if !rc.DryRun {
				notify.Send(msg)
			}
		} else {
			stats["ok"]++
			rc.Logf("%s %s (%s) -> %d 正常.", prefix, root, g.sample, code)
		}
	}

	rc.SetSummary("stats", stats)
	return nil
}

// resolveFirstIPv4 解析域名首个 A 记录 (对齐 PHP dns_get_record DNS_A 取首个 ip)
func resolveFirstIPv4(ctx context.Context, domain string) string {
	resolver := &net.Resolver{}
	ips, err := resolver.LookupIPAddr(ctx, domain)
	if err != nil {
		return ""
	}
	for _, ip := range ips {
		if v4 := ip.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}
