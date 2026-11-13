package handler

import "github.com/gin-gonic/gin"

// APIv1LegacyHandler v1 弃用族统一 401 拒绝 (2026-11-12 决议)
//
// 生产 h84 50 天实测 0 调用的 v1 muKey 族端点, Go 版不移植功能, 直接 401:
//
//	GET  /api/node/new           分配新节点 → v2: POST /api/node/apply_id
//	POST /api/simple_api_tools   工具箱 (new_node_id → apply_id; ip/time/due_time 无 v2 对应不迁移)
//	GET  /api/getNodeDomain/{id} 查节点域名 → v2: GET /api/nodes 或 POST /api/node/config
//	GET  /api/node/proxy/{id}    查代理配置 → v2: 同上
//	POST /api/ssn_sub/{id}       旧状态上报 (已软停用) → v2: POST /api/node/status
//	POST /api/ssn_v2/{id}        旧配置上报 (已软停用) → v2: POST /api/node/config
//
// 例外: POST /api/clonepay 也是 v1 但生产在用 (WP 内网专线回调), 完整移植于 clonepay.go。
type APIv1LegacyHandler struct{}

var DefaultAPIv1LegacyHandler = &APIv1LegacyHandler{}

// NodeNew GET /api/node/new → 401
func (h *APIv1LegacyHandler) NodeNew(c *gin.Context) { V1Rejected(c) }

// SimpleApiTools POST /api/simple_api_tools → 401
func (h *APIv1LegacyHandler) SimpleApiTools(c *gin.Context) { V1Rejected(c) }

// GetNodeDomain GET /api/getNodeDomain/:id → 401
func (h *APIv1LegacyHandler) GetNodeDomain(c *gin.Context) { V1Rejected(c) }

// NodeProxy GET /api/node/proxy/:id → 401
func (h *APIv1LegacyHandler) NodeProxy(c *gin.Context) { V1Rejected(c) }

// SsnSub POST /api/ssn_sub/:id → 401
func (h *APIv1LegacyHandler) SsnSub(c *gin.Context) { V1Rejected(c) }

// SsnV2 POST /api/ssn_v2/:id → 401
func (h *APIv1LegacyHandler) SsnV2(c *gin.Context) { V1Rejected(c) }
