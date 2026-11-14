// node_sort.go 节点健康分 (对齐 PHP app/Services/NodeSort/NodeSortCalculator.php v2
// + Job.php DailyJob 内保留的 v1 回滚公式; 方案 2026-11-11-node_sort健康分v2三维计算)。
//
// v2 三维 (自比健康率模型, 无状态可任意复算):
//
//	S 主分 ±60: 近3日均值 / 基准 base (max(7日峰值, 历史峰值×0.5, 1GiB)), R∈[0.5,0.9] 线性
//	T 趋势 ±20: 近2日 vs 第3~7日均值斜率 (剔除哑火日, 基准≥500MiB 才计)
//	K 打击 −15/击 (上限3击): 近6日哑火日 (<max(50MiB, base×0.15)) + 心跳>48h
//	总分 clamp [−150, +80]; 窗口 <3 条 → 0 (观察期)
//
// v1 回滚: 低于 min(left_daily, maxDay)/3 → 每日-100 无界累积; 否则 = 日均GB/成本。
package service

import (
	"context"
	"math"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/timex"
)

const (
	nodeSortObserveMinWindow = 3              // 观察期最小窗口
	nodeSortHistFactor       = 0.5            // 历史峰值折半托底
	nodeSortCapFloor         = int64(1 << 30) // 基准下限 1 GiB
	nodeSortRZero            = 0.5            // 零分线
	nodeSortRFull            = 0.9            // 满分线
	nodeSortSWeight          = 60.0
	nodeSortBadAbs           = int64(50 << 20) // 哑火绝对下限 50 MiB
	nodeSortBadBaseRatio     = 0.15
	nodeSortTWeight          = 20.0
	nodeSortTSpan            = 0.5
	nodeSortTBasisFloor      = int64(500 << 20) // 趋势基准下限 500 MiB
	nodeSortKPerStrike       = -15.0
	nodeSortKMaxStrikes      = 3
	nodeSortKWindow          = 6 // 只看近 6 日
	nodeSortHeartbeatStale   = 48 * 3600
	nodeSortMin              = -150.0
	nodeSortMax              = 80.0
)

// NodeSortCalculate v2 健康分 (纯函数, 只读 node 属性 + Redis 7 日窗口)。
// 调用前提: runDaily 之后 (窗口[0]=昨日增量); type≠0 && is_clone==0 && node_sort>-900 由调用方过滤。
func NodeSortCalculate(ctx context.Context, node *model.Node, w []int64) int {
	n := len(w)
	if n < nodeSortObserveMinWindow {
		return 0 // 观察期: 不加不扣
	}

	// 基准 base = max(7日实证峰值, 历史峰值×0.5, 1GiB)
	var maxW int64
	for _, v := range w {
		if v > maxW {
			maxW = v
		}
	}
	base := maxW
	if hist := int64(math.Floor(nodeSortHistFactor * float64(node.TrafficMaxDayValue))); hist > base {
		base = hist
	}
	if base < nodeSortCapFloor {
		base = nodeSortCapFloor
	}

	// S 主分: 近3日均值/基准, R∈[0.5,0.9] → ±60
	avg3 := float64(w[0]+w[1]+w[2]) / 3
	r := avg3 / float64(base)
	s := nodeSortSWeight * clampFloat((r-nodeSortRZero)/(nodeSortRFull-nodeSortRZero), -1, 1)

	// 哑火判定 (K/T 共用): w[i] < max(50MiB, base×0.15)
	badFloor := nodeSortBadAbs
	if bf := int64(math.Floor(nodeSortBadBaseRatio * float64(base))); bf > badFloor {
		badFloor = bf
	}
	isBad := make([]bool, n)
	for i := 0; i < n; i++ {
		isBad[i] = w[i] < badFloor
	}

	// T 趋势: 近2日 (剔除哑火) vs 第3~7日均值 (≥2 个非哑火且均值≥500MiB)
	tScore := 0.0
	var recent, basis []int64
	for i := 0; i < n && i <= 1; i++ {
		if !isBad[i] {
			recent = append(recent, w[i])
		}
	}
	for i := 2; i < n; i++ {
		if !isBad[i] {
			basis = append(basis, w[i])
		}
	}
	if len(recent) > 0 && len(basis) >= 2 {
		var sum int64
		for _, v := range basis {
			sum += v
		}
		basisMean := float64(sum) / float64(len(basis))
		if basisMean >= float64(nodeSortTBasisFloor) {
			var rsum int64
			for _, v := range recent {
				rsum += v
			}
			slope := (float64(rsum)/float64(len(recent)) - basisMean) / basisMean
			tScore = nodeSortTWeight * clampFloat(slope/nodeSortTSpan, -1, 1)
		}
	}

	// K 打击: 近6日哑火数 + 心跳超 48h (type≠0 且心跳>0 由调用方保证)
	strikes := 0
	for i := 0; i < n && i < nodeSortKWindow; i++ {
		if isBad[i] {
			strikes++
		}
	}
	if node.NodeHeartbeat > 0 && node.NodeHeartbeat < timex.Unix()-nodeSortHeartbeatStale {
		strikes++
	}
	k := nodeSortKPerStrike * float64(min(strikes, nodeSortKMaxStrikes))

	total := math.Round(s + tScore + k)
	return int(clampFloat(total, nodeSortMin, nodeSortMax))
}

// NodeSortCalculateV1 v1 回滚公式 (config node_sort_engine='v1'; 保留一个版本周期)。
func NodeSortCalculateV1(node *model.Node) int {
	maxDay := node.TrafficMaxDayValue
	if maxDay <= 0 {
		maxDay = node.TrafficUsedLastday
	}
	threshold := minInt64(node.TrafficLeftDaily, maxDay) / 3
	if node.TrafficUsedDaily < threshold {
		return int(node.NodeSort) - 100 // 无界累积
	}
	cost := node.NodeCost
	if cost < 1 {
		cost = 1
	}
	return int(math.Floor(float64(node.TrafficUsedDaily) / 1024 / 1024 / 1024 / float64(cost)))
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
