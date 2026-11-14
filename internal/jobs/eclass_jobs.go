// eclass_jobs.go E 类通用辅助任务 (对齐 PHP xcat 手动触发入口, 方案 §2.2 E):
// 本质是"手动跑一次定时任务/安全网", 统一走注册表 name 即命令名。
package jobs

import (
	"context"
	"fmt"
	"time"

	"spanel-go/internal/service"
)

func registerEClassJobs() {
	Register(&Job{
		Name:     "cleanExpiredDns",
		Title:    "清理超期无心跳节点的 DNS 记录 (L2; dailyjob 内含)",
		Schedule: "", // 仅手动
		Timeout:  30 * time.Minute,
		Fn: func(ctx context.Context, rc *RunContext) error {
			for k, v := range service.CleanExpiredDnsRecords(ctx, rc.DryRun, rc.Logf) {
				rc.SetSummary(k, v)
			}
			return nil
		},
	})
	Register(&Job{
		Name:     "cdnzoneaudit",
		Title:    "CDN zone 审计: 橙云/泛解析记录告警 (只告警不删; dailyjob 内含)",
		Schedule: "",
		Timeout:  15 * time.Minute,
		Fn: func(ctx context.Context, rc *RunContext) error {
			for k, v := range service.AuditCdnZones(ctx, rc.DryRun, rc.Logf) {
				rc.SetSummary(k, v)
			}
			return nil
		},
	})
	Register(&Job{
		Name:     "forceResetStaleNodes",
		Title:    "强制重置超 N 天未重置流量的节点 (安全网; dailyjob 内含)",
		Schedule: "",
		Timeout:  30 * time.Minute,
		Fn: func(ctx context.Context, rc *RunContext) error {
			for k, v := range service.ForceResetStaleNodes(ctx, rc.DryRun, rc.Logf) {
				rc.SetSummary(k, v)
			}
			return nil
		},
	})
	Register(&Job{
		Name:     "pruneDeadNodes",
		Title:    "死节点池水位线回收 (危险; --dry-run 预览 / --confirm 执行)",
		Schedule: "",
		Timeout:  1 * time.Hour,
		Fn: func(ctx context.Context, rc *RunContext) error {
			stats := service.PruneDeadNodes(ctx, rc.DryRun, rc.Confirm, rc.Logf)
			for k, v := range stats {
				rc.SetSummary(k, v)
			}
			if aborted, ok := stats["aborted"].(bool); ok && aborted {
				return fmt.Errorf("%v", stats["reason"])
			}
			return nil
		},
	})
	Register(&Job{
		Name:     "calcNodeRate",
		Title:    "节点倍率全量重算 (快照→重算顺序同 dailyjob)",
		Schedule: "",
		Timeout:  30 * time.Minute,
		Fn: func(ctx context.Context, rc *RunContext) error {
			for k, v := range service.SnapshotDailyPeaks(ctx, rc.Logf) {
				rc.SetSummary("rate_snapshot_"+k, v)
			}
			for k, v := range service.RecalculateAll(ctx, rc.Logf) {
				rc.SetSummary("rate_recalc_"+k, v)
			}
			return nil
		},
	})
	Register(&Job{
		Name:     "rotateCdnIps",
		Title:    "轮换 legacy cdnip= 节点的优选 IP (CSV 末尾 LIFO 消费)",
		Schedule: "",
		Timeout:  15 * time.Minute,
		Fn: func(ctx context.Context, rc *RunContext) error {
			for k, v := range service.RotateCdnIps(ctx, rc.DryRun, rc.Logf) {
				rc.SetSummary(k, v)
			}
			return nil
		},
	})
	Register(&Job{
		Name:     "listStaleDNS",
		Title:    "60 天无心跳节点域名 vs CF A/AAAA 匹配报告 (只读 JSON)",
		Schedule: "",
		Timeout:  15 * time.Minute,
		Fn: func(ctx context.Context, rc *RunContext) error {
			report, err := service.ListStaleDNS(ctx)
			if err != nil {
				return err
			}
			rc.Logf("%s", report)
			rc.SetSummary("report_len", len(report))
			return nil
		},
	})
}
