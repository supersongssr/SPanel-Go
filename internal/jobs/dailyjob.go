// dailyjob.go 对齐 PHP Job::DailyJob() (crontab `11 4 * * *`), 18 步顺序锁死:
//
//	① 耗尽禁用 → ② 日限额降档 → ③ 审计清理 → ④ 节点循环(月度重置→心跳下线→
//	7日窗口→健康分) → ⑤ 32天强制重置 → TG → ⑥ 峰值快照→⑦ 倍率重算(顺序不可颠倒)
//	→ ⑧ 禁用32天未用 → ⑨ 余额<0 → ⑩ 周期记账 → ⑪ 周期限额降档 → ⑫ 活跃积分
//	→ ⑬ 邀请返利回收 → ⑭ 每日快照 → ⑮ DNS清理(L2) → ⑯ 死节点GC(L3)
//	→ ⑰ CDN zone 审计 → ⑱ TG 收工
//
// PHP 的 UserActionLogger 审计由台账 summary 承接 (方案 §6.6); 各阶段 TG 播报保留。
package jobs

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/notify"
	"spanel-go/internal/pkg/timex"
	"spanel-go/internal/service"
)

func registerDailyJob() {
	Register(&Job{
		Name:     "dailyjob",
		Title:    "每日主任务: 用户配额处置 + 节点月度重置/健康分/倍率 + DNS清理 + 死节点GC + CDN审计",
		Schedule: "11 4 * * *",
		Timeout:  2 * time.Hour,
		Fn:       dailyJobFn,
	})
}

func dailyJobFn(ctx context.Context, rc *RunContext) error {
	// ① 耗尽类禁用 (无豁免, 含0/1组)
	mergeSummary(rc, "exhausted_", service.DisableExhaustedUsers(ctx, rc.DryRun, rc.Logf).Summary())
	// ② 日限额降档 (0/1组豁免)
	mergeSummary(rc, "daily_", service.DowngradeDailyTrafficOverUsers(ctx, rc.DryRun, rc.Logf).Summary())
	// ③ 审计日志清理 (按用户保留 N 条 + 匿名按天)
	cleanUserActionLog(ctx, rc)

	// ④ 节点循环: 月度重置 → 心跳下线 → 7日窗口 → 健康分 (runDaily 先于 node_sort)
	nodeDailyLoop(ctx, rc)

	// ⑤ 32 天强制重置安全网 (节点循环之后, 本月已重置的基准已刷新)
	forceStats := service.ForceResetStaleNodes(ctx, rc.DryRun, rc.Logf)
	for k, v := range forceStats {
		rc.SetSummary("force_reset_"+k, v)
	}
	rcNotify(rc, "节点梳理完毕")

	// ⑥⑦ 峰值快照 → 倍率重算 (顺序不可颠倒: main 公式依赖 traffic_max_day_value)
	for k, v := range service.SnapshotDailyPeaks(ctx, rc.Logf) {
		rc.SetSummary("rate_snapshot_"+k, v)
	}
	for k, v := range service.RecalculateAll(ctx, rc.Logf) {
		rc.SetSummary("rate_recalc_"+k, v)
	}

	// 顺序注释对齐 PHP: 耗尽禁用 → 日限额 → 未使用禁用 → 周期重置 → 余额负 → 周期限额 → 积分/返利 → 快照
	// ⑧ 禁用超 32 天未使用用户
	mergeSummary(rc, "unused_", service.DisableUnusedUsers(ctx, rc.DryRun, rc.Logf).Summary())
	rcNotify(rc, "未使用用户梳理完毕")

	// ⑨ 余额<0 用户处置
	mergeSummary(rc, "negative_", service.DisableNegativeMoneyUsers(ctx, rc.DryRun, rc.Logf).Summary())
	rcNotify(rc, "余额梳理完毕")

	// ⑩ 周期记账重置 (不受 user_cycle_quota_enabled 门控, 必须始终执行)
	mergeSummary(rc, "cycle_", service.ResetUserTrafficCycle(ctx, rc.DryRun, rc.Logf).Summary())
	rcNotify(rc, "流量周期完毕")

	// ⑪ 周期限额降档
	mergeSummary(rc, "total_", service.DowngradeTotalTrafficOverUsers(ctx, rc.DryRun, rc.Logf).Summary())
	rcNotify(rc, "总流量完毕")

	// ⑫ 活跃积分 (近24h使用, group>1, money>0, class>0)
	mergeSummary(rc, "active_", service.AddActiveScore(ctx, rc.DryRun, rc.Logf).Summary())
	rcNotify(rc, "积分完毕")

	// ⑬ 邀请返利回收 (余额<0 且 ref_by≠0 且 score<64)
	mergeSummary(rc, "invite_", service.ClawbackInvites(ctx, rc.DryRun, rc.Logf).Summary())
	rcNotify(rc, "邀请返利完毕")

	// ⑭ 每日快照 (last_day_t=d, rss/ips 昨日值; 按分组 1-8)
	mergeSummary(rc, "snapshot_", service.DailySnapshot(ctx, rc.DryRun, rc.Logf).Summary())
	rcNotify(rc, fmt.Sprintf("订阅统计完毕，总计处理用户数：%d", snapshotUserCount(rc)))

	// ⑮ 清理超期无心跳节点的 DNS (L2)
	for k, v := range service.CleanExpiredDnsRecords(ctx, rc.DryRun, rc.Logf) {
		rc.SetSummary("dns_clean_"+k, v)
	}

	// ⑯ 死节点 L3 回收 (L2 之后)
	for k, v := range service.GcDeadNodes(ctx, rc.DryRun, rc.Logf) {
		rc.SetSummary("node_gc_"+k, v)
	}

	// ⑰ CDN zone 审计 (橙云/泛解析告警)
	for k, v := range service.AuditCdnZones(ctx, rc.DryRun, rc.Logf) {
		rc.SetSummary("cdn_audit_"+k, v)
	}

	// ⑱ 收工
	rcNotify(rc, "姐姐姐姐，今日任务完成了呢,很开心")
	return nil
}

// nodeDailyLoop 节点循环 (对齐 PHP DailyJob L1056-1127):
// 月度重置(Redis 防重) → 心跳超时下线 → runDaily(7日窗口) → node_sort 重算 → save。
func nodeDailyLoop(ctx context.Context, rc *RunContext) {
	rdb := database.Redis
	now := timex.Unix()
	var offlineTimeout int64 = 259200
	engine := "v2"
	if cfg := config.GlobalConfig; cfg != nil {
		if cfg.Jobs.Params.NodeHeartbeatOfflineSetTimeout > 0 {
			offlineTimeout = int64(cfg.Jobs.Params.NodeHeartbeatOfflineSetTimeout)
		}
		engine = cfg.Jobs.Params.NodeSortEngine
	}

	stats := map[string]int64{"monthly_reset": 0, "heartbeat_offlined": 0, "sort_recalced": 0}
	resetMonth := timex.Ymd()[:6] // date("Ym")

	var batch []model.Node
	_ = database.DB.WithContext(ctx).
		Model(&model.Node{}).
		Where("id > 9").
		Order("id ASC").
		FindInBatches(&batch, 200, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				node := &batch[i]
				updates := map[string]any{}

				// 1. 月度周期流量重置 (今天是本节点 reset_day 才触发; Redis 防重)
				if service.MonthlyIsResetDayToday(node) && rdb != nil {
					resetKey := fmt.Sprintf("node_reset_%d_%s", node.ID, resetMonth)
					if rdb.Get(ctx, resetKey).Err() != nil { // 不存在 → 未重置
						service.MonthlyResetCycle(node)
						updates["traffic_used"] = node.TrafficUsed
						updates["traffic_used_daily"] = node.TrafficUsedDaily
						updates["traffic_left_daily"] = node.TrafficLeftDaily
						updates["traffic_left"] = node.TrafficLeft
						if node.IsClone == 0 && !rc.DryRun {
							service.MonthlyRecordReset(int64(node.ID))
						}
						if !rc.DryRun {
							rdb.SetEx(ctx, resetKey, "1", 86400*2) //nolint:errcheck
						}
						stats["monthly_reset"]++
						rc.Logf("月度重置: node=%d reset_day=%d", node.ID, node.TrafficResetDay)
					}
				}

				// 2. 节点掉线检测 (超过阈值无心跳 → type=0)
				if node.NodeHeartbeat < now-offlineTimeout && node.Type != 0 {
					node.Type = 0
					updates["type"] = 0
					stats["heartbeat_offlined"]++
				}

				// 3. 每日流量快照 + 7 日窗口 (dry-run 不写 Redis, 窗口保持只读)
				if !rc.DryRun {
					service.NodeDailyRunDaily(ctx, node)
				}

				// 3b. node_sort 健康分重算 (runDaily 之后: 窗口[0]=刚推入的昨日增量)
				//     前置过滤: 真实节点(type≠0, is_clone=0) + 非哨兵(>-900) + (v2: hb>0)
				if node.Type != 0 && node.IsClone == 0 && node.NodeSort > -900 {
					if engine != "v1" && node.NodeHeartbeat > 0 {
						w := service.NodeDailyWindow7d(ctx, int64(node.ID))
						newSort := service.NodeSortCalculate(ctx, node, w)
						if int64(newSort) != int64(node.NodeSort) {
							node.NodeSort = int32(newSort)
							updates["node_sort"] = newSort
							stats["sort_recalced"]++
						}
					} else if engine == "v1" {
						newSort := service.NodeSortCalculateV1(node)
						if int64(newSort) != int64(node.NodeSort) {
							node.NodeSort = int32(newSort)
							updates["node_sort"] = newSort
							stats["sort_recalced"]++
						}
					}
				}

				if !rc.DryRun && len(updates) > 0 {
					_ = database.DB.WithContext(ctx).Model(&model.Node{}).
						Where("id = ?", node.ID).Updates(updates).Error
				}
			}
			return nil
		})
	for k, v := range stats {
		rc.SetSummary("nodeloop_"+k, v)
	}
}

// cleanUserActionLog 审计日志清理 (对齐 PHP Job::CleanUserActionLog):
// 具名用户每用户保留最近 N 条; 匿名 (user_id=0) 按天清理 (防爆破刷爆表)。
func cleanUserActionLog(ctx context.Context, rc *RunContext) {
	keep := 100
	anonDays := 7
	if cfg := config.GlobalConfig; cfg != nil {
		keep = cfg.Jobs.Params.UserActionLogRetention
		anonDays = cfg.Jobs.Params.UserActionLogAnonRetentionDays
	}
	if keep <= 0 && anonDays <= 0 {
		return
	}
	deletedNamed, deletedAnon := int64(0), int64(0)

	if keep > 0 {
		// 超上限用户逐个删除多余旧行 (NOT IN 派生表, MySQL 安全写法)
		var over []struct {
			UserID int32 `gorm:"column:user_id"`
		}
		_ = database.DB.WithContext(ctx).Table("user_action_log").
			Select("user_id").Where("user_id > 0").
			Group("user_id").Having("COUNT(*) > ?", keep).
			Scan(&over).Error
		for _, row := range over {
			res := database.DB.WithContext(ctx).Exec(
				`DELETE FROM user_action_log WHERE user_id = ? AND id NOT IN (
				   SELECT id FROM (SELECT id FROM user_action_log WHERE user_id = ? ORDER BY id DESC LIMIT ?) t)`,
				row.UserID, row.UserID, keep)
			if res.Error == nil {
				deletedNamed += res.RowsAffected
			}
		}
	}
	if anonDays > 0 {
		cut := timex.Now().AddDate(0, 0, -anonDays).Format("2006-01-02 15:04:05")
		res := database.DB.WithContext(ctx).
			Exec("DELETE FROM user_action_log WHERE user_id = 0 AND created_at < ?", cut)
		if res.Error == nil {
			deletedAnon = res.RowsAffected
		}
	}
	rc.SetSummary("actionlog_deleted_named", deletedNamed)
	rc.SetSummary("actionlog_deleted_anon", deletedAnon)
	rc.Logf("审计日志清理完毕 (具名 %d 行, 匿名 %d 行)", deletedNamed, deletedAnon)
}

// rcNotify 阶段播报 (dry-run 只打日志不发 TG, 防预览刷屏)
func rcNotify(rc *RunContext, msg string) {
	if rc.DryRun {
		rc.Logf("[TG-dry] %s", msg)
		return
	}
	notify.Send(msg)
}

func mergeSummary(rc *RunContext, prefix string, m map[string]any) {
	for k, v := range m {
		rc.SetSummary(prefix+k, v)
	}
}

func snapshotUserCount(rc *RunContext) int64 {
	if v, ok := rc.Summary()["snapshot_snapshot_users"]; ok {
		if n, ok2 := v.(int); ok2 {
			return int64(n)
		}
	}
	return 0
}
