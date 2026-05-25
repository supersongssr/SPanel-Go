package middleware

import (
	"net/http"

	"spanel-go/internal/config"

	"github.com/gin-gonic/gin"
)

// NodeAuth 节点密钥校验中间件 (muKey 验证)
func NodeAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := ""

		// 1. 尝试从 Header 中的 Token / Key 获取
		token = c.GetHeader("Key")
		if token == "" {
			token = c.GetHeader("Token")
		}

		// 2. 尝试从 Query 参数获取
		if token == "" {
			token = c.Query("key")
		}
		if token == "" {
			token = c.Query("token")
		}

		// 3. 校验密钥恒等性
		if token == "" || token != config.GlobalConfig.App.MuKey {
			// 根据旧版 V2 API 的 Silent Exit 约束：
			// 涉及底层节点探测时，直接截断连接不返回任何可读数据，以防黑客扫描
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		c.Next()
	}
}
