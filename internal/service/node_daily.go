// node_daily.go 节点每日流量快照与 7 日滑动窗口
// (对齐 PHP app/Services/NodeDailyTraffic.php::runDaily)。
//
//	snapshot key: ssp:node:{id}:traffic_snapshot (TTL 2 天, 昨日 traffic_used 基准)
//	窗口 key:    ssp:node:{id}:traffic_7d (LPUSH+LTRIM 保留 7 条, [0]=昨日最新)
//
// 关键口径: 基准缺失 (GET 返回不存在) → diff=0 只重建基准;
// 基准为 "0" (月度重置后) → diff=today; 计数器回退 (today<yesterday) → diff=today。
// 写 ss_node.status 已停用 (节点端 monitor= 上报 vnstat, 面板不覆盖)。
package service

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
)

// NodeDailyRunDaily 推入昨日增量并返回 diff (bytes)。
// 必须在 NodeSortCalculate 之前执行 (窗口 [0] 须为刚推入的昨日增量)。
func NodeDailyRunDaily(ctx context.Context, node *model.Node) int64 {
	rdb := database.Redis
	if rdb == nil {
		return 0
	}
	snapKey := fmt.Sprintf("ssp:node:%d:traffic_snapshot", node.ID)
	today := node.TrafficUsed

	yesterday, err := rdb.Get(ctx, snapKey).Result()
	var diff int64
	switch {
	case err == redis.Nil:
		diff = 0 // 基准缺失: 只重建基准, 不计增量
	case err != nil:
		diff = 0 // Redis 异常: 保守 0
	default:
		var y int64
		if _, perr := fmt.Sscanf(yesterday, "%d", &y); perr == nil {
			if today >= y {
				diff = today - y // 含 y=0 (月度重置后) → diff=today
			} else {
				diff = today // 计数器回退/周期重置: 取 today
			}
		}
	}

	rdb.SetEx(ctx, snapKey, fmt.Sprintf("%d", today), 86400*2) //nolint:errcheck 基准写入失败下轮重建
	w7 := fmt.Sprintf("ssp:node:%d:traffic_7d", node.ID)
	rdb.LPush(ctx, w7, diff) //nolint:errcheck
	rdb.LTrim(ctx, w7, 0, 6) //nolint:errcheck 保留 7 条
	return diff
}

// NodeDailyWindow7d 读取 7 日窗口 (供健康分计算)。
func NodeDailyWindow7d(ctx context.Context, nodeID int64) []int64 {
	rdb := database.Redis
	if rdb == nil {
		return nil
	}
	w7 := fmt.Sprintf("ssp:node:%d:traffic_7d", nodeID)
	vals, err := rdb.LRange(ctx, w7, 0, 6).Result()
	if err != nil || len(vals) == 0 {
		return nil
	}
	out := make([]int64, 0, len(vals))
	for _, v := range vals {
		var n int64
		if _, perr := fmt.Sscanf(v, "%d", &n); perr == nil {
			out = append(out, n)
		}
	}
	return out
}
