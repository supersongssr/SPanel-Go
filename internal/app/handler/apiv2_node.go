package handler

import (
	"net/http"

	"spanel-go/internal/app/middleware"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
)

// APIv2NodeHandler v2 节点治理对: DNS 秒级注销 + 节点下架
//
// 对齐 PHP V2ApiController::purgeDns / offline (v8 收窄方案):
//   - muKey 三态认证 (Bearer / X-API-Token / token 参数), v2 data/error 信封;
//   - clone 入参自动归组 main, 整组生效 (与 pruneDeadNodes 连坐口径一致);
//   - 幂等: 行不存在 / 重复调用 → success 且计数 0 (调用方重试安全);
//   - 保留区保护: node_id / 归组后 mainId < 100 → 400 RESERVED_ID。
type APIv2NodeHandler struct{}

var DefaultAPIv2NodeHandler = &APIv2NodeHandler{}

// minAllocatableID 保留区下界 (对齐 PHP NodeApiService::MIN_ALLOCATABLE_ID)
const minAllocatableID = 100

// nodeIDFromReq 取 node_id 参数并做规范正整数校验 (拒绝 1e3/+5/01/小数等隐式换算写法)
func nodeIDFromReq(c *gin.Context) (int64, bool) {
	raw := c.Query("node_id")
	if raw == "" {
		raw = c.PostForm("node_id")
	}
	if raw == "" {
		return 0, false
	}
	return canonicalPositiveInt(raw)
}

// resolveNodeGroup 查节点 + clone 归组: 返回 (mainId, 组内全部 id)
func resolveNodeGroup(nodeID int64) (int64, []int64, bool) {
	var node model.Node
	if err := database.DB.First(&node, nodeID).Error; err != nil {
		// 幂等: 行不存在 = 已被 delete/GC 级联清掉
		return 0, nil, false
	}
	mainID := int64(node.ID)
	if node.IsClone > 0 {
		mainID = int64(node.IsClone)
	}
	if mainID < minAllocatableID {
		return -1, nil, false // 保留区
	}

	group := []int64{mainID}
	var clones []model.Node
	database.DB.Select("id").Where("is_clone = ? AND id >= ?", mainID, minAllocatableID).Find(&clones)
	for _, cl := range clones {
		group = append(group, int64(cl.ID))
	}
	return mainID, group, true
}

// PurgeDNS POST /api/v2/node/purge_dns —— 临时节点 DNS 秒级注销 (无状态, 只清 DNS)
//
// 整组逐节点 deleteNodeDnsRecords (CF API DELETE + dns_records 行删, 404 容忍);
// 不动节点行/type/custom_rss/Redis/日志 —— 下架与回收走心跳超时 + GC 通道。
// dns_deleted 口径 = 组内 dns_records 行数删前-删后差值 (CF 非 404 失败的行保留
// 在库待重试, 自然不计入)。
func (h *APIv2NodeHandler) PurgeDNS(c *gin.Context) {
	if !middleware.CheckMuKey(c, middleware.ModeV2ThreeWay) {
		errEnvelope(c, http.StatusUnauthorized, "INVALID_TOKEN", "token is invalid")
		return
	}

	nodeID, ok := nodeIDFromReq(c)
	if !ok {
		errEnvelope(c, http.StatusBadRequest, "INVALID_PARAM", "node_id must be a positive integer")
		return
	}
	if nodeID < minAllocatableID {
		errEnvelope(c, http.StatusBadRequest, "RESERVED_ID", "node id is in reserved range")
		return
	}
	if database.DB == nil {
		errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "database is offline")
		return
	}

	mainID, group, found := resolveNodeGroup(nodeID)
	if mainID == -1 {
		errEnvelope(c, http.StatusBadRequest, "RESERVED_ID", "main node id is in reserved range")
		return
	}
	if !found {
		// 幂等: 行不存在 = DNS 亦无残留
		dataOK(c, gin.H{"node_id": nodeID, "group_size": 0, "dns_deleted": 0})
		return
	}

	var before, after int64
	database.DB.Model(&model.DnsRecord{}).Where("node_id IN ?", group).Count(&before)

	for _, gid := range group {
		service.DeleteNodeDNSRecords(gid)
	}

	database.DB.Model(&model.DnsRecord{}).Where("node_id IN ?", group).Count(&after)

	dataOK(c, gin.H{
		"node_id":     mainID,
		"group_size":  len(group),
		"dns_deleted": before - after,
	})
}

// Offline POST /api/v2/node/offline —— 节点下架 (单一职责, 整组只置 type=0)
//
// 订阅 (type=1 过滤) 与节点列表即刻排除整组; 不动 custom_rss/心跳/DNS/其他字段;
// 心跳照常可上报, 复机走 register (重注册自动 type=1); 行由 GC 通道回收。
// offlined = 本次实际置 0 的行数 (非组规模), 幂等重放返回 0。
func (h *APIv2NodeHandler) Offline(c *gin.Context) {
	if !middleware.CheckMuKey(c, middleware.ModeV2ThreeWay) {
		errEnvelope(c, http.StatusUnauthorized, "INVALID_TOKEN", "token is invalid")
		return
	}

	nodeID, ok := nodeIDFromReq(c)
	if !ok {
		errEnvelope(c, http.StatusBadRequest, "INVALID_PARAM", "node_id must be a positive integer")
		return
	}
	if nodeID < minAllocatableID {
		errEnvelope(c, http.StatusBadRequest, "RESERVED_ID", "node id is in reserved range")
		return
	}
	if database.DB == nil {
		errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "database is offline")
		return
	}

	mainID, group, found := resolveNodeGroup(nodeID)
	if mainID == -1 {
		errEnvelope(c, http.StatusBadRequest, "RESERVED_ID", "main node id is in reserved range")
		return
	}
	if !found {
		dataOK(c, gin.H{"node_id": nodeID, "group_size": 0, "offlined": 0})
		return
	}

	// 只动 type: 仅更新仍 type!=0 的行, 返回实际生效行数
	res := database.DB.Model(&model.Node{}).
		Where("id IN ? AND type != 0", group).
		Update("type", 0)
	offlined := int64(0)
	if res.Error == nil {
		offlined = res.RowsAffected
	} else {
		logApiV2("offline update failed node_id=%d err=%v", mainID, res.Error)
	}

	dataOK(c, gin.H{
		"node_id":    mainID,
		"group_size": len(group),
		"offlined":   offlined,
	})
}
