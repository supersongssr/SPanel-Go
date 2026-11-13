// 节点全量级联删除服务 —— 对齐 PHP app/Services/NodePurgeService.php 的忠实移植。
//
// 单一实现, 三个删除入口共用:
//   - GC 每日固定周期自动回收 (64 天死节点, cascade="dead")
//   - 水位线手动应急命令 (cascade="dead")
//   - 临时节点直接删除通道 / admin 手动删 (cascade="all")
//
// 删除顺序 (无 FK 兜底, 顺序重要):
//
//	死/全部 clone 子节点 → DNS(DB+CF) → Redis 无 TTL key → 关联日志表
//	→ node_ids 引用修正 → 本行。
package service

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
)

// minAllocatableID 对齐 PHP NodeApiService::MIN_ALLOCATABLE_ID = 100:
// 最小可分配节点 ID, < 此值的节点视为保留区 (测试/公告/系统节点),
// 一切自动删除 (GC / 直接删除通道 / pruneDeadNodes) 不碰此区间。
const minAllocatableID = 100

// PurgeNode 对齐 NodePurgeService::purge(node, cascade, cutoff):
// 完整级联删除一个节点 (含其 clone 子节点)。
//
//	cascade: "all"  = 级联全部 clone (直接删除通道, 机器销毁场景, clone 无独立存在意义);
//	         "dead" = 仅级联死 clone (GC 口径: type=0 且 hb>0 且 hb<cutoff,
//	                  掉线仅数天的活 clone 不因 main 被回收而连带删除; 保留区不级联)
//	cutoff:  死池心跳截止线 (time()-天数*86400); cascade="dead" 时必传。
//	         hb>0 是必要防护 — 首次心跳前 hb=0 无任何时间信息可判年龄, 绝不进自动删除。
//
// 返回 stats: {"clones_deleted":N,"dns_deleted":N,"redis_cleared":N,"log_rows_deleted":N}
func PurgeNode(node *model.Node, cascade string, cutoff int64) map[string]int {
	stats := map[string]int{
		"clones_deleted":   0,
		"dns_deleted":      0,
		"redis_cleared":    0,
		"log_rows_deleted": 0,
	}
	if database.DB == nil {
		log.Printf("[NodePurge] database not initialized, skip purge id=%v", nodeIDOf(node))
		return stats
	}
	purgeDo(node, cascade, cutoff, stats)
	return stats
}

// purgeDo 对齐 PHP doPurge —— 内部递归实现, stats 引用累加。
func purgeDo(x *model.Node, cascade string, cutoff int64, stats map[string]int) {
	if x == nil {
		return
	}
	id := int64(x.ID)

	// 若是 main, 先递归清其 clone 子节点 (id DESC 遍历时 clone 多已先于 main 删, 这里保底)
	//   'dead': 只连坐死 clone (口径与 GC/pruneDeadNodes 候选集一致: id>=MIN_ALLOCATABLE_ID
	//           且 type=0 且 hb>0 且心跳超 cutoff; 保留区 clone 不级联, 不越出保留区);
	//   'all':  全部 clone。
	if x.IsClone == 0 {
		query := database.DB.Model(&model.Node{}).
			Where("is_clone = ?", id).
			Where("id >= ?", minAllocatableID)
		if cascade != "all" {
			query = query.
				Where("type = ?", 0).
				Where("node_heartbeat > ?", 0).
				Where("node_heartbeat < ?", cutoff)
		}
		var childIDs []int32
		if err := query.Pluck("id", &childIDs).Error; err != nil {
			log.Printf("[NodePurge] load clones failed id=%d: %v", id, err)
		}
		for _, cid := range childIDs {
			var clone model.Node
			if err := database.DB.First(&clone, cid).Error; err != nil {
				continue // PHP: Node::find 未命中则跳过 (避免脏读)
			}
			purgeDo(&clone, cascade, cutoff, stats)
			stats["clones_deleted"]++
		}
	}

	// 1. DNS (DB + Cloudflare); L2 通道 (CleanExpiredDnsRecords 32 天) 先行清过时此处幂等 no-op
	stats["dns_deleted"] += DeleteNodeDNSRecords(id)

	// 2. Redis 无 TTL 的两个 key (traffic_7d / traffic_snapshot; warn_* 自过期无需清)
	// PHP: try { new RedisClient(); del×2; stats+=2 } catch → error_log, 不计数
	if database.Redis != nil {
		keys := []string{
			fmt.Sprintf("ssp:node:%d:traffic_7d", id),
			fmt.Sprintf("ssp:node:%d:traffic_snapshot", id),
		}
		if err := database.Redis.Del(context.Background(), keys...).Err(); err != nil {
			log.Printf("[NodePurge] redis clean fail id=%d: %v", id, err)
		} else {
			stats["redis_cleared"] += 2
		}
	} else {
		log.Printf("[NodePurge] redis clean fail id=%d: redis client not initialized", id)
	}

	// 3. 关联日志表防御性 DELETE (批量不逐行; 死节点多为 0 行, 但保正确)
	stats["log_rows_deleted"] += purgeExecDelete("user_traffic_log", "node_id", id) // PHP TrafficLog
	stats["log_rows_deleted"] += purgeModelDelete(&model.NodeInfoLog{}, id)         // PHP NodeInfoLog (ss_node_info)
	stats["log_rows_deleted"] += purgeModelDelete(&model.NodeOnlineLog{}, id)       // PHP NodeOnlineLog (ss_node_online_log)
	stats["log_rows_deleted"] += purgeExecDelete("detect_log", "node_id", id)       // PHP DetectLog
	// 用户流量汇总两表 (PHP UserTrafficRollup::purgeNode): 节点下架即删累计记录
	// (节点累计非永久; 节点 id 不复用 —— id 恒唯一, 无混淆)
	stats["log_rows_deleted"] += purgeExecDelete("user_traffic_daily", "node_id", id)
	stats["log_rows_deleted"] += purgeExecDelete("user_node_traffic_total", "node_id", id)
	stats["log_rows_deleted"] += purgeExecDelete("relay", "source_node_id", id) // PHP Relay (源)
	stats["log_rows_deleted"] += purgeExecDelete("relay", "dist_node_id", id)   // PHP Relay (目标)
	// node_reset_day_audit 为审计表, 保留 (历史溯源)

	// 4. 修 node_ids 引用: 若 X 是 clone, 从其 main 的 node_ids 逗号串里移除 (订阅/clone 列表一致性)
	if x.IsClone > 0 {
		var main model.Node
		if err := database.DB.First(&main, x.IsClone).Error; err == nil && main.NodeIDs != "" {
			idStr := strconv.Itoa(int(id))
			kept := make([]string, 0, len(strings.Split(main.NodeIDs, ","))) // PHP array_values(array_diff(...)): 移除全部匹配
			for _, part := range strings.Split(main.NodeIDs, ",") {
				if part != idStr {
					kept = append(kept, part)
				}
			}
			var updateErr error
			if len(kept) > 0 {
				updateErr = database.DB.Model(&model.Node{}).
					Where("id = ?", main.ID).
					Update("node_ids", strings.Join(kept, ",")).Error
			} else {
				updateErr = database.DB.Model(&model.Node{}).
					Where("id = ?", main.ID).
					Update("node_ids", nil).Error // PHP: $main->node_ids = null
			}
			if updateErr != nil {
				log.Printf("[NodePurge] fix node_ids failed main=%d clone=%d: %v", main.ID, id, updateErr)
			}
		}
	}

	// 5. 删节点行本身
	if err := database.DB.Delete(&model.Node{}, id).Error; err != nil {
		log.Printf("[NodePurge] delete node row failed id=%d: %v", id, err)
	}
}

// nodeIDOf 安全取节点 id (nil 防护, 仅日志用)。
func nodeIDOf(n *model.Node) int64 {
	if n == nil {
		return 0
	}
	return int64(n.ID)
}

// purgeModelDelete 按模型删除节点关联日志行 (模型已存在的表走 GORM Delete), 返回删除行数。
func purgeModelDelete(m any, nodeID int64) int {
	res := database.DB.Where("node_id = ?", nodeID).Delete(m)
	if res.Error != nil {
		log.Printf("[NodePurge] model delete failed node_id=%d: %v", nodeID, res.Error)
		return 0
	}
	return int(res.RowsAffected)
}

// purgeExecDelete 无模型表 (user_traffic_log / detect_log / relay 等) 的 Raw SQL 删除,
// 返回删除行数 (对齐 PHP Model::where(...)->delete() 的返回值)。
func purgeExecDelete(table, column string, nodeID int64) int {
	res := database.DB.Exec(
		fmt.Sprintf("DELETE FROM `%s` WHERE `%s` = ?", table, column), nodeID)
	if res.Error != nil {
		log.Printf("[NodePurge] delete %s failed node_id=%d: %v", table, nodeID, res.Error)
		return 0
	}
	return int(res.RowsAffected)
}
