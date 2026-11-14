// mailjob.go 对齐 PHP DailyMail::sendDailyMail (crontab `22 4 * * *`)。
//
// 给 sendDailyMail=1 的用户发"每日流量报告+公告"邮件; 汇总 TG (签到人数+今日总流量)。
// PHP 的 Google Analytics 依赖收敛为直接 SQL (getTodayCheckinUser 同口径)。
package jobs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/mailer"
	"spanel-go/internal/pkg/notify"
	"spanel-go/internal/pkg/timex"
	"spanel-go/internal/pkg/tools"
)

func registerSendDiaryMail() {
	Register(&Job{
		Name:     "sendDiaryMail",
		Title:    "用户每日流量报告邮件 + 公告 (订阅制 sendDailyMail=1) + TG 汇总",
		Schedule: "22 4 * * *",
		Timeout:  1 * time.Hour,
		Fn:       sendDiaryMailFn,
	})
}

func sendDiaryMailFn(ctx context.Context, rc *RunContext) error {
	// 公告拼接 (倒序, 含 "Links" 的排除 — 对齐 PHP)
	var anns []struct {
		Content string `gorm:"column:content"`
	}
	_ = database.DB.WithContext(ctx).Table("ann").
		Select("content").Order("id DESC").Scan(&anns).Error
	var annBuf strings.Builder
	for _, a := range anns {
		if !strings.Contains(a.Content, "Links") {
			annBuf.WriteString(a.Content + "<br><br>")
		}
	}

	// 用户遍历 + 昨日用量汇总
	type row struct {
		ID            int32  `gorm:"column:id"`
		Email         string `gorm:"column:email"`
		D             int64  `gorm:"column:d"`
		LastDayT      int64  `gorm:"column:last_day_t"`
		SendDailyMail int32  `gorm:"column:sendDailyMail"`
	}
	sent, skipped := int64(0), int64(0)
	var lastdayTotal int64
	appName := "SPanel"
	var batch []row
	_ = database.DB.WithContext(ctx).Table("user").
		Select("id", "email", "d", "last_day_t", "sendDailyMail").
		Order("id ASC").
		FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				u := &batch[i]
				lastdayTotal += u.D - u.LastDayT
				if u.SendDailyMail != 1 {
					skipped++
					continue
				}
				if rc.DryRun {
					sent++
					continue
				}
				lastdayMB := float64(u.D-u.LastDayT) / 1024 / 1024
				html := "<p>下面是系统中目前的公告:</p><br><br>" + annBuf.String() +
					"<br><br><p>昨日使用流量: " + fmt.Sprintf("%.2f", lastdayMB) + " MB</p><br><p>晚安！</p>"
				if err := mailer.Send(mailer.Mail{
					To:       u.Email,
					Subject:  appName + "-每日流量报告以及公告",
					HTMLBody: html,
				}); err != nil {
					rc.Logf("Send daily mail to user %d failed: %v", u.ID, err)
					skipped++
					continue
				}
				sent++
			}
			return nil
		})

	// TG 汇总 (签到人数 = 近 24h last_check_in_time)
	var checkins int64
	database.DB.WithContext(ctx).Table("user").
		Where("last_check_in_time > ?", timex.Unix()-24*3600).Count(&checkins)
	rc.SetSummary("mail_sent", sent)
	rc.SetSummary("mail_skipped", sent+skipped-sent)
	rc.SetSummary("checkin_users", checkins)
	rc.SetSummary("lastday_total_bytes", lastdayTotal)
	if !rc.DryRun {
		notify.Send("各位老爷少奶奶，我来为大家报告一下系统今天的运行状况哈~\n" +
			"今日签到人数:" + fmt.Sprintf("%d", checkins) + "\n" +
			"今日使用总流量:" + tools.FlowAutoShow(float64(lastdayTotal)) + "\n" +
			"晚安~")
	}
	rc.Logf("sendDiaryMail: 发送 %d, 跳过 %d", sent, skipped)
	return nil
}
