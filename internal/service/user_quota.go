// user_quota.go 用户侧配额处置 (对齐 PHP Job.php 中 DailyJob 的用户处理步骤)。
//
// 两类规则 (方案 .plans/2026-10-21):
//
//	耗尽类: 无豁免 (含0/1组), transfer_enable <= u+d → enable=0 + warming 自助激活引导
//	限额类: 0/1组豁免, 超额降一档不禁用 (小时/日/周期三档)
//
// PHP 的 UserActionLogger 审计由 job_run_log.summary 承接 (方案 §6.6)。
package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/notify"
	"spanel-go/internal/pkg/timex"
	"spanel-go/internal/pkg/tools"
)

// quotaCtx 配额处置的输出收集 (对齐 PHP echo + 审计)
type quotaCtx struct {
	ctx    context.Context
	dryRun bool
	logf   func(format string, args ...any)
	counts map[string]int
	ids    map[string][]int32 // 审计承接: 每类受影响用户 id (前 20)
}

func newQuotaCtx(ctx context.Context, dryRun bool, logf func(string, ...any)) *quotaCtx {
	return &quotaCtx{ctx: ctx, dryRun: dryRun, logf: logf,
		counts: map[string]int{}, ids: map[string][]int32{}}
}

func (q *quotaCtx) hit(kind string, id int32) {
	q.counts[kind]++
	if len(q.ids[kind]) < 20 {
		q.ids[kind] = append(q.ids[kind], id)
	}
}

func (q *quotaCtx) Logf(format string, args ...any) {
	if q.logf != nil {
		q.logf(format, args...)
	}
}

// Summary 输出计数摘要 (job_run_log 用)
func (q *quotaCtx) Summary() map[string]any {
	out := map[string]any{}
	for k, v := range q.counts {
		out[k] = v
	}
	for k, v := range q.ids {
		if len(v) > 0 {
			out[k+"_ids_sample"] = v
		}
	}
	return out
}

// DisableExhaustedUsers 耗尽类: 总配额用尽 → enable=0 + warming 引导。
// id>10 保护系统端口账户; transfer_enable>0 跳过未配置配额的特殊账户。
func DisableExhaustedUsers(ctx context.Context, dryRun bool, logf func(string, ...any)) *quotaCtx {
	q := newQuotaCtx(ctx, dryRun, logf)
	var batch []model.User
	_ = database.DB.WithContext(ctx).
		Model(&model.User{}).
		Select("id", "u", "d", "transfer_enable").
		Where("id > 10 AND enable = 1 AND transfer_enable > 0 AND transfer_enable <= u + d").
		Order("id ASC").
		FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				u := &batch[i]
				if q.dryRun {
					q.hit("exhausted_disabled", u.ID)
					q.Logf("流量耗尽禁用(预览): user=%d quota=%s", u.ID, tools.FlowAutoShow(float64(u.TransferEnable)))
					continue
				}
				err := database.DB.WithContext(ctx).Model(&model.User{}).
					Where("id = ?", u.ID).
					Updates(map[string]any{
						"enable":  0,
						"warming": "本期流量已全部用完，账号暂停使用。购买套餐或签到获得流量后，在面板点击「激活账号」即可恢复。",
					}).Error
				if err == nil {
					q.hit("exhausted_disabled", u.ID)
					q.Logf("流量耗尽禁用: user=%d quota=%s", u.ID, tools.FlowAutoShow(float64(u.TransferEnable)))
				}
			}
			return nil
		})
	if n := q.counts["exhausted_disabled"]; n > 0 {
		notify.Warning("流量耗尽禁用用户", fmt.Sprintf("共 %d 个用户总量配额用尽, 已禁用(可自助激活)", n))
		q.Logf("流量耗尽禁用用户数: %d", n)
	}
	return q
}

// DowngradeDailyTrafficOverUsers 日限额类: Redis 快照 traffic_lastday (TTL 86400)
// 与本轮差值 > 日限额 → 静默降一档 (0/1组豁免, 不禁用不写 warming)。
func DowngradeDailyTrafficOverUsers(ctx context.Context, dryRun bool, logf func(string, ...any)) *quotaCtx {
	q := newQuotaCtx(ctx, dryRun, logf)
	cfg := config.GlobalConfig
	if cfg == nil || !cfg.Jobs.Params.UserRateLimitEnabled {
		q.Logf("小时/日限额模块已关闭 (user_rate_limit_enabled), 跳过")
		return q
	}
	limitGB := cfg.Jobs.Params.UserDailyTrafficLimitGB
	if limitGB <= 0 {
		q.Logf("每日流量限额已关闭 (user_daily_traffic_limit=%d), 跳过", limitGB)
		return q
	}
	limitBytes := int64(limitGB) << 30
	rdb := database.Redis
	if rdb == nil {
		q.Logf("Redis 不可用, 跳过每日限额降档")
		return q
	}
	now := timex.Unix()
	active := 0
	var info []string

	var batch []model.User
	_ = database.DB.WithContext(ctx).
		Model(&model.User{}).
		Select("id", "u", "d", "node_group").
		Where("enable = 1 AND node_group > 1 AND t > ?", now-24*3600).
		Order("id ASC").
		FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				u := &batch[i]
				active++
				total := u.U + u.D
				key := fmt.Sprintf("ssp:user:%d:traffic_lastday", u.ID)
				if lastStr, err := rdb.Get(ctx, key).Result(); err == nil {
					if last, perr := strconv.ParseInt(lastStr, 10, 64); perr == nil {
						used := total - last
						if used > limitBytes {
							from := u.NodeGroup
							if q.dryRun {
								q.hit("daily_downgraded", u.ID)
								q.Logf("每日超限降档(预览): user=%d G:%d->%d used=%s", u.ID, from, from-1, tools.FlowAutoShow(float64(used)))
							} else {
								tx := database.DB.WithContext(ctx).
									Model(&model.User{}).
									Where("id = ? AND node_group > 1", u.ID).
									Update("node_group", gorm.Expr("node_group - 1"))
								if tx.Error == nil && tx.RowsAffected > 0 {
									q.hit("daily_downgraded", u.ID)
									q.Logf("每日超限降档: user=%d G:%d->%d used=%s", u.ID, from, from-1, tools.FlowAutoShow(float64(used)))
								}
							}
							if len(info) < 20 {
								info = append(info, fmt.Sprintf("User%dG:%d->%dUsed___%dG___;", u.ID, from, from-1, int64(used)>>30))
							}
						}
					}
				}
				if !q.dryRun {
					rdb.SetEx(ctx, key, strconv.FormatInt(total, 10), 86400*time.Second)
				}
			}
			return nil
		})

	q.counts["daily_active_users"] = active
	notify.Info("每日任务完成", fmt.Sprintf("过去一天活跃用户数：%d，超%dG降档用户数：%d", active, limitGB, q.counts["daily_downgraded"]))
	if q.counts["daily_downgraded"] > 0 {
		notify.Warning("超流量自动降档用户", joinStrings(info))
		q.Logf("每天任务执行完成，过去一天活跃用户数：%d，超%dG降档用户数：%d", active, limitGB, q.counts["daily_downgraded"])
	}
	return q
}

// ResetUserTrafficCycle 周期记账 (不受 user_cycle_quota_enabled 门控, 必须始终执行):
// renew_time 到期 → u+=d、d=0、transfer_limit=class×10GiB、renew_time 顺延 class×10 天。
func ResetUserTrafficCycle(ctx context.Context, dryRun bool, logf func(string, ...any)) *quotaCtx {
	q := newQuotaCtx(ctx, dryRun, logf)
	now := timex.Unix()
	var batch []model.User
	_ = database.DB.WithContext(ctx).
		Model(&model.User{}).
		Select("id", "u", "d", "class").
		Where("enable > 0 AND class > 0 AND renew_time < ?", now).
		Order("id ASC").
		FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				u := &batch[i]
				if q.dryRun {
					q.hit("cycle_reset", u.ID)
					continue
				}
				newLimit := int64(u.Class) * 10 << 30
				err := database.DB.WithContext(ctx).Model(&model.User{}).
					Where("id = ?", u.ID).
					Updates(map[string]any{
						"u":              u.U + u.D,
						"d":              0,
						"transfer_limit": newLimit,
						"renew_time":     now + int64(u.Class)*10*24*3600,
					}).Error
				if err == nil {
					q.hit("cycle_reset", u.ID)
				}
			}
			return nil
		})
	q.Logf("周期记账重置: %d 个用户", q.counts["cycle_reset"])
	return q
}

// DowngradeTotalTrafficOverUsers 周期限额类: d > transfer_limit → 降一档 + 补一档额度
// + score-1 + ban_times+1 (0/1组豁免; user_cycle_quota_enabled 默认 true)。
func DowngradeTotalTrafficOverUsers(ctx context.Context, dryRun bool, logf func(string, ...any)) *quotaCtx {
	q := newQuotaCtx(ctx, dryRun, logf)
	cfg := config.GlobalConfig
	if cfg != nil && !config.BoolOrDefault(cfg.Jobs.Params.UserCycleQuotaEnabled, true) {
		q.Logf("周期配额模块已关闭 (user_cycle_quota_enabled), 跳过")
		return q
	}
	var batch []model.User
	_ = database.DB.WithContext(ctx).
		Model(&model.User{}).
		Select("id", "class", "node_group").
		Where("node_group > 1 AND enable > 0 AND class > 0 AND d > transfer_limit").
		Order("id ASC").
		FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				u := &batch[i]
				if q.dryRun {
					q.hit("total_downgraded", u.ID)
					continue
				}
				// score-1, ban_times+1, 降一档, 补一档额度 (条件更新防并发双降)
				err := database.DB.WithContext(ctx).Model(&model.User{}).
					Where("id = ? AND node_group > 1", u.ID).
					Updates(map[string]any{
						"score":          gorm.Expr("score - 1"),
						"ban_times":      gorm.Expr("ban_times + 1"),
						"node_group":     gorm.Expr("node_group - 1"),
						"transfer_limit": gorm.Expr("transfer_limit + ?", int64(u.Class)*10<<30),
					}).Error
				if err == nil {
					q.hit("total_downgraded", u.ID)
				}
			}
			return nil
		})
	q.Logf("周期下行超限降档: %d 个用户", q.counts["total_downgraded"])
	return q
}

// DisableUnusedUsers 禁用超 32 天未使用用户 (id>10, class>0, 注册超 1 月)。
func DisableUnusedUsers(ctx context.Context, dryRun bool, logf func(string, ...any)) *quotaCtx {
	q := newQuotaCtx(ctx, dryRun, logf)
	dateCheck := timex.Now().AddDate(0, -1, 0).Format("2006-01-02 15:04:05")
	nouseTime := timex.Unix() - 32*86400
	var batch []model.User
	_ = database.DB.WithContext(ctx).
		Model(&model.User{}).
		Select("id").
		Where("id > 10 AND enable = 1 AND class > 0 AND t < ? AND reg_date < ?", nouseTime, dateCheck).
		Order("id ASC").
		FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				u := &batch[i]
				if q.dryRun {
					q.hit("unused_disabled", u.ID)
					continue
				}
				err := database.DB.WithContext(ctx).Model(&model.User{}).
					Where("id = ?", u.ID).
					Updates(map[string]any{
						"enable": 0,
						"warming": timex.DateStrNow() +
							"账号超过1个月未使用，系统启用账号保护。您可以自助解除保护",
					}).Error
				if err == nil {
					q.hit("unused_disabled", u.ID)
				}
			}
			return nil
		})
	q.Logf("超32天未使用禁用: %d 个用户", q.counts["unused_disabled"])
	return q
}

// DisableNegativeMoneyUsers 余额<0 用户处置: enable=0 + 降一档 + ban_times+=class + score-1。
func DisableNegativeMoneyUsers(ctx context.Context, dryRun bool, logf func(string, ...any)) *quotaCtx {
	q := newQuotaCtx(ctx, dryRun, logf)
	var batch []model.User
	_ = database.DB.WithContext(ctx).
		Model(&model.User{}).
		Select("id", "class", "node_group").
		Where("money < 0 AND enable = 1").
		Order("id ASC").
		FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				u := &batch[i]
				if q.dryRun {
					q.hit("negative_disabled", u.ID)
					continue
				}
				updates := map[string]any{
					"enable":    0,
					"warming":   timex.DateStrNow() + "账号余额异常，系统启用账号保护。请检查您的余额",
					"ban_times": gorm.Expr("ban_times + ?", u.Class),
					"score":     gorm.Expr("score - 1"),
					// PHP: node_group>1 才 -1, 0/1 保持 (IF 防御 0 组被拍成 1)
					"node_group": gorm.Expr("IF(node_group > 1, node_group - 1, node_group)"),
				}
				err := database.DB.WithContext(ctx).Model(&model.User{}).
					Where("id = ?", u.ID).
					Updates(updates).Error
				if err == nil {
					q.hit("negative_disabled", u.ID)
				}
			}
			return nil
		})
	q.Logf("余额为负禁用: %d 个用户", q.counts["negative_disabled"])
	return q
}

// AddActiveScore 活跃积分: 近 24h 使用的 group>1、money>0、class>0 用户 score+1。
func AddActiveScore(ctx context.Context, dryRun bool, logf func(string, ...any)) *quotaCtx {
	q := newQuotaCtx(ctx, dryRun, logf)
	last24 := timex.Unix() - 24*3600
	var ids []int32
	_ = database.DB.WithContext(ctx).
		Model(&model.User{}).
		Select("id").
		Where("node_group > 1 AND enable > 0 AND money > 0 AND class > 0 AND t > ?", last24).
		Order("id ASC").
		FindInBatches(&ids, 500, func(_ *gorm.DB, _ int) error {
			if !q.dryRun {
				for _, id := range ids {
					_ = database.DB.WithContext(ctx).Model(&model.User{}).
						Where("id = ?", id).
						Update("score", gorm.Expr("score + 1")).Error
				}
			}
			q.counts["active_scored"] += len(ids)
			return nil
		})
	q.Logf("活跃积分 +1: %d 个用户", q.counts["active_scored"])
	return q
}

// ClawbackInvites 邀请返利回收: 余额<0 且 ref_by≠0 且 score<64 →
// clawbackLegacy (仅旧口径未充值用户), 实际回收则邀请人 ban_times+1、用户 ref_by=0 + 禁用。
func ClawbackInvites(ctx context.Context, dryRun bool, logf func(string, ...any)) *quotaCtx {
	q := newQuotaCtx(ctx, dryRun, logf)
	var batch []model.User
	_ = database.DB.WithContext(ctx).
		Model(&model.User{}).
		Select("id", "ref_by").
		Where("money < 0 AND ref_by != 0 AND score < 64").
		Order("id ASC").
		FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				u := &batch[i]
				if q.dryRun {
					// 预览口径: 仅统计候选 (是否实际回收取决于 payback 记录, 逐条查代价大)
					q.hit("invite_clawback", u.ID)
					continue
				}
				refID, ok := ClawbackLegacy(ctx, int64(u.ID))
				if !ok {
					continue
				}
				_ = database.DB.WithContext(ctx).Model(&model.User{}).
					Where("id = ?", refID).
					Update("ban_times", gorm.Expr("ban_times + 1")).Error
				_ = database.DB.WithContext(ctx).Model(&model.User{}).
					Where("id = ?", u.ID).
					Updates(map[string]any{"ref_by": 0, "enable": 0}).Error
				q.hit("invite_clawback", u.ID)
			}
			return nil
		})
	q.Logf("邀请返利回收: %d 个用户", q.counts["invite_clawback"])
	return q
}

// DailySnapshot 每日快照: 近 48h 活跃用户 last_day_t=d、rss/ips 昨日快照 (按分组 1-8)。
func DailySnapshot(ctx context.Context, dryRun bool, logf func(string, ...any)) *quotaCtx {
	q := newQuotaCtx(ctx, dryRun, logf)
	checkTime := timex.Unix() - 48*3600
	total := 0
	for group := 1; group < 9; group++ {
		groupCount := 0
		var ids []int32
		_ = database.DB.WithContext(ctx).
			Model(&model.User{}).
			Select("id").
			Where("enable > 0 AND class > 0 AND t > ? AND node_group = ?", checkTime, group).
			Order("id ASC").
			FindInBatches(&ids, 500, func(_ *gorm.DB, _ int) error {
				if !q.dryRun {
					// 单条 UPDATE 走 SQL 层 (last_day_t=d 列间引用, 避免 N+1 读改写)
					_ = database.DB.WithContext(ctx).Exec(
						`UPDATE user SET last_day_t = d, rss_count_lastday = rss_count, rss_ips_lastday = rss_ips_count
						 WHERE id IN (?)`, ids).Error
				}
				groupCount += len(ids)
				return nil
			})
		total += groupCount
		q.Logf("分组 %d 处理完成，共处理 %d 个用户", group, groupCount)
	}
	q.counts["snapshot_users"] = total
	q.Logf("所有分组处理完成，总计处理 %d 个用户", total)
	return q
}

func joinStrings(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += "\n"
		}
		out += s
	}
	return out
}
