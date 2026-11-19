// Package adminv2 管理端 v2 API (/api/v2/admin) — 方案 .plans/2026-11-18_admin移植方案
// 与 v1 (/api/v1/admin) 双轨并存 (Owner 2026-11-18 决议 R5: v1 暂不下线)。
// 统一契约:
//
//	列表: GET ?page=1&page_size=25&order_by=id&order_dir=desc&search=xxx → {ret:1,data:{items,total}}
//	      page_size=0 表示全部 (对应 PHP DataTables length=-1)
//	动作: {ret:1|0, msg:"..."}
package adminv2

import (
	"strconv"

	"spanel-go/internal/pkg/tools"

	"github.com/gin-gonic/gin"
)

// Handler 管理端 v2 处理器 (无状态, 复用全局 database.DB / service 层)
type Handler struct{}

// Default 包级单例 (与 v1 DefaultAdminHandler 同惯例)
var Default = &Handler{}

// ListQuery 统一列表查询契约 (admin-table.js 前端组件对接)
type ListQuery struct {
	Page     int // 从 1 起
	PageSize int // 0 = 全部
	Offset   int
	Limit    int    // -1 = 不加 LIMIT
	OrderBy  string // 已通过白名单校验的 DB 列名
	OrderDir string // asc | desc
	Search   string
}

// ParseListQuery 解析并校验列表查询参数。
// sortable: 前端列 key → DB 列名 白名单 (空 map 表示该资源不支持排序);
// defCol/defDir: 默认排序 (非法/缺省 order_by 时回退, 对齐 DataTables 默认序)。
func ParseListQuery(c *gin.Context, sortable map[string]string, defCol, defDir string) *ListQuery {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "25"))
	if pageSize < 0 || pageSize > 100000 {
		pageSize = 25
	}

	q := &ListQuery{Page: page, PageSize: pageSize}
	if pageSize == 0 { // 全部
		q.Offset, q.Limit = 0, -1
	} else {
		q.Offset, q.Limit = (page-1)*pageSize, pageSize
	}

	q.OrderBy = defCol
	if col, ok := sortable[c.Query("order_by")]; ok && col != "" {
		q.OrderBy = col
	}
	q.OrderDir = defDir
	switch c.Query("order_dir") {
	case "asc":
		q.OrderDir = "asc"
	case "desc":
		q.OrderDir = "desc"
	}

	q.Search = c.Query("search")
	return q
}

// RespondList 列表统一响应
func RespondList(c *gin.Context, items interface{}, total int64) {
	c.JSON(200, gin.H{"ret": 1, "msg": "获取成功", "data": gin.H{"items": items, "total": total}})
}

// RespondOK 动作统一响应
func RespondOK(c *gin.Context, msg string) {
	c.JSON(200, gin.H{"ret": 1, "msg": msg})
}

// RespondErr 动作失败响应 (HTTP 200 + ret=0, 与 v1/PHP 信封一致)
func RespondErr(c *gin.Context, msg string) {
	c.JSON(200, gin.H{"ret": 0, "msg": msg})
}

// flowAuto bytes → 可读字符串 (tools.FlowAutoShow 包装, 避免 audit.go 引入新依赖)
func flowAuto(b int64) string {
	return tools.FlowAutoShow(float64(b))
}
