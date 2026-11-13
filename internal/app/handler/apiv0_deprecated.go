package handler

import "github.com/gin-gonic/gin"

// APIv0DeprecatedHandler v0 已停用接口处理器 (对齐 PHP DeprecatedController)
//
// 生产实证 50 天 0 调用的 v0 死路由, 一律 410 + 停用说明, 不进入任何业务逻辑:
//
//	GET  /api/token/{token}   TokenStorage 查询 (方法体已被 PHP 注释, 调用即 500)
//	POST /api/token           邮箱密码换 token (死路由)
//	GET  /api/node            节点列表 (死路由)
//	GET  /api/user/{id}       用户信息 (恒 not implemented 占位)
//	GET  /api/sublink         老客户端取订阅地址 (返回值已切 /rss/)
type APIv0DeprecatedHandler struct{}

var DefaultAPIv0DeprecatedHandler = &APIv0DeprecatedHandler{}

// Token GET/POST /api/token* → 410
func (h *APIv0DeprecatedHandler) Token(c *gin.Context) { V0Gone(c) }

// Node GET /api/node → 410
func (h *APIv0DeprecatedHandler) Node(c *gin.Context) { V0Gone(c) }

// UserInfo GET /api/user/:id → 410 (v2 GET /api/v2/user 已接替)
func (h *APIv0DeprecatedHandler) UserInfo(c *gin.Context) { V0Gone(c) }

// SubLink GET /api/sublink → 410 (新版订阅 GET /rss/{token})
func (h *APIv0DeprecatedHandler) SubLink(c *gin.Context) { V0Gone(c) }
