// radiusjob.go 对齐 PHP SyncRadius::synclogin/syncvpn (默认不排程, enable_radius 门控)。
//
// synclogin: radpostauth 近 60s 认证记录 → 记 10000B 保活流量 (node_id=1) + t 刷新;
// syncvpn:   radacct 近 60s 停止会话 → acctinputoctets/acctoutputoctets 入账 (node_id=2)。
package jobs

import (
	"context"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/radius"
	"spanel-go/internal/pkg/timex"
	"spanel-go/internal/pkg/tools"
)

func registerRadiusJobs() {
	Register(&Job{
		Name:  "radiusSyncLogin",
		Title: "Radius 登录保活记账 (radpostauth 近 60s 认证 → 10000B)",
		Fn:    radiusSyncLoginFn,
	})
	Register(&Job{
		Name:  "radiusSyncVpn",
		Title: "Radius VPN 流量入账 (radacct 近 60s 停止会话 octets)",
		Fn:    radiusSyncVpnFn,
	})
}

func radiusSyncLoginFn(ctx context.Context, rc *RunContext) error {
	if !radius.Enabled() {
		rc.Logf("enable_radius=false, 跳过")
		return nil
	}
	rdb := radius.DB()
	if rdb == nil {
		return nil
	}
	// email → userid 映射
	type uRow struct {
		ID    int32  `gorm:"column:id"`
		Email string `gorm:"column:email"`
	}
	var users []uRow
	_ = database.DB.WithContext(ctx).Table("user").Select("id", "email").Scan(&users).Error
	userbox := map[string]int32{}
	for i := range users {
		userbox[radius.GetUserName(users[i].Email)] = users[i].ID
	}

	type authRow struct {
		Username string `gorm:"column:username"`
	}
	var logs []authRow
	_ = rdb.WithContext(ctx).Table("radpostauth").
		Select("username").
		Where("authdate < ? AND authdate > ?", timex.DateStrNow(), timex.DateStr(timex.Now().Add(-60*time.Second))).
		Scan(&logs).Error

	now := timex.Unix()
	processed := int64(0)
	for i := range logs {
		uid, ok := userbox[logs[i].Username]
		if !ok {
			continue
		}
		if rc.DryRun {
			processed++
			continue
		}
		_ = database.DB.WithContext(ctx).Exec(
			`INSERT INTO user_traffic_log (user_id, u, d, node_id, rate, traffic, log_time)
			 VALUES (?, 0, 10000, 1, 1, ?, ?)`, uid, tools.FlowAutoShow(10000), now).Error
		_ = database.DB.WithContext(ctx).Exec(
			"UPDATE user SET t = ?, d = d + 10000 WHERE id = ?", now, uid).Error
		processed++
	}
	rc.SetSummary("processed", processed)
	rc.Logf("radiusSyncLogin: %d 条认证记录", processed)
	return nil
}

func radiusSyncVpnFn(ctx context.Context, rc *RunContext) error {
	if radiusEnabledConfig() == "" {
		rc.Logf("radius_db_host 未配置, 跳过")
		return nil
	}
	rdb := radius.DB()
	if rdb == nil {
		return nil
	}
	type uRow struct {
		ID    int32  `gorm:"column:id"`
		Email string `gorm:"column:email"`
	}
	var users []uRow
	_ = database.DB.WithContext(ctx).Table("user").Select("id", "email").Scan(&users).Error
	userbox := map[string]int32{}
	for i := range users {
		userbox[radius.GetUserName(users[i].Email)] = users[i].ID
	}

	type acctRow struct {
		Username string `gorm:"column:username"`
		AcctIn   int64  `gorm:"column:acctinputoctets"`
		AcctOut  int64  `gorm:"column:acctoutputoctets"`
	}
	var logs []acctRow
	_ = rdb.WithContext(ctx).Table("radacct").
		Select("username", "acctinputoctets", "acctoutputoctets").
		Where("acctstoptime IS NOT NULL AND acctstoptime < ? AND acctstoptime > ?",
			timex.DateStrNow(), timex.DateStr(timex.Now().Add(-60*time.Second))).
		Scan(&logs).Error

	now := timex.Unix()
	processed := int64(0)
	var totalIn, totalOut int64
	for i := range logs {
		uid, ok := userbox[logs[i].Username]
		if !ok {
			continue
		}
		in, out := logs[i].AcctIn, logs[i].AcctOut
		totalIn += in
		totalOut += out
		if rc.DryRun {
			processed++
			continue
		}
		_ = database.DB.WithContext(ctx).Exec(
			`INSERT INTO user_traffic_log (user_id, u, d, node_id, rate, traffic, log_time)
			 VALUES (?, ?, ?, 2, 1, ?, ?)`, uid, in, out, tools.FlowAutoShow(float64(in+out)), now).Error
		_ = database.DB.WithContext(ctx).Exec(
			"UPDATE user SET t = ?, u = u + ?, d = d + ? WHERE id = ?", now, in, out, uid).Error
		processed++
	}
	rc.SetSummary("processed", processed)
	rc.SetSummary("acct_in", totalIn)
	rc.SetSummary("acct_out", totalOut)
	rc.Logf("radiusSyncVpn: %d 个停止会话, in=%s out=%s", processed,
		tools.FlowAutoShow(float64(totalIn)), tools.FlowAutoShow(float64(totalOut)))
	return nil
}

// radiusEnabledConfig host 检查 (与 radius.DB 同源)
func radiusEnabledConfig() string {
	if cfg := config.GlobalConfig; cfg != nil && cfg.Radius.Enable && cfg.Radius.Host != "" {
		return cfg.Radius.Host
	}
	return ""
}
