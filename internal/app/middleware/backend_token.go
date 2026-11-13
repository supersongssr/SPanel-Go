package middleware

import (
	"net/http"
	"strings"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/netutil"

	"github.com/gin-gonic/gin"
)

// BackendToken v2 后端节点 API 鉴权中间件 (/api/v2/backend/*)。
//
// 契约 (对齐 PHP app/Middleware/BackendToken.php):
//   - 标准 Bearer token, 每节点一个独立 token (ss_node.api_token 唯一索引精确匹配);
//   - 长度守卫: 空 / >64 直接 401, 不进库;
//   - sort=999 (死池/回收, 管理员显式下线) → 403 NODE_DISABLED;
//     节点流量耗尽不算禁用 (契约要求 GET /users 返回 200+空列表);
//   - api_ip_allowlist 非空时校验直连 IP (逗号分隔 IP/CIDR), 不匹配 → 401;
//   - 鉴权成功把 *model.Node 挂到 c.Set("node")。
func BackendToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c.GetHeader("Authorization"))

		if token == "" || len(token) > 64 {
			abortBackendErr(c, http.StatusUnauthorized, "INVALID_TOKEN", "token is invalid or revoked")
			return
		}

		if database.DB == nil {
			abortBackendErr(c, http.StatusInternalServerError, "INTERNAL", "database is offline")
			return
		}

		var node model.Node
		if err := database.DB.Where("api_token = ?", token).First(&node).Error; err != nil {
			abortBackendErr(c, http.StatusUnauthorized, "INVALID_TOKEN", "token is invalid or revoked")
			return
		}

		// 管理员禁用 (死池/回收): 节点等待面板恢复
		if node.Sort == 999 {
			abortBackendErr(c, http.StatusForbidden, "NODE_DISABLED", "node is disabled")
			return
		}

		// 可选 IP 白名单 (纵深防御): 直连地址, 绝不信任 XFF
		if allow := strings.TrimSpace(node.APIIPAllowlist); allow != "" {
			if !netutil.IPAllowed(netutil.RemoteIP(c.Request), allow) {
				abortBackendErr(c, http.StatusUnauthorized, "INVALID_TOKEN", "token is invalid or revoked")
				return
			}
		}

		c.Set("node", &node)
		c.Next()
	}
}

func abortBackendErr(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{
		"error": gin.H{"code": code, "message": message},
	})
}

// NodeFromContext 取 BackendToken 挂载的节点 (handler 侧使用)
func NodeFromContext(c *gin.Context) *model.Node {
	if v, ok := c.Get("node"); ok {
		if n, ok := v.(*model.Node); ok {
			return n
		}
	}
	return nil
}
