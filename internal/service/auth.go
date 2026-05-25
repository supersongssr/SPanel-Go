package service

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"

	"github.com/golang-jwt/jwt/v5"
)

// AuthService 提供认证核心逻辑
type AuthService struct{}

var DefaultAuthService = &AuthService{}

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

// GenerateToken 为当前用户颁发 JWT Token
func (s *AuthService) GenerateToken(user *model.User) (string, error) {
	expireTime := time.Now().Add(time.Duration(config.GlobalConfig.JWT.ExpireHours) * time.Hour)

	// 创建 Claims 载荷
	claims := jwt.MapClaims{
		"user_id":   user.ID,
		"email":     user.Email,
		"is_admin":  user.IsAdmin,
		"exp":       expireTime.Unix(),
		"issued_at": time.Now().Unix(),
	}

	// 使用 HS256 签名算法生成 JWT
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(config.GlobalConfig.JWT.Secret))
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, nil
}
