package middleware

import (
	"net/http"
	"strings"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// Auth JWT 统一用户身份验证中间件
func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := ""

		// 1. 尝试从 Authorization Header 获取 Token
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
		}

		// 2. 如果 Header 没有，尝试从 URL Query 获取 Token
		if tokenStr == "" {
			tokenStr = c.Query("token")
		}

		// 3. 校验 Token 存在性
		if tokenStr == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"ret": 0,
				"msg": "未登录，请先登录",
			})
			c.Abort()
			return
		}

		// 4. 解析 JWT Token
		token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
			return []byte(config.GlobalConfig.JWT.Secret), nil
		})

		if err != nil || !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{
				"ret": 0,
				"msg": "身份凭证已过期或无效，请重新登录",
			})
			c.Abort()
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{
				"ret": 0,
				"msg": "身份凭证解析失败",
			})
			c.Abort()
			return
		}

		userIDFloat, ok := claims["user_id"].(float64)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{
				"ret": 0,
				"msg": "凭证格式错误",
			})
			c.Abort()
			return
		}

		userID := int32(userIDFloat)

		// 5. 校验数据库连接
		if database.DB == nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"ret": 0,
				"msg": "系统维护中：数据库连接异常，请稍后重试",
			})
			c.Abort()
			return
		}

		// 6. 从数据库中拉取用户详细状态
		var user model.User
		if err := database.DB.First(&user, userID).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"ret": 0,
				"msg": "用户账户不存在",
			})
			c.Abort()
			return
		}

		// 7. 校验用户是否启用 (enable == 1)
		if user.Enable != 1 {
			c.JSON(http.StatusForbidden, gin.H{
				"ret": 0,
				"msg": "该账户已被封禁或禁用，请联系管理员",
			})
			c.Abort()
			return
		}

		// 8. 注入上下文方便 Handler 直接消费
		c.Set("user", &user)
		c.Set("user_id", user.ID)

		c.Next()
	}
}
