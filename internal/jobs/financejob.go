// financejob.go 对齐 PHP FinanceMail::sendFinanceMail_day/_week/_month (默认不排程)。
//
// 统计 code(type=-1 isused=1) + yft_order_info(state=1, 存在时) 收入, HTML 表格
// 发全部管理员; finance_public=true 时 TG 播报。
package jobs

import (
	"context"
	"fmt"
	"strings"

	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/mailer"
	"spanel-go/internal/pkg/notify"
)

func registerFinanceJobs() {
	Register(&Job{
		Name:  "financeMailDay",
		Title: "财务日报 (昨日充值明细 → 管理员邮件)",
		Fn:    financeMailFn("day"),
	})
	Register(&Job{
		Name:  "financeMailWeek",
		Title: "财务周报 (上周充值汇总 → 管理员邮件)",
		Fn:    financeMailFn("week"),
	})
	Register(&Job{
		Name:  "financeMailMonth",
		Title: "财务月报 (上月充值汇总 → 管理员邮件)",
		Fn:    financeMailFn("month"),
	})
}

// incomeRow 收入明细
type incomeRow struct {
	Number   float64 `gorm:"column:number"`
	Userid   int32   `gorm:"column:userid"`
	UserName string  `gorm:"column:user_name"`
	UsedAt   string  `gorm:"column:usedatetime"`
}

func financeMailFn(kind string) func(context.Context, *RunContext) error {
	return func(ctx context.Context, rc *RunContext) error {
		// 1. code 表充值明细 (对齐 PHP 各周期 SQL)
		var codes []incomeRow
		codeQuery := map[string]string{
			"day": `SELECT CAST(c.number AS DOUBLE) AS number, c.userid, u.user_name, c.usedatetime
				FROM code c LEFT JOIN user u ON u.id = c.userid
				WHERE TO_DAYS(NOW()) - TO_DAYS(c.usedatetime) = 1 AND c.type = -1 AND c.isused = 1`,
			"week": `SELECT CAST(c.number AS DOUBLE) AS number, c.userid, u.user_name, c.usedatetime
				FROM code c LEFT JOIN user u ON u.id = c.userid
				WHERE DATEDIFF(NOW(), c.usedatetime) <= 7 AND DATEDIFF(NOW(), c.usedatetime) >= 1 AND c.isused = 1`,
			"month": `SELECT CAST(c.number AS DOUBLE) AS number, c.userid, u.user_name, c.usedatetime
				FROM code c LEFT JOIN user u ON u.id = c.userid
				WHERE DATE_FORMAT(c.usedatetime, '%Y-%m') = DATE_FORMAT(DATE_SUB(CURDATE(), INTERVAL 1 MONTH), '%Y-%m')
				  AND c.type = -1 AND c.isused = 1`,
		}[kind]
		_ = database.DB.WithContext(ctx).Raw(codeQuery).Scan(&codes).Error

		incomeCount, incomeTotal := int64(0), 0.0
		var html strings.Builder
		if kind == "day" { // 日报带明细表 (对齐 PHP); 周/月报仅汇总
			html.WriteString("<table border=1><tr><td>金额</td><td>用户ID</td><td>用户名</td><td>充值时间</td>")
		}
		for i := range codes {
			incomeCount++
			incomeTotal += codes[i].Number
			if kind == "day" {
				html.WriteString(fmt.Sprintf("<tr><td>%.2f</td><td>%d</td><td>%s</td><td>%s</td></tr>",
					codes[i].Number, codes[i].Userid, codes[i].UserName, codes[i].UsedAt))
			}
		}
		if kind == "day" {
			html.WriteString("</table>")
		}

		// 2. yft_order_info (易付通, 表存在时)
		var yftExists int64
		database.DB.WithContext(ctx).Raw(
			`SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'yft_order_info'`).
			Scan(&yftExists)
		if yftExists == 1 {
			yftQuery := map[string]string{
				"day":   `SELECT price AS number, user_id AS userid, '' AS user_name, DATE_FORMAT(create_time, '%Y-%m-%d %H:%i:%s') AS usedatetime FROM yft_order_info WHERE TO_DAYS(NOW()) - TO_DAYS(create_time) = 1 AND state = 1`,
				"week":  `SELECT price AS number, user_id AS userid, '' AS user_name, '' AS usedatetime FROM yft_order_info WHERE YEARWEEK(DATE_FORMAT(create_time, '%Y-%m-%d')) = YEARWEEK(NOW()) - 1 AND state = 1`,
				"month": `SELECT price AS number, user_id AS userid, '' AS user_name, '' AS usedatetime FROM yft_order_info WHERE DATE_FORMAT(create_time, '%Y-%m') = DATE_FORMAT(DATE_SUB(CURDATE(), INTERVAL 1 MONTH), '%Y-%m') AND state = 1`,
			}[kind]
			var yfts []incomeRow
			_ = database.DB.WithContext(ctx).Raw(yftQuery).Scan(&yfts).Error
			for i := range yfts {
				incomeCount++
				incomeTotal += yfts[i].Number
			}
		}

		period := map[string]string{"day": "昨日", "week": "上周", "month": "上月"}[kind]
		html.WriteString(fmt.Sprintf("<br>%s总收入笔数：%d<br>%s总收入金额：%.2f",
			period, incomeCount, period, incomeTotal))

		// 3. 发全部管理员
		type adminRow struct {
			ID    int32  `gorm:"column:id"`
			Email string `gorm:"column:email"`
		}
		var admins []adminRow
		_ = database.DB.WithContext(ctx).Table("user").
			Select("id", "email").Where("is_admin = 1").Scan(&admins).Error
		sent := int64(0)
		subject := map[string]string{"day": "SPanel-财务日报", "week": "SPanel-财务周报", "month": "SPanel-财务月报"}[kind]
		for _, a := range admins {
			if rc.DryRun {
				sent++
				continue
			}
			if err := mailer.Send(mailer.Mail{To: a.Email, Subject: subject, HTMLBody: html.String()}); err != nil {
				rc.Logf("Send finance mail to admin %d failed: %v", a.ID, err)
				continue
			}
			sent++
		}

		// 4. TG 播报 (finance_public)
		rc.SetSummary("income_count", incomeCount)
		rc.SetSummary("income_total", incomeTotal)
		rc.SetSummary("mail_sent", sent)
		if !rc.DryRun && financePublicEnabled() {
			tail := map[string]string{"day": "凌晨也在努力工作~", "week": "周末也在努力工作~", "month": "月初也在努力工作~"}[kind]
			notify.Send(fmt.Sprintf("新鲜出炉的财务%s~\n%s总收入笔数:%d\n%s总收入金额:%.2f\n%s",
				map[string]string{"day": "日报", "week": "周报", "month": "月报"}[kind],
				period, incomeCount, period, incomeTotal, tail))
		}
		rc.Logf("financeMail[%s]: 笔数 %d, 金额 %.2f, 邮件 %d", kind, incomeCount, incomeTotal, sent)
		return nil
	}
}

func financePublicEnabled() bool {
	// 对齐 PHP finance_public (默认 false); 简化挂接在 telegram.enable 之外的独立诉求暂不设键
	return false
}
