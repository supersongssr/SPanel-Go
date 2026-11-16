package middleware

import (
	"net/http"
	"strings"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// enableWhitelist enable==0 用户仍可访问的端点后缀 (PHP Auth 中间件豁免名单等价:
// disable/reactive/logout/backtoadmin 系列; 其余 → 403 + 前端跳 /user/disable.html)
var enableWhitelist = []string{
	"/api/v1/user/disable",
	"/api/v1/user/reactive",
	"/api/v1/user/logout",
	"/api/v1/user/back-to-admin",
	"/api/v1/user/profile", // 禁用页需要展示基础信息
}

// Auth 统一用户身份验证中间件 (§4.0 方案 B):
//  1. HttpOnly Cookie `spanel_token` 优先 (浏览器同源自动携带)
//  2. Authorization: Bearer <jwt> 兜底 (调试/未来 API 客户端)
//  3. ?token= 查询参数最后兜底 (getiosconf 下载直链)
//
// tv (token_version) 比对: 中间件每请求本就拉取 user 行, 顺手比对 claims.tv,
// 不一致 → 401 (改密/重置/管理员踢下线已 tv+1, 旧凭证即时全端失效)。
func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := ""

		// 1. HttpOnly Cookie 优先
		if ck, err := c.Request.Cookie(service.CookieName); err == nil && ck.Value != "" {
			tokenStr = ck.Value
		}

		// 2. Authorization Header 兜底
		if tokenStr == "" {
			authHeader := c.GetHeader("Authorization")
			if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
				tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		// 3. URL Query 最后兜底
		if tokenStr == "" {
			tokenStr = c.Query("token")
		}

		if tokenStr == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"ret": 0,
				"msg": "未登录，请先登录",
			})
			c.Abort()
			return
		}

		// 4. 解析 JWT
		token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
			return []byte(config.GlobalConfig.JWT.Secret), nil
		})
		if err != nil || !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{
				"ret": 0,
				"msg": "登录态已过期或无效，请重新登录",
			})
			c.Abort()
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{
				"ret": 0,
				"msg": "登录态解析失败",
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

		if database.DB == nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"ret": 0,
				"msg": "系统维护中：数据库连接异常，请稍后重试",
			})
			c.Abort()
			return
		}

		// 5. 拉取用户行 (顺带完成 tv 吊销比对, 零额外查询)
		var user model.User
		if err := database.DB.First(&user, userID).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"ret": 0,
				"msg": "用户账户不存在",
			})
			c.Abort()
			return
		}

		// 6. token_version 比对 (§4.0: 不等 = 凭证已被吊销)
		if tvFloat, has := claims["tv"].(float64); has {
			if int32(tvFloat) != user.TokenVersion {
				c.JSON(http.StatusUnauthorized, gin.H{
					"ret": 0,
					"msg": "登录态已失效（密码已变更或被强制下线），请重新登录",
				})
				c.Abort()
				return
			}
		}

		// 7. enable 校验: 被禁用户仅可触达豁免端点, 其余 403 + 前端跳禁用引导页
		if user.Enable != 1 {
			path := c.Request.URL.Path
			allowed := false
			for _, w := range enableWhitelist {
				if strings.HasPrefix(path, w) {
					allowed = true
					break
				}
			}
			if !allowed {
				c.JSON(http.StatusForbidden, gin.H{
					"ret":  0,
					"msg":  "该账户已被封禁或禁用",
					"data": gin.H{"redirect": "/user/disable.html"},
				})
				c.Abort()
				return
			}
		}

		// 8. 注入上下文
		c.Set("user", &user)
		c.Set("user_id", user.ID)
		c.Set("claims", claims)

		c.Next()
	}
}
