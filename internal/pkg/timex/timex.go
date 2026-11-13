// Package timex 提供全站统一的时区口径：Asia/Shanghai。
//
// 与 PHP 版对齐：PHP date()/strtotime('today') 使用 PHP 配置时区（生产为上海），
// 签名日期(Ymd)、expire_in 比较、今日流量边界、月度天数计算等均以此时区为准。
package timex

import "time"

// Loc 上海时区（LoadLocation 失败时回落固定 UTC+8，容器无 tzdata 也能工作）
var Loc = loadLoc()

func loadLoc() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("Asia/Shanghai", 8*3600)
}

// Now 当前上海时间
func Now() time.Time { return time.Now().In(Loc) }

// Unix 当前时间戳（秒）
func Unix() int64 { return time.Now().Unix() }

// TodayStart 今日零点（上海时区），对齐 PHP strtotime('today')
func TodayStart() time.Time {
	n := Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, Loc)
}

// DateStr 格式化为 PHP 常用 "Y-m-d H:i:s"（上海时区）
func DateStr(t time.Time) string { return t.In(Loc).Format("2006-01-02 15:04:05") }

// DateStrNow 当前时间的 "Y-m-d H:i:s"
func DateStrNow() string { return DateStr(Now()) }

// Ymd 当前日期 "Ymd"（clonepay 签名用，对齐 PHP date("Ymd")）
func Ymd() string { return Now().Format("20060102") }

// DayOfMonth 当前日（j：1-31）
func DayOfMonth() int { return Now().Day() }

// Month 当前月（n：1-12）
func Month() int { return int(Now().Month()) }

// Year 当前年
func Year() int { return Now().Year() }

// DaysInMonth 指定年月的天数（t：28-31）
func DaysInMonth(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, Loc).Day()
}

// DaysInCurrentMonth 当前月天数
func DaysInCurrentMonth() int {
	n := Now()
	return DaysInMonth(n.Year(), int(n.Month()))
}
