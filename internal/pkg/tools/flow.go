// Package tools 通用工具（对齐 PHP app/Utils/Tools.php 中面板侧使用的函数）
package tools

import "fmt"

// FlowAutoShow 字节数转人类可读字符串，对齐 PHP Tools::flowAutoShow
// （阈值严格大于才进位，round(x,2)，单位无空格，如 "1.23GB"）
func FlowAutoShow(value float64) string {
	const kb = 1024.0
	const mb = 1024 * 1024.0
	const gb = 1024 * 1024 * 1024.0
	const tb = gb * 1024
	const pb = tb * 1024

	abs := value
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs > pb:
		return fmt.Sprintf("%.2fPB", round2(value/pb))
	case abs > tb:
		return fmt.Sprintf("%.2fTB", round2(value/tb))
	case abs > gb:
		return fmt.Sprintf("%.2fGB", round2(value/gb))
	case abs > mb:
		return fmt.Sprintf("%.2fMB", round2(value/mb))
	case abs > kb:
		return fmt.Sprintf("%.2fKB", round2(value/kb))
	default:
		return fmt.Sprintf("%.2fB", round2(value))
	}
}

// round2 PHP round(x, 2) 语义（四舍五入到 2 位小数）
func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100 // v>=0
}
