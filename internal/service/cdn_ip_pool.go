// cdn_ip_pool.go CDN 优选 IP 消费型池 (对齐 PHP app/Services/Dns/CdnIpPool.php)
// + 优选 IP 轮换 (对齐 Job::RotateCdnIps)。
//
// CSV 池: cloudflare_cdn_preferred_ips_csv 配置路径; flock 独占 → 从文件末尾向前找
// 第一个首列为合法 IP 的行 (末尾=最新测速批次) → 删该行重写 → 返回 IP (LIFO 弹出)。
// 耗尽/未配置/失败返回 "" → 调用方回退节点真实 IP。dry-run 用内存快照模拟, 不消费文件。
package service

import (
	"context"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
)

// CdnIpPoolLoad 只读快照 (CSV 原序)。
func CdnIpPoolLoad() []string {
	path := cdnIpCsvPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if ip := cdnIpCsvFirstCol(line); ip != "" {
			out = append(out, ip)
		}
	}
	return out
}

// CdnIpPoolPick 真消费: 弹出文件末尾最后一个合法 IP 行 (LIFO); 不可用返回 ""。
func CdnIpPoolPick() string {
	path := cdnIpCsvPath()
	if path == "" {
		return ""
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return ""
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return ""
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck

	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	// 从末尾向前找第一个首列为合法 IP 的行 (表头/空行/注释/非 IP 行保留原样)
	for i := len(lines) - 1; i >= 0; i-- {
		ip := cdnIpCsvFirstCol(lines[i])
		if ip == "" {
			continue
		}
		newLines := append(append([]string{}, lines[:i]...), lines[i+1:]...)
		content := strings.Join(newLines, "\n")
		if err := f.Truncate(0); err != nil {
			return ""
		}
		if _, err := f.Seek(0, 0); err != nil {
			return ""
		}
		if _, err := f.WriteString(content); err != nil {
			return ""
		}
		return ip
	}
	return ""
}

func cdnIpCsvPath() string {
	if cfg := config.GlobalConfig; cfg != nil {
		return cfg.Node.CloudflareCDNPreferredIPsCSV
	}
	return ""
}

var cdnIpCsvSep = regexp.MustCompile(`[,;\t]`)

// cdnIpCsvFirstCol 提取行首列并校验为合法 IP (BOM 容忍)。
func cdnIpCsvFirstCol(line string) string {
	line = strings.TrimPrefix(line, "\xEF\xBB\xBF")
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	first := line
	if m := cdnIpCsvSep.Split(line, 2); len(m) > 0 {
		first = strings.TrimSpace(m[0])
	}
	if net.ParseIP(first) == nil {
		return ""
	}
	return first
}

// RotateCdnIps 轮换 legacy cdnip= 模式节点的 add=/cdnip= 优选 IP (不动 DNS)。
// 候选: server 含 v2_name=xhttp-cdn 且 parse 后 cdn==='1' 且 cdnip= 非空。
func RotateCdnIps(ctx context.Context, dryRun bool, logf func(string, ...any)) map[string]int64 {
	stats := map[string]int64{"matched": 0, "rotated": 0, "unchanged": 0, "no_ip": 0}
	var nodes []model.Node
	if err := database.DB.WithContext(ctx).
		Select("id", "server").
		Where("server LIKE ?", "%v2_name=xhttp-cdn%").
		Find(&nodes).Error; err != nil {
		return stats
	}

	// dry-run 内存模拟池 (array_pop 语义); real 用真消费
	var simPool []string
	if dryRun {
		simPool = CdnIpPoolLoad()
	}

	for i := range nodes {
		n := &nodes[i]
		raw, err := url.ParseQuery(n.Server)
		if err != nil || raw.Get("cdn") != "1" {
			continue
		}
		stats["matched"]++
		cdnip := raw.Get("cdnip")
		if cdnip == "" {
			stats["no_ip"]++ // 域名模式注册, 非 legacy cdnip 模式
			continue
		}
		var newIP string
		if dryRun {
			if len(simPool) > 0 {
				newIP = simPool[len(simPool)-1]
				simPool = simPool[:len(simPool)-1]
			}
		} else {
			newIP = CdnIpPoolPick()
		}
		if newIP == "" {
			stats["no_ip"]++ // 池耗尽/未配置 → 回退真实 IP 语义由节点端处理, 此处跳过
			continue
		}
		if newIP == cdnip {
			stats["unchanged"]++
			continue
		}
		newServer := replaceParam(n.Server, "add", newIP)
		newServer = replaceParam(newServer, "cdnip", newIP)
		if newServer == n.Server {
			stats["unchanged"]++
			continue
		}
		if !dryRun {
			_ = database.DB.WithContext(ctx).Model(&model.Node{}).
				Where("id = ?", n.ID).
				Update("server", newServer).Error
		}
		stats["rotated"]++
		if logf != nil {
			logf("rotate node=%d cdnip=%s -> %s%s", n.ID, cdnip, newIP, dryMark(dryRun))
		}
	}

	if logf != nil {
		logf("☁️CDN 优选 IP 轮换: 命中 %d, 轮换 %d, 未变 %d, 无IP %d%s",
			stats["matched"], stats["rotated"], stats["unchanged"], stats["no_ip"], dryMark(dryRun))
	}
	return stats
}

// replaceParam 定点替换 query 串中首个 key=value (\bkey=[^&]*)
func replaceParam(query, key, value string) string {
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(key) + `=[^&]*`)
	if err != nil {
		return query
	}
	return re.ReplaceAllString(query, key+"="+value)
}
