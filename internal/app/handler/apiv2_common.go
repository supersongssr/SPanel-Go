package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ─────────────────────────────────────────────────────────────────────────────
// /api/* 各族响应信封 helpers (对齐 PHP 三套信封, 不得混用)
// ─────────────────────────────────────────────────────────────────────────────

// 1) v2 data/error 信封 (/api/v2/user, /api/v2/node/*, /api/v2/backend/*)
//    成功 {"data":{...}} / 失败 {"error":{"code","message"}} + HTTP 状态码

func dataOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"data": data})
}

func errEnvelope(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

// 2) v2 生命周期族 status/message 信封 (/api/node/*, GET /api/nodes)
//    成功 {"status":"success",...} / 失败 {"status":"error","message":...} (HTTP 200, 对齐 PHP echoJson)

func lifecycleError(c *gin.Context, message string) {
	c.JSON(http.StatusOK, gin.H{"status": "error", "message": message})
}

// 3) v1 ret/msg 信封 (clonepay 走文本语义, 见 clonepay.go; 此处备通用 ret:0)

func retError(c *gin.Context, msg string) {
	c.JSON(http.StatusOK, gin.H{"ret": 0, "msg": msg})
}

// 4) v0 死路由 410 桩 (对齐 PHP DeprecatedController::v0)

const v0StoppedAt = "2026-11-12"

// V0Gone v0 已停用接口统一响应: 410 + JSON 说明 + 迁移指引头
func V0Gone(c *gin.Context) {
	path := c.Request.URL.Path
	migration := v0MigrationHint(path)
	c.Header("X-API-Deprecated", "true")
	c.Header("X-API-Stopped-At", v0StoppedAt)
	c.Header("X-API-Migration", migration)
	c.JSON(http.StatusGone, gin.H{
		"ret":        0,
		"deprecated": true,
		"stopped_at": v0StoppedAt,
		"msg":        "接口已停用: " + path + " (v0 API 已于 " + v0StoppedAt + " 停用, 不再提供服务)",
		"migration":  migration,
	})
}

func v0MigrationHint(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/sublink"):
		return "请使用新版订阅端点 GET /rss/{token} (token 见用户中心-订阅管理), 详见 docs/rss-contract.md"
	case strings.HasPrefix(path, "/api/token"), strings.HasPrefix(path, "/api/user"):
		return "请使用 v2 API: GET /api/v2/user (按邮箱或ID查询用户, Bearer muKey), 详见 docs/api2/user.md"
	default:
		return "请使用 v2 API, 详见 docs/api2/README.md"
	}
}

// 5) v1 弃用族 401 直接拒绝 (2026-11-12 决议: 不移植, 直接 401)

// V1Rejected v1 弃用端点统一响应: 401 + 弃用说明
func V1Rejected(c *gin.Context) {
	path := c.Request.URL.Path + appendSuffix(c.Request.Method)
	c.Header("X-API-Deprecated", "true")
	c.Header("X-API-Sunset", "v1-removed")
	c.JSON(http.StatusUnauthorized, gin.H{
		"ret":        0,
		"deprecated": true,
		"msg":        "此 v1 接口已停用 (未移植到 Go 版): " + path + ", 请使用 v2 API, 详见 docs/api2/README.md",
	})
}

func appendSuffix(method string) string {
	if method == "" {
		return ""
	}
	return " [" + method + "]"
}
