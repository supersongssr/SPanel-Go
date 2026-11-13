// 节点月度流量日历公式 (对齐 PHP app/Services/NodeMonthlyTraffic.php 的
// computeDaysElapsed / computeDaysRemaining / 日封顶)。
//
// 口径: 按 traffic_reset_day 为月内锚点日, 跨月顺延; reset_day==0 回退自然月。
// 全部使用 timex (Asia/Shanghai) 时区, 与 PHP date() 一致。
package service

import (
	"spanel-go/internal/pkg/timex"
)

// UnlimitedDailyLeftCap 无限制节点 (traffic_limit<=0) 的 left_daily 封顶哨兵值: 1 TiB
const UnlimitedDailyLeftCap = int64(1000 * 1024 * 1024 * 1024)

// DaysElapsed 本周期已过天数 (>=1), 按 resetDay 锚点跨月顺延
func DaysElapsed(resetDay int) int {
	today := timex.DayOfMonth()
	if resetDay > 0 {
		if today >= resetDay {
			return maxInt(1, today-resetDay+1)
		}
		// 跨月: 上月 resetDay 起 + 本月至今日
		lastMonth := timex.Month() - 1
		lastYear := timex.Year()
		if lastMonth < 1 {
			lastMonth = 12
			lastYear--
		}
		daysInLastMonth := timex.DaysInMonth(lastYear, lastMonth)
		return maxInt(1, daysInLastMonth-resetDay+today+1)
	}
	// reset_day==0: 回退自然月
	return maxInt(1, today)
}

// DaysRemaining 本周期剩余天数 (>=1), 按 resetDay 锚点跨月顺延
func DaysRemaining(resetDay int) int {
	today := timex.DayOfMonth()
	daysInMonth := timex.DaysInCurrentMonth()
	if resetDay > 0 {
		if today >= resetDay {
			return maxInt(1, daysInMonth-today+minInt(resetDay, daysInMonth))
		}
		return maxInt(1, resetDay-today)
	}
	// reset_day==0: 回退自然月
	return maxInt(1, daysInMonth-today)
}

// DailyLeftCap 无限制节点的日剩余封顶: min(1TiB 哨兵值, 历史日峰值 maxDayValue)
// (口径对齐 PHP computeDailyLeft 无限制分支)
func DailyLeftCap(maxDayValue int64) int64 {
	cap := UnlimitedDailyLeftCap
	if maxDayValue > 0 && maxDayValue < cap {
		cap = maxDayValue
	}
	return cap
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
