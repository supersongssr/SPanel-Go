package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"spanel-go/internal/config"

	"github.com/gin-gonic/gin"
)

// MuKeyMode muKey 鉴权模式（对齐 PHP 两套校验函数）
type MuKeyMode int

const (
	// ModeV2ThreeWay v2 生命周期族口径 (V2ApiController::validateToken):
	// Authorization: Bearer / X-API-Token 头 / token 参数 (GET/POST)
	ModeV2ThreeWay MuKeyMode = iota
	// ModeBearerOnly v2 用户查询口径 (V2\UserController): 仅 Authorization: Bearer 头,
	// 刻意不接受 query 传 key (避免主密钥落入 nginx 日志/浏览器历史)
	ModeBearerOnly
)

// MuKeyAuth muKey 校验中间件。
//
// 多 key: config muKey 支持逗号分隔多 key, 逐 key constant-time 比较 (subtle),
// 与 PHP Mu 中间件 isValidMuKey 口径一致。鉴权失败的响应信封由调用方 handler 决定,
// 本中间件只负责判定并 Abort 401:
//   - 生命周期族 handler 需返回 {"status":"error","message":"invalid_token"} (HTTP 200)
//   - 因此该族路由不挂本中间件, 而是调用 CheckMuKey 函数自判 (见 apiv2_lifecycle.go)
//
// 真正适合挂本中间件的是 data/error 信封族 (/api/v2/*): 失败统一 401 INVALID_TOKEN。
func MuKeyAuth(mode MuKeyMode) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !CheckMuKey(c, mode) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "INVALID_TOKEN", "message": "token is invalid"},
			})
			return
		}
		c.Next()
	}
}

// CheckMuKey 判定请求是否携带有效 muKey (供 handler 自行调用, 失败响应自定义)
func CheckMuKey(c *gin.Context, mode MuKeyMode) bool {
	muKey := ""
	if config.GlobalConfig != nil {
		muKey = config.GlobalConfig.App.MuKey
	}
	if muKey == "" {
		return false
	}

	var token string
	switch mode {
	case ModeV2ThreeWay:
		// 1. Authorization: Bearer
		if t := bearerToken(c.GetHeader("Authorization")); t != "" {
			return muKeyMatch(muKey, t)
		}
		// 2. X-API-Token 头
		if t := strings.TrimSpace(c.GetHeader("X-API-Token")); t != "" {
			return muKeyMatch(muKey, t)
		}
		// 3. token 参数 (GET/POST)
		token = paramToken(c, "token")
		if token == "" {
			return false
		}
		return muKeyMatch(muKey, token)
	case ModeBearerOnly:
		token = bearerToken(c.GetHeader("Authorization"))
		if token == "" {
			return false
		}
		return muKeyMatch(muKey, token)
	default:
		return false
	}
}

// bearerToken 解析 "Bearer xxx" 头 (大小写不敏感, 对齐 PHP preg_match '/Bearer\s+(.*)$/i')
func bearerToken(header string) string {
	if header == "" {
		return ""
	}
	idx := strings.IndexByte(header, ' ')
	if idx < 0 {
		return ""
	}
	scheme := strings.ToLower(strings.TrimSpace(header[:idx]))
	if scheme != "bearer" {
		return ""
	}
	return strings.TrimSpace(header[idx+1:])
}

// paramToken 取 GET/POST 参数 (对齐 Slim getParam)
func paramToken(c *gin.Context, names ...string) string {
	for _, n := range names {
		if v := c.Query(n); v != "" {
			return v
		}
		if v := c.PostForm(n); v != "" {
			return v
		}
	}
	return ""
}

// muKeyMatch 多 key 逗号分隔 + constant-time 比较
func muKeyMatch(muKey, token string) bool {
	for _, key := range strings.Split(muKey, ",") {
		key = strings.TrimSpace(key)
		if key != "" && len(key) == len(token) && subtle.ConstantTimeCompare([]byte(key), []byte(token)) == 1 {
			return true
		}
	}
	return false
}
