// checkjob.go 对齐 PHP Job::CheckJob() (crontab `17 5 * * *`, 生产按日跑)。
//
// 临时表清理 → class_expire 批量降级 → 用户删除循环 (RadiusBan/过期通知打标/
// 四分支删除判定/两阶段删除/kill_user 级联) → RadiusBan 恢复循环。
// PHP 的 flock 文件锁由 runner 的 Redis 任务锁取代 (跨 PHP/Go 生效)。
// 日期哨兵 644447105 (≈1990-06-15): 解析出的时间戳 <= 该值视为零日期/永不过期。
package jobs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"spanel-go/internal/config"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/radius"
	"spanel-go/internal/pkg/timex"
	"spanel-go/internal/service"
)

// dateSentinel 零日期哨兵 (对齐 PHP 644447105)
const dateSentinel int64 = 644447105

func registerCheckJob() {
	Register(&Job{
		Name:     "checkjob",
		Title:    "用户过期/删除/Radius 联动 (两阶段删除 + kill 级联 + 封禁恢复)",
		Schedule: "17 5 * * *",
		Timeout:  2 * time.Hour,
		Fn:       checkJobFn,
	})
}

// candidateRow 候选用户 (raw 扫描规避 GORM datetime 零日期解析问题)
type candidateRow struct {
	ID              int32   `gorm:"column:id"`
	Email           string  `gorm:"column:email"`
	Passwd          string  `gorm:"column:passwd"`
	Enable          int8    `gorm:"column:enable"`
	Class           int32   `gorm:"column:class"`
	U               int64   `gorm:"column:u"`
	D               int64   `gorm:"column:d"`
	T               int64   `gorm:"column:t"`
	TransferEnable  int64   `gorm:"column:transfer_enable"`
	Money           float64 `gorm:"column:money"`
	ExpireIn        string  `gorm:"column:expire_in"`
	LastCheckInTime int64   `gorm:"column:last_check_in_time"`
	RegDate         string  `gorm:"column:reg_date"`
}

// strtotime PHP 语义: 解析失败返回 0 (零日期 '0000-00-00...' → 0)
func strtotime(s string) int64 {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, timex.Loc); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func checkJobFn(ctx context.Context, rc *RunContext) error {
	now := timex.Unix()
	nowStr := timex.DateStrNow()

	// 1. 临时表清理 (容忍表不存在的历史库差异)
	for tbl, cutoff := range map[string]int64{
		"ip":               now - 300,
		"unblockip":        now - 300,
		"blockip":          now - 86400,
		"telegram_session": now - 3600,
	} {
		delOrCountTable(rc, ctx, tbl, "datetime < ?", cutoff)
	}

	// 2. class_expire 过期批量降级 (单条 UPDATE, 字符串比较)
	if rc.DryRun {
		var n int64
		database.DB.WithContext(ctx).Table("user").
			Where("class > 0 AND class_expire < ?", nowStr).Count(&n)
		rc.SetSummary("would_class_expire_zero", n)
	} else {
		res := database.DB.WithContext(ctx).Exec(
			"UPDATE user SET class = 0 WHERE class > 0 AND class_expire < ?", nowStr)
		rc.SetSummary("class_expire_zero", res.RowsAffected)
	}

	// 3. 用户删除循环 (id 游标分批)
	var p config.JobsParams
	if cfg := config.GlobalConfig; cfg != nil {
		p = cfg.Jobs.Params
	}
	stats := map[string]int64{"candidates": 0, "radius_banned": 0, "expire_notified": 0,
		"kill_stage1_disabled": 0, "kill_stage1_waiting": 0, "killed": 0, "clawback": 0, "restored": 0}
	rdb := database.Redis

	const batchSize = 500
	cursor := int32(0)
	for {
		var rows []candidateRow
		err := database.DB.WithContext(ctx).Table("user").
			Select("id", "email", "passwd", "enable", "class", "u", "d", "t",
				"transfer_enable", "(money+0) AS money", "expire_in",
				"last_check_in_time", "reg_date").
			Where("id > ? AND class < 1 AND (expire_in < ? OR transfer_enable <= u + d OR enable = 0)",
				cursor, nowStr).
			Order("id ASC").Limit(batchSize).Scan(&rows).Error
		if err != nil || len(rows) == 0 {
			break
		}
		for i := range rows {
			u := &rows[i]
			cursor = u.ID
			stats["candidates"]++
			checkOneUser(ctx, rc, rdb, u, now, &p, stats)
		}
		if len(rows) < batchSize {
			break
		}
	}

	// 4. RadiusBan 恢复循环
	restoreRadiusBans(ctx, rc, now, stats)

	for k, v := range stats {
		rc.SetSummary(k, v)
	}
	rc.Logf("checkjob 完成: 候选 %d, 删除 %d, 封禁 %d, 恢复 %d",
		stats["candidates"], stats["killed"], stats["radius_banned"], stats["restored"])
	return nil
}

// checkOneUser 单个候选用户处置 (对齐 PHP CheckJob 用户循环)
func checkOneUser(ctx context.Context, rc *RunContext, rdb *redis.Client, u *candidateRow, now int64, p *config.JobsParams, stats map[string]int64) {
	expireTS := strtotime(u.ExpireIn)
	regTS := strtotime(u.RegDate)

	// ① RadiusBan 写入 + Radius 账号删除
	needBan := u.TransferEnable <= u.U+u.D || u.Enable == 0 ||
		(expireTS < now && expireTS > dateSentinel)
	if needBan {
		var banCount int64
		database.DB.WithContext(ctx).Table("radius_ban").
			Where("userid = ?", u.ID).Count(&banCount)
		if banCount == 0 {
			if rc.DryRun {
				stats["radius_banned"]++
			} else {
				if err := database.DB.WithContext(ctx).
					Exec("INSERT INTO radius_ban (userid) VALUES (?)", u.ID).Error; err == nil {
					stats["radius_banned"]++
				}
			}
			if !rc.DryRun {
				radius.Delete(ctx, u.Email)
			}
		}
	}

	// ② 过期通知去重打标 (通知邮件已停用, 仅保留标记语义兼容回切 PHP)
	notifyKey := fmt.Sprintf("ssp:user:%d:expire_notified", u.ID)
	if rdb != nil {
		if expireTS < now {
			if rdb.Get(ctx, notifyKey).Err() != nil && !rc.DryRun {
				_ = rdb.SetEx(ctx, notifyKey, "1", 30*86400*time.Second)
				stats["expire_notified"]++
			}
		} else if expireTS > now {
			_ = rdb.Del(ctx, notifyKey) // 续费复活清标记
		}
	}

	// ③ class_expire 二次降级 — 死代码 (候选限 class<1 且 §2 批量先跑), 不移植

	// ④ 四分支删除判定 (elseif 链, 先命中先用)
	iskill := false
	switch {
	case p.AccountExpireDeleteDays >= 0 &&
		expireTS > dateSentinel && expireTS+int64(p.AccountExpireDeleteDays)*86400 < now &&
		(now-expireTS) > int64(u.Money*2592000): // 余额折算: 1元=30天
		iskill = true
	case p.AutoCleanUncheckDays > 0 &&
		maxI64(u.LastCheckInTime, regTS)+int64(p.AutoCleanUncheckDays)*86400 < now &&
		u.Class == 0 && u.Money <= p.AutoCleanMinMoney:
		iskill = true
	case p.AutoCleanUnusedDays > 0 &&
		maxI64(u.T, regTS)+int64(p.AutoCleanUnusedDays)*86400 < now &&
		u.Class == 0 && u.Money <= p.AutoCleanMinMoney:
		iskill = true
	case u.T == 0 && u.U == 0 && u.D == 0 &&
		regTS+14*86400 < now && u.Class == 0 && u.Money <= 1:
		iskill = true
	}
	if !iskill {
		return
	}

	// ⑤⑥ 两阶段删除 (在服先禁用打标 → 标记期内等待 → 标记过期后删)
	killKey := fmt.Sprintf("ssp:user:%d:kill_pending", u.ID)
	// isServingLike 防御性守卫 (候选条件与其矛盾, 保留防并发修改)
	servingLike := u.Enable == 1 && expireTS > now && (u.U+u.D) < u.TransferEnable
	if servingLike {
		if rc.DryRun {
			stats["kill_stage1_disabled"]++
			return
		}
		_ = database.DB.WithContext(ctx).Exec("UPDATE user SET enable = 0 WHERE id = ?", u.ID)
		if rdb != nil {
			_ = rdb.SetEx(ctx, killKey, "1", 300*time.Second)
		}
		stats["kill_stage1_disabled"]++
		return
	}
	if rdb != nil && rdb.Get(ctx, killKey).Err() == nil {
		stats["kill_stage1_waiting"]++ // 禁用后 <300s, 等节点完成剔除
		return
	}

	// 返利回收 (实际回收才惩罚邀请人)
	if !rc.DryRun {
		if refID, ok := service.ClawbackLegacy(ctx, int64(u.ID)); ok {
			_ = database.DB.WithContext(ctx).Exec(
				"UPDATE user SET ban_times = ban_times + 1 WHERE id = ?", refID)
			stats["clawback"]++
		}
		killUser(ctx, u)
	}
	stats["killed"]++
}

// killUser 级联删除 (对齐 PHP User::kill_user; 保留表: Ip/DetectLog/TelegramSession/
// UnblockIp/TrafficLog 注释态不删)。表名按实际库对齐: disconnect_ip/user_token/
// ss_invite_code/ss_password_reset。
func killUser(ctx context.Context, u *candidateRow) {
	radius.Delete(ctx, u.Email)
	for tbl, cond := range map[string]string{
		"radius_ban":        fmt.Sprintf("userid = %d", u.ID),
		"disconnect_ip":     fmt.Sprintf("userid = %d", u.ID),
		"bought":            fmt.Sprintf("userid = %d", u.ID),
		"code":              fmt.Sprintf("userid = %d", u.ID),
		"link":              fmt.Sprintf("userid = %d", u.ID),
		"login_ip":          fmt.Sprintf("userid = %d", u.ID),
		"ss_invite_code":    fmt.Sprintf("user_id = %d", u.ID),
		"user_token":        fmt.Sprintf("user_id = %d", u.ID),
		"ss_password_reset": fmt.Sprintf("email = '%s'", escapeSQL(u.Email)),
		"user_action_log":   fmt.Sprintf("user_id = %d", u.ID),
	} {
		_ = database.DB.WithContext(ctx).Exec("DELETE FROM " + tbl + " WHERE " + cond).Error
	}
	// 工单树: 根工单 + 树下全部回复 (含管理员回复)
	_ = database.DB.WithContext(ctx).Exec(
		"DELETE FROM ticket WHERE rootid IN (SELECT t.id FROM (SELECT id FROM ticket WHERE userid = ? AND rootid = 0) t)", u.ID).Error
	_ = database.DB.WithContext(ctx).Exec(
		"DELETE FROM ticket WHERE id IN (SELECT t.id FROM (SELECT id FROM ticket WHERE userid = ? AND rootid = 0) t)", u.ID).Error
	_ = database.DB.WithContext(ctx).Exec("DELETE FROM user WHERE id = ?", u.ID).Error
}

// restoreRadiusBans RadiusBan 恢复循环: 用户恢复 (enable=1, 未过期或零日期, 流量未用尽)
// → 删封禁 + Radius 重建。
func restoreRadiusBans(ctx context.Context, rc *RunContext, now int64, stats map[string]int64) {
	type rbRow struct {
		ID     int32 `gorm:"column:id"`
		Userid int32 `gorm:"column:userid"`
	}
	var rbs []rbRow
	_ = database.DB.WithContext(ctx).Table("radius_ban").
		Select("id", "userid").Order("id ASC").Scan(&rbs).Error
	for i := range rbs {
		rb := &rbs[i]
		var u candidateRow
		err := database.DB.WithContext(ctx).Table("user").
			Select("id", "email", "passwd", "enable", "u", "d", "transfer_enable",
				"(money+0) AS money", "expire_in", "reg_date", "last_check_in_time", "t", "class").
			Where("id = ?", rb.Userid).Scan(&u).Error
		if err != nil || u.ID == 0 {
			// 孤儿封禁记录直接删
			if !rc.DryRun {
				_ = database.DB.WithContext(ctx).Exec("DELETE FROM radius_ban WHERE id = ?", rb.ID).Error
			}
			continue
		}
		expireTS := strtotime(u.ExpireIn)
		if u.Enable == 1 &&
			(expireTS > now || expireTS < dateSentinel) && // 未过期 或 零日期(永不过期)
			u.TransferEnable > u.U+u.D {
			if rc.DryRun {
				stats["restored"]++
				continue
			}
			_ = database.DB.WithContext(ctx).Exec("DELETE FROM radius_ban WHERE id = ?", rb.ID).Error
			radius.Add(ctx, u.ID, u.Email, u.Passwd)
			stats["restored"]++
		}
	}
}

func delOrCountTable(rc *RunContext, ctx context.Context, table, where string, args ...any) {
	db := database.DB.WithContext(ctx)
	if rc.DryRun {
		var n int64
		if err := db.Table(table).Where(where, args...).Count(&n).Error; err != nil {
			return // 表不存在等, 容忍
		}
		rc.SetSummary("would_delete_"+table, n)
		return
	}
	res := db.Exec("DELETE FROM "+table+" WHERE "+where, args...)
	if res.Error == nil {
		rc.SetSummary("deleted_"+table, res.RowsAffected)
	}
}

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func escapeSQL(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
