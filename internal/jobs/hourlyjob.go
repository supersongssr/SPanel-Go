// hourlyjob.go 对齐 PHP Job::HourlyJob() (crontab `30 * * * *`)。
//
// 步骤 (顺序对齐 PHP, 各步独立容错互不阻断):
//  1. 每小时限额超额静默降档 (0/1组豁免; PHP 的 UserActionLogger 审计暂由台账 summary 承接)
//  2. 节点心跳超时下线 (type=1→0, 默认 3 天)
//  3. reset_day 缺失告警合并窗口尾部 flush (满窗即发, 兜底收敛到 ~1h)
//  4. 用户流量日汇总安全网 (防 DbClean 漏跑致明细未聚合即被删; 失败只记日志)
//
// 注: PHP HourlyJob 内的 CdnBanDetect 小时兜底不再重复调用 —— Go 侧 cdnbandetect
// 独立任务 `*/10 * * * *` 注册进调度器, 频率语义等价 (方案 §2.2 A)。
package jobs

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"gorm.io/gorm"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/notify"
	"spanel-go/internal/pkg/timex"
	"spanel-go/internal/pkg/tools"
	"spanel-go/internal/service"
)

func registerHourlyJob() {
	Register(&Job{
		Name:     "hourlyjob",
		Title:    "每小时任务: 小时限额降档 + 节点心跳下线 + reset_day 告警兜底 + 流量汇总安全网",
		Schedule: "30 * * * *",
		Timeout:  time.Hour,
		Fn:       hourlyJobFn,
	})
}

func hourlyJobFn(ctx context.Context, rc *RunContext) error {
	downgradeHourlyTrafficOverUsers(ctx, rc)
	nodeHeartbeatCheck(ctx, rc)
	service.FlushPendingResetDayWarn()
	if err := service.AggregateRecentDetail(2); err != nil {
		rc.Logf("UserTrafficRollup hourly aggregate fail: %v", err)
	}
	return nil
}

// downgradeHourlyTrafficOverUsers 对齐 PHP DowngradeHourlyTrafficOverUsers:
// Redis 快照 ssp:user:{id}:traffic_lasthour (TTL 4600s) 与本轮差值 > 限额 → 静默降一档。
func downgradeHourlyTrafficOverUsers(ctx context.Context, rc *RunContext) {
	cfg := config.GlobalConfig
	if cfg == nil || !cfg.Jobs.Params.UserRateLimitEnabled {
		rc.Logf("小时/日限额模块已关闭 (user_rate_limit_enabled), 跳过")
		return
	}
	limitGB := cfg.Jobs.Params.UserHourlyTrafficLimitGB
	if limitGB <= 0 {
		rc.Logf("每小时流量限额已关闭 (user_hourly_traffic_limit=%d), 跳过", limitGB)
		return
	}
	limitBytes := int64(limitGB) << 30

	rdb := database.Redis
	if rdb == nil {
		rc.Logf("Redis 不可用, 跳过小时限额降档")
		return
	}

	now := timex.Unix()
	activeUsers, downgraded := 0, 0
	var downgradedIDs []int32 // 摘要保留前 20 个 (审计承接, 方案 §6.6)

	var batch []model.User
	_ = database.DB.WithContext(ctx).
		Model(&model.User{}).
		Select("id", "u", "d", "node_group").
		Where("enable = 1 AND node_group > 1 AND t > ?", now-3600).
		Order("id ASC").
		FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				u := &batch[i]
				activeUsers++
				total := u.U + u.D
				key := fmt.Sprintf("ssp:user:%d:traffic_lasthour", u.ID)

				if lastStr, err := rdb.Get(ctx, key).Result(); err == nil {
					if last, perr := strconv.ParseInt(lastStr, 10, 64); perr == nil {
						used := total - last
						if used > limitBytes {
							from := u.NodeGroup
							if rc.DryRun {
								downgraded++
								if len(downgradedIDs) < 20 {
									downgradedIDs = append(downgradedIDs, u.ID)
								}
								rc.Logf("每小时超限降档: user=%d G:%d->%d used=%s",
									u.ID, from, from-1, tools.FlowAutoShow(float64(used)))
								continue // dry-run: 只预览不降档不刷快照
							}
							// 条件更新 (node_group>1 才降) 防并发双降
							tx := database.DB.WithContext(ctx).
								Model(&model.User{}).
								Where("id = ? AND node_group > 1", u.ID).
								Update("node_group", gorm.Expr("node_group - 1"))
							if tx.Error == nil && tx.RowsAffected > 0 {
								downgraded++
								if len(downgradedIDs) < 20 {
									downgradedIDs = append(downgradedIDs, u.ID)
								}
								rc.Logf("每小时超限降档: user=%d G:%d->%d used=%s",
									u.ID, from, from-1, tools.FlowAutoShow(float64(used)))
							}
						}
					}
				}
				// 写入当前快照, 过期 ~1.28h (dry-run 不刷快照, 保持下轮真实基线)
				if !rc.DryRun {
					rdb.SetEx(ctx, key, strconv.FormatInt(total, 10), 4600*time.Second)
				}
			}
			return nil
		})

	if downgraded > 0 {
		rc.Logf("每小时任务执行完成，过去一小时活跃用户数：%d，超%dG降档用户数：%d", activeUsers, limitGB, downgraded)
	}
	rc.SetSummary("hourly_active_users", activeUsers)
	rc.SetSummary("hourly_downgraded", downgraded)
	if len(downgradedIDs) > 0 {
		rc.SetSummary("hourly_downgraded_ids_sample", downgradedIDs)
	}
}

// nodeHeartbeatCheck 对齐 PHP NodeHeartbeatCheck:
// id>9 且 type=1 且心跳超时 (默认 259200s=3天) → type=0 + error 级 TG 告警 (clone 附主节点)。
func nodeHeartbeatCheck(ctx context.Context, rc *RunContext) {
	var timeoutSec int64 = 259200
	if cfg := config.GlobalConfig; cfg != nil && cfg.Jobs.Params.NodeHeartbeatOfflineSetTimeout > 0 {
		timeoutSec = int64(cfg.Jobs.Params.NodeHeartbeatOfflineSetTimeout)
	}
	now := timex.Unix()
	offlined := 0

	var batch []model.Node
	_ = database.DB.WithContext(ctx).
		Model(&model.Node{}).
		Select("id", "name", "node_heartbeat", "is_clone").
		Where("id > 9 AND type = 1").
		Order("id ASC").
		FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				node := &batch[i]
				hbAge := now - node.NodeHeartbeat
				if hbAge <= timeoutSec {
					continue
				}
				if rc.DryRun {
					offlined++ // 预览只计数不下线不告警
					continue
				}
				// 条件更新 (type=1 才改) 防与手动操作并发双写
				tx := database.DB.WithContext(ctx).
					Model(&model.Node{}).
					Where("id = ? AND type = 1", node.ID).
					Update("type", 0)
				if tx.Error != nil || tx.RowsAffected == 0 {
					continue
				}
				offlined++
				msg := fmt.Sprintf("ID: %d\n名称: %s\n心跳中断: %d小时",
					node.ID, node.Name, int64(math.Round(float64(hbAge)/3600)))
				if mainID := node.IsClone; mainID > 0 {
					var main model.Node
					mainErr := database.DB.WithContext(ctx).
						Select("id", "name").First(&main, mainID).Error
					if mainErr == nil {
						msg += fmt.Sprintf("\n(Clone 节点, 来自主节点 ID: %d 名称: %s)", main.ID, main.Name)
					} else {
						msg += fmt.Sprintf("\n(Clone 节点, 主节点 ID: %d 已不存在)", mainID)
					}
				}
				notify.Error("节点掉线", msg)
			}
			return nil
		})

	rc.SetSummary("heartbeat_offlined", offlined)
}
