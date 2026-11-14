// node_rate.go 节点倍率计算 (对齐 PHP app/Services/TrafficRate/TrafficRateCalculator.php,
// 利用率计费引擎 v4)。
//
// DailyJob 末尾顺序锁死: SnapshotDailyPeaks (→ traffic_max_day_value) → RecalculateAll
// (main 公式依赖 max_day, 顺序不可颠倒)。冷启动期 (无峰值/配额耗尽) 回退 node_cost。
package service

import (
	"context"
	"fmt"
	"math"

	"gorm.io/gorm"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/timex"
)

const (
	rateDefault  = 1.0
	rateMin      = 0.1
	rateSnapshot = "ssp:node_traffic_daily:last_run" // TTL 7 天, Analytics 同源基准
)

func rateNormalize(r float64) float64 {
	if r < rateMin {
		r = rateMin
	}
	return math.Round(r*10) / 10
}

// SnapshotDailyPeaks 快照历史日峰值: 25h 间隔保护 (首跑/漏跑跨天只刷基准不计峰值)。
// clone 跳过 (与 main 共享流量, 防重复计增)。
func SnapshotDailyPeaks(ctx context.Context, logf func(string, ...any)) map[string]int64 {
	stats := map[string]int64{"count_peak": 0, "refreshed_lastday": 0}
	rdb := database.Redis
	if database.DB == nil {
		return stats
	}

	countPeak := false
	if rdb != nil {
		lastRun, err := rdb.Get(ctx, rateSnapshot).Result()
		if err == nil {
			var last int64
			if _, perr := fmt.Sscanf(lastRun, "%d", &last); perr == nil {
				gap := timex.Unix() - last
				countPeak = gap > 0 && gap < 25*3600
			}
		}
	}

	var batch []model.Node
	_ = database.DB.WithContext(ctx).
		Model(&model.Node{}).
		Select("id", "traffic_used", "traffic_used_lastday", "traffic_max_day_value").
		Where("is_clone = 0").
		Order("id ASC").
		FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				n := &batch[i]
				updates := map[string]any{}
				if countPeak && n.TrafficUsedLastday > 0 {
					var dailyInc int64
					if n.TrafficUsed >= n.TrafficUsedLastday {
						dailyInc = n.TrafficUsed - n.TrafficUsedLastday
					} else {
						dailyInc = n.TrafficUsed // 周期重置归零 → 取 usedNow
					}
					if dailyInc > n.TrafficMaxDayValue {
						updates["traffic_max_day_value"] = dailyInc // 只增更新
						stats["count_peak"]++
					}
				}
				if n.TrafficUsed != n.TrafficUsedLastday {
					updates["traffic_used_lastday"] = n.TrafficUsed
					stats["refreshed_lastday"]++
				}
				if len(updates) > 0 {
					_ = database.DB.WithContext(ctx).Model(&model.Node{}).
						Where("id = ?", n.ID).Updates(updates).Error
				}
			}
			return nil
		})

	if rdb != nil {
		rdb.SetEx(ctx, rateSnapshot, fmt.Sprintf("%d", timex.Unix()), 7*86400) //nolint:errcheck
	}
	if logf != nil {
		logf("snapshotDailyPeaks: count_peak=%d refreshed_lastday=%d", stats["count_peak"], stats["refreshed_lastday"])
	}
	return stats
}

// TrafficRateCalculate 单节点倍率 (main 公式)。
// 冷启动判定: maxDay>0 且 leftDaily>0 才利用率计费, 否则回退 node_cost。
// 不封顶 (过载 → rate>cost 施加分流压力); left_daily=0 防爆回退。
func TrafficRateCalculate(node *model.Node) float64 {
	cost := float64(node.NodeCost)
	if cost <= 0 {
		cost = 1.0
	}
	maxDay := node.TrafficMaxDayValue
	leftDaily := node.TrafficLeftDaily
	usedDaily := node.TrafficUsedDaily
	var rate float64
	if maxDay > 0 && leftDaily > 0 {
		supplyDaily := minInt64(maxDay, leftDaily)
		rate = float64(usedDaily) / float64(max64(1, supplyDaily)) * cost
	} else {
		rate = cost
	}
	return rateNormalize(rate)
}

// RecalculateAll 全量重算倍率: main (is_clone=0) 直接算; clone (is_clone>0) = main 倍率
// + 0.01×node_class 微差异化, 孤儿 clone (main 已删) 跳过。差异 >0.0001 才落库。
func RecalculateAll(ctx context.Context, logf func(string, ...any)) map[string]int64 {
	stats := map[string]int64{"main_updated": 0, "clone_updated": 0, "clone_orphan": 0}
	if database.DB == nil {
		return stats
	}
	mainRate := map[int64]float64{}

	var batch []model.Node
	_ = database.DB.WithContext(ctx).
		Model(&model.Node{}).
		Select("id", "node_cost", "traffic_rate", "traffic_max_day_value", "traffic_left_daily", "traffic_used_daily").
		Where("is_clone = 0").
		Order("id ASC").
		FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				n := &batch[i]
				newRate := TrafficRateCalculate(n)
				mainRate[int64(n.ID)] = newRate
				if math.Abs(newRate-float64(n.TrafficRate)) > 0.0001 {
					_ = database.DB.WithContext(ctx).Model(&model.Node{}).
						Where("id = ?", n.ID).
						Update("traffic_rate", newRate).Error
					stats["main_updated"]++
				}
			}
			return nil
		})

	var clones []model.Node
	_ = database.DB.WithContext(ctx).
		Model(&model.Node{}).
		Select("id", "is_clone", "node_class", "traffic_rate").
		Where("is_clone > 0").
		Order("id ASC").
		Find(&clones).Error
	for i := range clones {
		c := &clones[i]
		mr, ok := mainRate[int64(c.IsClone)]
		if !ok {
			// main 不在缓存 (理论上 main 全量扫过, 除非并发删除) → 重查一次
			var main model.Node
			if err := database.DB.WithContext(ctx).Select("traffic_rate").
				First(&main, c.IsClone).Error; err != nil {
				stats["clone_orphan"]++
				continue
			}
			mr = float64(main.TrafficRate)
		}
		cloneRate := rateNormalize(mr + 0.01*float64(c.NodeClass))
		if math.Abs(cloneRate-float64(c.TrafficRate)) > 0.0001 {
			_ = database.DB.WithContext(ctx).Model(&model.Node{}).
				Where("id = ?", c.ID).
				Update("traffic_rate", cloneRate).Error
			stats["clone_updated"]++
		}
	}
	if logf != nil {
		logf("node rates recalculated: main=%d clone=%d orphan=%d", stats["main_updated"], stats["clone_updated"], stats["clone_orphan"])
	}
	return stats
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
