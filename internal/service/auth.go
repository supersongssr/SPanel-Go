package service

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// AuthService 提供认证核心逻辑
type AuthService struct{}

var DefaultAuthService = &AuthService{}

// CookieName 登录态 HttpOnly Cookie 名 (§4.0 方案 B)
const CookieName = "spanel_token"

// MD5Hash 进行含盐的 MD5 签名计算
func (s *AuthService) MD5Hash(pwd, salt string) string {
	h := md5.New()
	h.Write([]byte(pwd + salt))
	return hex.EncodeToString(h.Sum(nil))
}

// SHA256Hash 进行含盐的 SHA256 签名计算
func (s *AuthService) SHA256Hash(pwd, salt string) string {
	h := sha256.New()
	h.Write([]byte(pwd + salt))
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyPassword 校验用户密码哈希 (支持 md5 / sha256)
func (s *AuthService) VerifyPassword(user *model.User, rawPassword string) bool {
	salt := config.GlobalConfig.App.Salt
	pwdHash := ""

	// 匹配旧版密码哈希模式
	pwdHash = s.SHA256Hash(rawPassword, salt)
	if user.Pass == pwdHash {
		return true
	}

	// 降级匹配 md5
	pwdHash = s.MD5Hash(rawPassword, salt)
	return user.Pass == pwdHash
}

// GenerateToken 为当前用户颁发 JWT Token (默认 TTL 来自 jwt.expire_hours)
func (s *AuthService) GenerateToken(user *model.User) (string, error) {
	return s.GenerateTokenEx(user, 0, 0)
}

// GenerateTokenEx 颁发 JWT (§4.0 方案 B):
//   - ttlSeconds <= 0 → 使用配置 jwt.expire_hours
//   - actAsAdmin > 0 → 附带 act_as_admin claim (管理员 masquerade 切换身份,
//     /api/v1/user/back-to-admin 依据它重签管理员 cookie, 无服务端状态)
//
// 载荷含 tv (user.token_version), 中间件每请求比对实现即时吊销。
func (s *AuthService) GenerateTokenEx(user *model.User, ttlSeconds int64, actAsAdmin int32) (string, error) {
	var expireTime time.Time
	if ttlSeconds > 0 {
		expireTime = time.Now().Add(time.Duration(ttlSeconds) * time.Second)
	} else {
		expireTime = time.Now().Add(time.Duration(config.GlobalConfig.JWT.ExpireHours) * time.Hour)
	}

	claims := jwt.MapClaims{
		"user_id":   user.ID,
		"email":     user.Email,
		"is_admin":  user.IsAdmin,
		"tv":        user.TokenVersion,
		"exp":       expireTime.Unix(),
		"issued_at": time.Now().Unix(),
	}
	if actAsAdmin > 0 {
		claims["act_as_admin"] = actAsAdmin
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(config.GlobalConfig.JWT.Secret))
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, nil
}

// SetAuthCookie 颁发登录态 HttpOnly Cookie (SameSite=Lax; 本地 http 部署自动降级去掉 Secure)
func (s *AuthService) SetAuthCookie(c *gin.Context, token string, ttlSeconds int64) {
	maxAge := int(ttlSeconds)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearAuthCookie 过期清除登录态 Cookie (登出)
func (s *AuthService) ClearAuthCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// LoginTTL remember_me 两态 TTL (PHP 口径: 默认 1 天, 勾选 32 天)
func (s *AuthService) LoginTTL(rememberMe bool) int64 {
	if rememberMe {
		return int64(32 * 24 * 3600)
	}
	return int64(24 * 3600)
}
