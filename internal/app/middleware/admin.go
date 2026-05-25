package middleware

import (
	"net/http"

	"spanel-go/internal/model"

	"github.com/gin-gonic/gin"
)

// Admin 管理员校验中间件 (必须悬挂在 Auth 之后)
func Admin() gin.HandlerFunc {
	return func(c *gin.Context) {
		userVal, exists := c.Get("user")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{
				"ret": 0,
				"msg": "未授权的访问，请先登录",
			})
			c.Abort()
			return
		}

		user, ok := userVal.(*model.User)
		if !ok || user.IsAdmin != 1 {
			c.JSON(http.StatusForbidden, gin.H{
				"ret": 0,
				"msg": "权限不足，只有系统管理员有权执行此操作",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}
