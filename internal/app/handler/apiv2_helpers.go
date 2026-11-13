package handler

import (
	"fmt"
	"log"
	"math"
	"net/mail"
	"strconv"
	"time"
)

// canonicalPositiveInt 规范正整数字符串判定 (对齐 PHP ctype_digit + 回写比对):
// 拒绝前导零 (01)、科学计数法 (1e3)、带符号 (+5)、小数、非数字
func canonicalPositiveInt(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	if strconv.FormatInt(n, 10) != s {
		return 0, false // 前导零等非规范形式
	}
	return n, true
}

// validEmail 邮箱格式校验 (对齐 PHP filter_var FILTER_VALIDATE_EMAIL 的大口径)
func validEmail(s string) bool {
	if s == "" || len(s) > 254 {
		return false
	}
	_, err := mail.ParseAddress(s)
	return err == nil
}

// parseIntDefault 安全整数解析 (失败返回 def)
func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// roundF 四舍五入保留 n 位小数 (PHP round 语义)
func roundF(v float64, n int) float64 {
	shift := math.Pow(10, float64(n))
	return math.Round(v*shift) / shift
}

// timeStr time.Time → "Y-m-d H:i:s" (零值返回空串, 对齐 PHP (string) 强转)
func timeStr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

// logApiV2 v2 族统一日志前缀
func logApiV2(format string, args ...any) {
	log.Printf("[APIv2] "+format, args...)
}

// fmtInt int64 格式化 (调试用)
func fmtInt(n int64) string { return fmt.Sprintf("%d", n) }
