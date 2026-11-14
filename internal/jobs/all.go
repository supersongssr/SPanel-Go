// all.go 任务注册中心 (显式列出, 顺序即注册序)。
// 新任务移植时: 新建文件实现 registerXxx() 并在 init 追加一行;
// config.yaml jobs.schedules 可按名覆盖表达式, 置 "" = 停用排程仅手动。
package jobs

import (
	"fmt"
	"strings"

	"spanel-go/internal/config"
)

func init() {
	// 定时任务 (生产 crontab 对齐)
	registerDailyJob()
	registerHourlyJob()
	registerCdnBanDetect()
	registerDbClean()
	registerCheckJob()
	registerSendDiaryMail()
	// 默认不排程 (仅手动/cron 覆盖)
	registerFinanceJobs()
	registerBackup()
	registerRadiusJobs()
	// E 类: 手动触发入口 (定时逻辑已含在 dailyjob 内)
	registerEClassJobs()
}

// EffectiveSchedule 解析任务生效表达式: config 覆盖 > 注册表默认; "" = 不排程。
// 第二个返回值表示是否被 config 覆盖。
func EffectiveSchedule(cfg *config.Config, name, def string) (string, bool) {
	if cfg != nil {
		if v, ok := cfg.Jobs.Schedules[name]; ok {
			return v, true
		}
	}
	return def, false
}

// PrintList CLI `spanel job list`: 打印注册表 (名称/生效表达式/说明)。
func PrintList(cfg *config.Config) {
	fmt.Printf("%-18s %-18s %s\n", "JOB", "SCHEDULE", "TITLE")
	fmt.Println(strings.Repeat("-", 78))
	for _, j := range All() {
		expr, overridden := EffectiveSchedule(cfg, j.Name, j.Schedule)
		if expr == "" {
			expr = "(仅手动)"
		} else if overridden {
			expr += " ←cfg"
		}
		fmt.Printf("%-18s %-20s %s\n", j.Name, expr, j.Title)
	}
	fmt.Println(strings.Repeat("-", 78))
	fmt.Println("←cfg = config.yaml jobs.schedules 覆盖; 手动执行: ./spanel job <name> [--dry-run]")
}
