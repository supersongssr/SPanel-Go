// dbclean.go 对齐 PHP Job::DbClean() (crontab `4 5 * * *`)。
//
// 顺序锁死 (方案 2026-10-22): 聚合 → 水位推进 → 再清理。
// 统计失败则跳过 user_traffic_log / user_traffic_daily 两张流量表的清理
// (明细是唯一数据源, 宁可多留一天也不能丢) 并 TG 告警; 其余表清理不受影响。
// Go 新增: job_run_log 台账按 keep_days 顺带清理。
package jobs

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/notify"
	"spanel-go/internal/pkg/timex"
	"spanel-go/internal/service"
)

func registerDbClean() {
	Register(&Job{
		Name:     "dbclean",
		Title:    "数据库日志清理 (先聚合后清理; 流量统计失败则保护性跳过两张流量表)",
		Schedule: "4 5 * * *",
		Timeout:  time.Hour,
		Fn:       dbCleanFn,
	})
}

func dbCleanFn(ctx context.Context, rc *RunContext) error {
	now := timex.Unix()

	// ① 聚合 → ② 水位推进 (任一失败 → 两张流量表本轮不清);
	// dry-run 全程只读: 跳过聚合推进, 仅统计待删行数
	trafficAggOK := true
	if rc.DryRun {
		rc.Logf("dry-run: 跳过流量聚合与水位推进, 仅统计待删行数")
	} else if err := service.AggregateRecentDetail(2); err != nil {
		trafficAggOK = false
		rc.Logf("[UserTrafficRollup] dbclean aggregate fail: %v", err)
		notify.Send("用户流量日汇总失败, 本轮跳过 user_traffic_log/user_traffic_daily 清理: " + err.Error())
	} else if err := service.PushTotal(); err != nil {
		trafficAggOK = false
		rc.Logf("[UserTrafficRollup] dbclean pushTotal fail: %v", err)
		notify.Send("用户流量日汇总失败, 本轮跳过 user_traffic_log/user_traffic_daily 清理: " + err.Error())
	}

	var params config.JobsParams
	if cfg := config.GlobalConfig; cfg != nil {
		params = cfg.Jobs.Params
	}

	// 节点日志: 3 天窗口
	delOrCount(rc, ctx, "ss_node_info", "log_time < ?", now-3*86400)
	delOrCount(rc, ctx, "ss_node_online_log", "log_time < ?", now-3*86400)

	if trafficAggOK {
		// 明细: 保留窗口可配 (默认 24h), 统计成功后才清理
		if params.TrafficlogDetailKeepHours > 0 {
			delOrCount(rc, ctx, "user_traffic_log", "log_time < ?",
				now-int64(params.TrafficlogDetailKeepHours)*3600)
		}
		// 日汇总: 默认 90 天; <=0 永久
		if params.UserTrafficDailyDays > 0 {
			delOrCount(rc, ctx, "user_traffic_daily", "day < ?",
				timex.Now().AddDate(0, 0, -params.UserTrafficDailyDays).Format("2006-01-02"))
		}
	} else {
		rc.SetSummary("traffic_tables_protected", true)
	}

	delOrCount(rc, ctx, "detect_log", "datetime < ?", now-3*86400)
	delOrCount(rc, ctx, "speedtest", "datetime < ?", now-3*86400)
	delOrCount(rc, ctx, "email_verify", "expire_in < ?", now-3*86400)

	// 任务台账 (Go 新增表)
	if params.JobRunLogKeepDays > 0 {
		delOrCount(rc, ctx, "job_run_log", "started_at < ?",
			timex.Now().AddDate(0, 0, -params.JobRunLogKeepDays))
	}

	if !rc.DryRun {
		// storage/*.png (speedtest 残图; 目录不存在则跳过)
		if matches, _ := filepath.Glob("storage/*.png"); len(matches) > 0 {
			removed := 0
			for _, m := range matches {
				if err := os.Remove(m); err == nil {
					removed++
				}
			}
			rc.SetSummary("png_removed", removed)
		}
		notify.Send("姐姐姐姐，数据库被清理了，感觉身体被掏空了呢~")
	}
	rc.Logf("db clean done :)")
	return nil
}

// delOrCount 删除 (dry-run 时只统计待删行数); 结果打印并计入台账 summary。
func delOrCount(rc *RunContext, ctx context.Context, table, where string, args ...any) {
	db := database.DB.WithContext(ctx)
	if rc.DryRun {
		var n int64
		if err := db.Table(table).Where(where, args...).Count(&n).Error; err != nil {
			rc.Logf("count %s failed: %v", table, err)
			return
		}
		rc.SetSummary("would_delete_"+table, n)
		rc.Logf("待删除 %s: %d 行", table, n)
		return
	}
	res := db.Exec("DELETE FROM "+table+" WHERE "+where, args...)
	if res.Error != nil {
		rc.Logf("delete %s failed: %v", table, res.Error)
		rc.SetSummary("delete_error_"+table, res.Error.Error())
		return
	}
	rc.SetSummary("deleted_"+table, res.RowsAffected)
	rc.Logf("已删除 %s: %d 行", table, res.RowsAffected)
}
