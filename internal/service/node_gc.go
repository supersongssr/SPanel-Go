// node_gc.go 节点流量安全网与死节点回收
// (对齐 PHP Job::ForceResetStaleNodes / gcDeadNodes / pruneDeadNodes)。
//
// 两级阶梯 (方案 2026-11-11 §五): 32 天 L2 清 DNS (CleanExpiredDnsRecords)
// → 64 天 L3 删行 (gcDeadNodes), 中间整月窗口留给流量耗尽停机节点复机。
package service

import (
	"context"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/notify"
	"spanel-go/internal/pkg/timex"
)

// ForceResetStaleNodes 32 天强制重置安全网 (dailyjob 节点循环之后调用):
// 月度重置被错过 (cron 漏跑/掉线/重置日当月不存在) 时超阈值强制重置一次。
// 返回 stats: {scanned, baseline, reset, skipped}。
func ForceResetStaleNodes(ctx context.Context, dryRun bool, logf func(string, ...any)) map[string]int64 {
	stats := map[string]int64{"scanned": 0, "baseline": 0, "reset": 0, "skipped": 0}
	var days int = 32
	if cfg := config.GlobalConfig; cfg != nil && cfg.Jobs.Params.NodeForceResetStaleDays != 0 {
		days = cfg.Jobs.Params.NodeForceResetStaleDays
	}
	if days <= 0 {
		if logf != nil {
			logf("[ForceReset] Disabled by config: node_force_reset_stale_days=%d", days)
		}
		return stats
	}
	window := int64(days) * 86400
	now := timex.Unix()

	var batch []model.Node
	_ = database.DB.WithContext(ctx).
		Model(&model.Node{}).
		Select("id", "name", "traffic_reset_day", "traffic_last_reset_time", "traffic_limit").
		Where("id > 9 AND is_clone = 0 AND traffic_reset_day > 0").
		Order("id ASC").
		FindInBatches(&batch, 200, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				n := &batch[i]
				stats["scanned"]++
				last := n.TrafficLastResetTime
				switch {
				case last <= 0:
					// 无重置记录: 建立基准 (仅当无基准时)
					if logf != nil {
						logf("[ForceReset] baseline node=%d (%s) reset_day=%d", n.ID, n.Name, n.TrafficResetDay)
					}
					if !dryRun {
						_ = database.DB.WithContext(ctx).Model(&model.Node{}).
							Where("id = ? AND traffic_last_reset_time = 0", n.ID).
							Update("traffic_last_reset_time", now).Error
					}
					stats["baseline"]++
				case now-last > window:
					if logf != nil {
						logf("[ForceReset] STALE node=%d (%s) last_reset=%s age=%dd -> reset%s",
							n.ID, n.Name, timex.DateStr(timex.Now().Add(-timeDur(now-last))),
							(now-last)/86400, dryMark(dryRun))
					}
					if !dryRun {
						var fresh model.Node
						if err := database.DB.WithContext(ctx).First(&fresh, n.ID).Error; err == nil {
							MonthlyResetCycle(&fresh)
							_ = database.DB.WithContext(ctx).Model(&model.Node{}).
								Where("id = ?", n.ID).
								Updates(map[string]any{
									"traffic_used":       fresh.TrafficUsed,
									"traffic_used_daily": fresh.TrafficUsedDaily,
									"traffic_left_daily": fresh.TrafficLeftDaily,
									"traffic_left":       fresh.TrafficLeft,
								}).Error
							MonthlyRecordReset(int64(n.ID))
						}
					}
					stats["reset"]++
				default:
					stats["skipped"]++
				}
			}
			return nil
		})

	if logf != nil {
		logf("🔄节点流量强制重置(%d天安全网): 扫描 %d, 新基准 %d, 强制重置 %d, 跳过 %d%s",
			days, stats["scanned"], stats["baseline"], stats["reset"], stats["skipped"], dryMark(dryRun))
	}
	if !dryRun && stats["reset"] > 0 {
		notify.Warning("节点流量强制重置",
			fmt.Sprintf("%d 个节点超过%d天未重置, 已强制重置", stats["reset"], days))
	}
	return stats
}

func dryMark(dryRun bool) string {
	if dryRun {
		return " [DRY-RUN]"
	}
	return ""
}

func timeDur(secs int64) time.Duration { return time.Duration(secs) * time.Second }

// GcDeadNodes 死节点 L3 级联物理删除 (dailyjob, 在 CleanExpiredDnsRecords 之后)。
// 候选: id>=100 (保留区) AND type=0 AND 0<hb<cutoff; 活 main 的 clone 跳过。
func GcDeadNodes(ctx context.Context, dryRun bool, logf func(string, ...any)) map[string]int64 {
	stats := map[string]int64{"candidates": 0, "deleted": 0, "clones_deleted": 0,
		"dns_deleted": 0, "redis_cleared": 0, "log_rows_deleted": 0, "skipped_clone_of_live": 0}
	var cfg *config.JobsParams
	if c := config.GlobalConfig; c != nil {
		cfg = &c.Jobs.Params
	}
	if cfg != nil && !config.BoolOrDefault(cfg.NodeGcEnable, true) {
		if logf != nil {
			logf("[NodeGc] Disabled by config: node_gc_enable")
		}
		return map[string]int64{"skipped": 1}
	}
	gcDays := int64(64)
	if cfg != nil && cfg.NodeGcDeadDays != 0 {
		gcDays = int64(cfg.NodeGcDeadDays)
	}
	if gcDays <= 0 {
		if logf != nil {
			logf("[NodeGc] Disabled by config: node_gc_dead_days=%d", gcDays)
		}
		return map[string]int64{"skipped": 1}
	}
	dnsBase := int64(32)
	if cfg != nil && cfg.NodeDnsCleanupInactiveDays > 0 {
		dnsBase = int64(cfg.NodeDnsCleanupInactiveDays)
	}
	// 阈值防御: L3 须 > L2×2 且 > 掉线判定窗+7
	adjusted := false
	if gcDays <= dnsBase {
		gcDays = dnsBase * 2
		adjusted = true
	}
	offlineDays := int64(3)
	if cfg != nil && cfg.NodeHeartbeatOfflineSetTimeout > 0 {
		offlineDays = int64(int64(cfg.NodeHeartbeatOfflineSetTimeout) / 86400)
		if cfg.NodeHeartbeatOfflineSetTimeout%86400 != 0 {
			offlineDays++ // ceil
		}
	}
	if gcDays < offlineDays+7 {
		gcDays = offlineDays + 7
		adjusted = true
	}
	if adjusted {
		if logf != nil {
			logf("[NodeGc] 阈值防御: node_gc_dead_days 误配, 已抬升为 %d 天 (须 > dns %d 天且 > 掉线判定 %d 天)",
				gcDays, dnsBase, offlineDays)
		}
		notify.Warning("节点GC阈值防御",
			fmt.Sprintf("[NodeGc] 阈值防御: node_gc_dead_days 误配, 已抬升为 %d 天", gcDays))
	}

	cutoff := timex.Unix() - gcDays*86400
	// 一次性取候选 id (DESC, 防边删边游标错乱)
	var ids []int32
	_ = database.DB.WithContext(ctx).Model(&model.Node{}).
		Where("id >= ? AND type = 0 AND node_heartbeat > 0 AND node_heartbeat < ?", nodeMinAllocatableID, cutoff).
		Order("id DESC").Pluck("id", &ids).Error
	stats["candidates"] = int64(len(ids))
	if len(ids) == 0 {
		return stats
	}

	var aliveMains []int32
	_ = database.DB.WithContext(ctx).Model(&model.Node{}).
		Where("type = 1 AND is_clone = 0").Pluck("id", &aliveMains).Error
	aliveSet := map[int32]bool{}
	for _, id := range aliveMains {
		aliveSet[id] = true
	}

	for _, id := range ids {
		var X model.Node
		if err := database.DB.WithContext(ctx).First(&X, id).Error; err != nil {
			continue // 候选间可能互删
		}
		if X.IsClone > 0 && aliveSet[X.IsClone] {
			stats["skipped_clone_of_live"]++
			continue // 活 main 的暂时掉线 clone
		}
		if dryRun {
			stats["deleted"]++
			if logf != nil {
				logf("  [DRY] gc id=%d is_clone=%d hb=%s name=%s",
					X.ID, X.IsClone, timex.DateStr(timex.Now().Add(-timeDur(timex.Unix()-X.NodeHeartbeat))), X.Name)
			}
			continue
		}
		p := PurgeNode(&X, "dead", cutoff)
		stats["deleted"]++
		stats["clones_deleted"] += int64(p["clones_deleted"])
		stats["dns_deleted"] += int64(p["dns_deleted"])
		stats["redis_cleared"] += int64(p["redis_cleared"])
		stats["log_rows_deleted"] += int64(p["log_rows_deleted"])
	}

	if logf != nil {
		logf("[NodeGc] dead>%dd 候选 %d, 删除 %d 个 (含级联 clone %d, DNS 行 %d, Redis key %d, 日志行 %d), 跳过活main的clone %d%s",
			gcDays, stats["candidates"], stats["deleted"], stats["clones_deleted"], stats["dns_deleted"],
			stats["redis_cleared"], stats["log_rows_deleted"], stats["skipped_clone_of_live"], dryMark(dryRun))
	}
	if !dryRun && stats["deleted"] > 0 {
		notify.Warning("节点自动回收",
			fmt.Sprintf("死节点GC (>%d天无心跳): 删除 %d 个 (含级联 clone %d, DNS 行 %d, Redis key %d, 日志行 %d), 跳过活main的clone %d",
				gcDays, stats["deleted"], stats["clones_deleted"], stats["dns_deleted"],
				stats["redis_cleared"], stats["log_rows_deleted"], stats["skipped_clone_of_live"]))
	}
	return stats
}

// PruneDeadNodes 水位线回收 (手动应急, 安全闸: 无参数只提示)。
// 总数>=min_total 且 pool/total>max_ratio → 按 target 比例计算删除量, id DESC 级联删。
// confirmed 由调用方从 --confirm 解析。
func PruneDeadNodes(ctx context.Context, dryRun, confirmed bool, logf func(string, ...any)) map[string]any {
	stats := map[string]any{}
	if !dryRun && !confirmed {
		if logf != nil {
			logf("[Prune] 危险操作 (不可逆删除)。请用 --dry-run 预览候选, 或 --confirm 实际执行。")
		}
		return map[string]any{"aborted": true, "reason": "need --dry-run or --confirm"}
	}
	var p *config.JobsParams
	if c := config.GlobalConfig; c != nil {
		p = &c.Jobs.Params
	}
	maxR, targetR, minAge, minTot := 0.50, 0.48, int64(32), 1000
	if p != nil {
		maxR, targetR, minAge, minTot = p.NodeDeadPoolMaxRatio, p.NodeDeadPoolTargetRatio,
			int64(p.NodeDeadPoolMinAgeDays), p.NodeDeadPoolMinTotal
	}
	cutoff := timex.Unix() - minAge*86400

	// 配置防御 (不可逆删除, 误配显式中止): 0<=target<max<1
	if targetR < 0 || targetR >= 1 || maxR <= targetR || maxR >= 1 {
		if logf != nil {
			logf("[Prune] 配置错误: node_dead_pool_target_ratio=%v / node_dead_pool_max_ratio=%v (要求 0<=target<max<1, 默认 0.48/0.50), 已中止。", targetR, maxR)
		}
		return map[string]any{"aborted": true, "reason": "bad ratio config"}
	}

	var total, pool int64
	database.DB.WithContext(ctx).Model(&model.Node{}).
		Where("id >= ?", nodeMinAllocatableID).Count(&total)
	database.DB.WithContext(ctx).Model(&model.Node{}).
		Where("id >= ? AND type = 0 AND node_heartbeat > 0 AND node_heartbeat < ?",
			nodeMinAllocatableID, cutoff).Count(&pool)
	ratio := 0.0
	if total > 0 {
		ratio = math.Round(float64(pool)/float64(total)*1e4) / 1e4
	}
	for _, v := range []map[string]any{
		{"total": total}, {"pool": pool}, {"ratio": ratio},
		{"dry_run": dryRun}, {"deleted": 0}, {"clones_deleted": 0},
		{"redis_cleared": 0}, {"skipped_clone_of_live": 0}, {"log_rows_deleted": 0},
	} {
		for k, v2 := range v {
			stats[k] = v2
		}
	}
	if logf != nil {
		logf("[Prune] total=%d pool=%d ratio=%v (触发线>%v, 门槛总数>=%d)", total, pool, ratio, maxR, minTot)
	}
	if total < int64(minTot) {
		if logf != nil {
			logf("[Prune] total=%d < min_total=%d, 跳过 (节点数不足, 无需回收)", total, minTot)
		}
		return stats
	}
	if ratio <= maxR {
		if logf != nil {
			logf("[Prune] ratio=%v <= %v, 跳过 (未超水位线)", ratio, maxR)
		}
		return stats
	}

	d := int(math.Ceil((float64(pool) - targetR*float64(total)) / (1 - targetR)))
	if logf != nil {
		logf("[Prune] ratio=%v > %v, 将回收 ~%d 个 (target %v)%s",
			ratio, maxR, d, targetR, ternary(dryRun, " [DRY-RUN]", " [CONFIRMED]"))
	}

	var ids []int32
	_ = database.DB.WithContext(ctx).Model(&model.Node{}).
		Where("id >= ? AND type = 0 AND node_heartbeat > 0 AND node_heartbeat < ?",
			nodeMinAllocatableID, cutoff).
		Order("id DESC").Limit(d).Pluck("id", &ids).Error
	var aliveMains []int32
	_ = database.DB.WithContext(ctx).Model(&model.Node{}).
		Where("type = 1 AND is_clone = 0").Pluck("id", &aliveMains).Error
	aliveSet := map[int32]bool{}
	for _, id := range aliveMains {
		aliveSet[id] = true
	}

	deleted := int64(0)
	for _, id := range ids {
		var X model.Node
		if err := database.DB.WithContext(ctx).First(&X, id).Error; err != nil {
			continue
		}
		if X.IsClone > 0 && aliveSet[X.IsClone] {
			stats["skipped_clone_of_live"] = stats["skipped_clone_of_live"].(int) + 1
			continue
		}
		if dryRun {
			if logf != nil {
				logf("  [DRY] del id=%d is_clone=%d hb=%s name=%s", X.ID, X.IsClone,
					timex.DateStr(timex.Now().Add(-timeDur(timex.Unix()-X.NodeHeartbeat))), X.Name)
			}
			deleted++
			continue
		}
		pu := PurgeNode(&X, "dead", cutoff)
		deleted++
		stats["clones_deleted"] = stats["clones_deleted"].(int) + pu["clones_deleted"]
		stats["redis_cleared"] = stats["redis_cleared"].(int) + pu["redis_cleared"]
		stats["log_rows_deleted"] = stats["log_rows_deleted"].(int) + pu["log_rows_deleted"]
	}
	stats["deleted"] = deleted
	return stats
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
