// user_traffic_rollup.go 用户流量三层漏斗 (对齐 PHP app/Services/UserTrafficRollup.php)。
//
//	user_traffic_log (上报间隔粒度明细, 保留 24h)
//	  │ ① AggregateRecentDetail: 逐自然日 INSERT..SELECT..ON DUPLICATE KEY 幂等聚合
//	  ▼
//	user_traffic_daily (用户×节点×日, 保留 90 天)
//	  │ ② PushTotal: 闭合日增量按水位 synced_day 累加推进 (幂等/漏跑自愈)
//	  ▼
//	user_node_traffic_total (用户×节点 全期累计缓存)
//
// 自然日日界由 timex (Asia/Shanghai) 算好后以字面量传入 SQL, 不经 FROM_UNIXTIME,
// 无 DB 会话时区依赖。MySQL 5.7 生产库 → ON DUPLICATE 用 VALUES() 语法 (与 PHP 一致)。
package service

import (
	"fmt"

	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/timex"
)

// AggregateRecentDetail ① 明细 → 日汇总: 对最近 days 个自然日 (含今日) 逐日幂等聚合。
// 今日为"进行中"值, 每次覆盖为截至今刻; 窗口 2 天即覆盖 24h 保留窗口内现存全部明细。
// 由 HourlyJob (安全网) 与 DbClean (主路径) 调用。
func AggregateRecentDetail(days int) error {
	if database.DB == nil {
		return fmt.Errorf("database not initialized")
	}
	if days < 1 {
		days = 1
	}
	todayStart := timex.TodayStart()
	for i := 0; i < days; i++ {
		dayStart := todayStart.AddDate(0, 0, -i)
		dayEnd := dayStart.AddDate(0, 0, 1)
		day := dayStart.Format("2006-01-02")

		err := database.DB.Exec(
			`INSERT INTO user_traffic_daily (user_id, node_id, day, u, d)
			 SELECT user_id, node_id, ?, SUM(u), SUM(d)
			 FROM user_traffic_log
			 WHERE user_id > 0 AND log_time >= ? AND log_time < ?
			 GROUP BY user_id, node_id
			 HAVING SUM(u) + SUM(d) > 0
			 ON DUPLICATE KEY UPDATE u = VALUES(u), d = VALUES(d)`,
			day, dayStart.Unix(), dayEnd.Unix(),
		).Error
		if err != nil {
			return fmt.Errorf("aggregate day %s: %w", day, err)
		}
	}
	return nil
}

// PushTotal ② 日汇总 → 累计表: 按 per-(user,node) 水位 synced_day 增量推进至昨日闭合日。
// 重复执行时增量集为空 → 零效果 (幂等); DbClean 漏跑多天时一次补齐。
func PushTotal() error {
	if database.DB == nil {
		return fmt.Errorf("database not initialized")
	}
	closedDay := timex.Now().AddDate(0, 0, -1).Format("2006-01-02")
	return database.DB.Exec(
		`INSERT INTO user_node_traffic_total (user_id, node_id, u, d, last_use_day, synced_day)
		 SELECT t.user_id, t.node_id, SUM(t.u), SUM(t.d), MAX(t.day), ?
		 FROM user_traffic_daily t
		 LEFT JOIN user_node_traffic_total g
		   ON g.user_id = t.user_id AND g.node_id = t.node_id
		 WHERE t.day <= ?
		   AND (g.synced_day IS NULL OR t.day > g.synced_day)
		 GROUP BY t.user_id, t.node_id
		 ON DUPLICATE KEY UPDATE
		   u = u + VALUES(u),
		   d = d + VALUES(d),
		   last_use_day = GREATEST(last_use_day, VALUES(last_use_day)),
		   synced_day = VALUES(synced_day)`,
		closedDay, closedDay,
	).Error
}

// CleanTrafficDailyExpired daily 过期行清理 (DbClean 调用); keepDays<=0 表示永久保留。
func CleanTrafficDailyExpired(keepDays int) (int64, error) {
	if database.DB == nil || keepDays <= 0 {
		return 0, nil
	}
	cutoff := timex.Now().AddDate(0, 0, -keepDays).Format("2006-01-02")
	res := database.DB.Exec("DELETE FROM user_traffic_daily WHERE day < ?", cutoff)
	return res.RowsAffected, res.Error
}
