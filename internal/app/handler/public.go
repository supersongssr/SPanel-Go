// Package handler - 用户端公开认证接口 (/api/v1/auth)
//
// 移植基线: PHP AuthController (config/routes.php /auth 组), 决策口径:
//   - PoW 不移植 (§0-7): 防爆破由 图形验证码 + Redis 失败锁定 + IP 限频 承担
//   - 登录态: JWT in HttpOnly Cookie + token_version 吊销 (§4.0 方案 B)
//   - 密码哈希 sha256/md5+salt 与 PHP 共库兼容
package handler

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/captcha"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/mailer"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type PublicHandler struct{}

var DefaultPublicHandler = &PublicHandler{}

// ---- 请求体 ----

type LoginRequest struct {
	Email       string `json:"email" binding:"required,email"`
	Password    string `json:"password" binding:"required"`
	Code        string `json:"code"` // 2FA (ga_enable=1 时必填)
	RememberMe  bool   `json:"remember_me"`
	CaptchaID   string `json:"captchaId"`
	CaptchaCode string `json:"captchaCode"`
}

type RegisterRequest struct {
	Email       string `json:"email" binding:"required,email"`
	Password    string `json:"password" binding:"required"`
	Repassword  string `json:"repassword"`
	UserName    string `json:"userName"`
	ImType      int    `json:"imType"`
	ImValue     string `json:"imValue"`
	Code        string `json:"code"`      // 邀请码
	EmailCode   string `json:"emailCode"` // 邮箱验证码
	CaptchaID   string `json:"captchaId"`
	CaptchaCode string `json:"captchaCode"`
}

type SendCodeRequest struct {
	Email       string `json:"email" binding:"required,email"`
	CaptchaID   string `json:"captchaId"`
	CaptchaCode string `json:"captchaCode"`
}

// ---- 配置缺省口径 ----

func registerMode() string {
	if m := config.GlobalConfig.Security.RegisterMode; m != "" {
		return m
	}
	return "open"
}

func emailVerifyTTL() int {
	if t := config.GlobalConfig.Security.EmailVerifyTTL; t > 0 {
		return t
	}
	return 600
}

func emailVerifyIPLimit() int {
	if n := config.GlobalConfig.Security.EmailVerifyIPLimit; n > 0 {
		return n
	}
	return 5
}

// ---- 失败锁定 (PoW 移除后的等价替代, §6.1) ----

const (
	loginFailThreshold  = 5               // 同 email+ip 连续失败 5 次
	loginFailLockSec    = 15 * 60         // 锁 15 分钟
	loginIPFailMaxPerHr = 50              // 单 IP 每小时失败上限
)

func loginFailKey(email, ip string) string  { return "login_fail:" + email + ":" + ip }
func loginIPFailKey(ip string) string       { return "login_fail_ip:" + ip }

// isLoginLocked 查询失败锁定状态 (Redis 不可用时放行, 与 captcha 降级口径一致)
func isLoginLocked(ctx context.Context, email, ip string) bool {
	if database.Redis == nil {
		return false
	}
	n, err := database.Redis.Get(ctx, loginFailKey(email, ip)).Int()
	if err == nil && n >= loginFailThreshold {
		return true
	}
	n2, err := database.Redis.Get(ctx, loginIPFailKey(ip)).Int()
	if err == nil && n2 >= loginIPFailMaxPerHr {
		return true
	}
	return false
}

// recordLoginFail 记一次失败; 达阈值自动带锁定 TTL
func recordLoginFail(ctx context.Context, email, ip string) {
	if database.Redis == nil {
		return
	}
	n, _ := database.Redis.Incr(ctx, loginFailKey(email, ip)).Result()
	if n == 1 {
		database.Redis.Expire(ctx, loginFailKey(email, ip), time.Duration(loginFailLockSec*4)*time.Second)
	}
	if n >= loginFailThreshold {
		database.Redis.Expire(ctx, loginFailKey(email, ip), time.Duration(loginFailLockSec)*time.Second)
	}
	n2, _ := database.Redis.Incr(ctx, loginIPFailKey(ip)).Result()
	if n2 == 1 {
		database.Redis.Expire(ctx, loginIPFailKey(ip), time.Hour)
	}
}

func clearLoginFail(ctx context.Context, email, ip string) {
	if database.Redis == nil {
		return
	}
	database.Redis.Del(ctx, loginFailKey(email, ip))
}

// writeLoginIp 登录足迹审计 (type 0 成功 / 1 失败; PHP LoginIp 同口径)
func writeLoginIp(userID int64, ip string, typ int32) {
	if database.DB == nil {
		return
	}
	database.DB.Create(&model.LoginIP{
		UserID:   userID,
		IP:       ip,
		DateTime: time.Now().Unix(),
		Type:     typ,
	})
}

// ---- 图形验证码 ----

// Captcha 获取图形验证码 SVG (根据全局配置动态开闭)
func (h *PublicHandler) Captcha(c *gin.Context) {
	if !config.GlobalConfig.Security.EnableRegCaptcha && !config.GlobalConfig.Security.EnableLoginCaptcha {
		c.JSON(http.StatusOK, gin.H{
			"ret":                  1,
			"msg":                  "无需验证码",
			"captcha_provider":     "none",
			"enable_reg_captcha":   false,
			"enable_login_captcha": false,
			"enable_email_verify":  config.GlobalConfig.Security.EnableEmailVerify,
		})
		return
	}

	captchaId := captcha.GenerateRandomUUID()
	code := captcha.GenerateRandomCode()
	svg := captcha.GenerateCaptchaSVG(code)

	captcha.StoreCaptchaCode(captchaId, code)

	c.JSON(http.StatusOK, gin.H{
		"ret":                  1,
		"msg":                  "获取成功",
		"captcha_provider":     "graphic",
		"captchaId":            captchaId,
		"svg":                  svg,
		"enable_reg_captcha":   config.GlobalConfig.Security.EnableRegCaptcha,
		"enable_login_captcha": config.GlobalConfig.Security.EnableLoginCaptcha,
		"enable_email_verify":  config.GlobalConfig.Security.EnableEmailVerify,
	})
}

// ---- 登录 ----

// Login 用户登录 (PoW 移除后防爆破链: 验证码 → 失败锁定 → 密码 → 2FA → LoginIp 审计)
func (h *PublicHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "输入格式有误，请输入有效的邮箱与密码"})
		return
	}

	ctx := context.Background()
	ip := c.ClientIP()

	// 1. 失败锁定前置检查
	if isLoginLocked(ctx, req.Email, ip) {
		c.JSON(http.StatusTooManyRequests, gin.H{"ret": 0, "msg": "登录失败次数过多，账号已临时锁定 15 分钟，请稍后再试"})
		return
	}

	// 2. 图形验证码 (开关可控)
	if config.GlobalConfig.Security.EnableLoginCaptcha {
		if !captcha.VerifyCaptchaCode(req.CaptchaID, req.CaptchaCode) {
			c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "图形验证码输入错误或已失效，请重新输入"})
			return
		}
	}

	// 3. 数据库连接
	if database.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "系统维护中：数据库连接异常，请稍后重试"})
		return
	}

	fail := func(userID int64, msg string, status int) {
		recordLoginFail(ctx, req.Email, ip)
		if userID > 0 {
			writeLoginIp(userID, ip, 1)
		}
		c.JSON(status, gin.H{"ret": 0, "msg": msg})
	}

	// 4. 检索用户 (统一文案防枚举)
	var user model.User
	if err := database.DB.Where("email = ?", req.Email).First(&user).Error; err != nil {
		fail(0, "邮箱或密码错误，请检查后重试", http.StatusUnauthorized)
		return
	}

	// 5. 密码
	if !service.DefaultAuthService.VerifyPassword(&user, req.Password) {
		fail(int64(user.ID), "邮箱或密码错误，请检查后重试", http.StatusUnauthorized)
		return
	}

	// 6. 被禁用户禁止登录
	if user.Enable != 1 {
		fail(int64(user.ID), "该账户已被限制登录，请联系客服", http.StatusForbidden)
		return
	}

	// 7. 2FA (ga_enable=1 时强制 TOTP, ±30s 容差; 重置密码可关闭 2FA 作为逃生通道)
	if user.GaEnable == 1 {
		if req.Code == "" {
			c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "该账户已开启两步验证，请输入 6 位动态验证码"})
			return
		}
		if !verifyTOTP(user.GaToken, req.Code) {
			fail(int64(user.ID), "两步验证码错误，请确认时间同步后重试", http.StatusBadRequest)
			return
		}
	}

	// 8. 签发登录态 (§4.0: HttpOnly Cookie + tv claim; remember_me 两态 TTL)
	ttl := service.DefaultAuthService.LoginTTL(req.RememberMe)
	token, err := service.DefaultAuthService.GenerateTokenEx(&user, ttl, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "登录会话生成失败，请重试"})
		return
	}
	service.DefaultAuthService.SetAuthCookie(c, token, ttl)

	// 9. 成功: 清失败计数 + 写成功足迹
	clearLoginFail(ctx, req.Email, ip)
	writeLoginIp(int64(user.ID), ip, 0)

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "登录成功",
		"data": gin.H{
			"token": token, // 兼容输出 (Bearer 通道用); 主通道为 HttpOnly Cookie
			"user": gin.H{
				"id":        user.ID,
				"user_name": user.UserName,
				"userName":  user.UserName,
				"email":     user.Email,
				"is_admin":  user.IsAdmin,
				"isAdmin":   user.IsAdmin == 1,
				"class":     user.Class,
			},
		},
	})
}

// ---- 注册 ----

// Register 用户注册 (邀请码全量校验 + EV 验证码 + IP 限频 + 返利 v5 + 自动登录)
func (h *PublicHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "注册参数错误"})
		return
	}

	// 1. 注册模式 (open/invite/close)
	mode := registerMode()
	if mode == "close" {
		c.JSON(http.StatusForbidden, gin.H{"ret": 0, "msg": "本站已关闭注册通道"})
		return
	}
	if mode == "invite" && req.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "本站为邀请制注册，请填写邀请码"})
		return
	}

	// 2. 图形验证码
	if config.GlobalConfig.Security.EnableRegCaptcha {
		if !captcha.VerifyCaptchaCode(req.CaptchaID, req.CaptchaCode) {
			c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "图形验证码错误或已过期，请点击验证码图片刷新重试"})
			return
		}
	}

	// 3. 邮箱验证码 (enable_email_verify 开启时闭环校验)
	if config.GlobalConfig.Security.EnableEmailVerify {
		if req.EmailCode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "请输入邮箱中收到的激活验证码"})
			return
		}
		var ev model.EmailVerify
		if err := database.DB.Where("email = ? AND code = ? AND expire_in > ?", req.Email, req.EmailCode, time.Now().Unix()).First(&ev).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "邮箱验证码错误或已过期，请重新获取"})
			return
		}
	}

	if database.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "系统维护中：数据库连接异常，请稍后重试"})
		return
	}

	// 4. 邮箱查重
	var existing model.User
	if err := database.DB.Where("email = ?", req.Email).First(&existing).Error; err == nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "该邮箱已存在注册记录，请直接登录"})
		return
	}

	// 5. 密码强度 (PHP 口径 ≥8 位, 两次一致)
	if len(req.Password) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "密码长度至少 8 位"})
		return
	}
	if req.Repassword != "" && req.Repassword != req.Password {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "两次输入的密码不一致"})
		return
	}

	// 6. IP 注册限频 (PHP 口径: 前缀匹配 reg_ip, 10min>1 / 24h>7)
	ipPrefix := c.ClientIP()
	if len(ipPrefix) > 24 {
		ipPrefix = ipPrefix[:24]
	}
	now := time.Now().Unix()
	var cnt10m, cnt24h int64
	database.DB.Model(&model.User{}).Where("reg_ip LIKE ? AND reg_date > ?", ipPrefix+"%", time.Unix(now-600, 0)).Count(&cnt10m)
	database.DB.Model(&model.User{}).Where("reg_ip LIKE ? AND reg_date > ?", ipPrefix+"%", time.Unix(now-86400, 0)).Count(&cnt24h)
	if cnt10m > 1 || cnt24h > 7 {
		c.JSON(http.StatusTooManyRequests, gin.H{"ret": 0, "msg": "该网络环境注册过于频繁，请稍后再试"})
		return
	}

	// 7. 邀请码全量校验 (同码取 id 最大; 邀请人须存在/启用/有邀请次数; 兼容直连 UID)
	refBy := int32(0)
	if req.Code != "" {
		var inviteCode model.InviteCode
		if err := database.DB.Where("code = ?", req.Code).Order("id DESC").First(&inviteCode).Error; err == nil {
			var inviter model.User
			if err := database.DB.First(&inviter, inviteCode.UserID).Error; err != nil || inviter.Enable != 1 || inviter.InviteNum < 1 {
				c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "邀请码无效或邀请人名额已用尽"})
				return
			}
			refBy = inviter.ID
		} else {
			var refUser model.User
			if err := database.DB.Where("id = ?", req.Code).First(&refUser).Error; err == nil {
				refBy = refUser.ID
			} else if mode == "invite" {
				c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "邀请码不存在，请核对后重试"})
				return
			}
		}
	} else if mode == "invite" {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "本站为邀请制注册，请填写邀请码"})
		return
	}

	// 8. 组装用户 (ga_token 注册即建 Base32 密钥; edu.cn 标记; 返利 v5 初始口径)
	userName := req.UserName
	if userName == "" {
		parts := strings.Split(req.Email, "@")
		userName = parts[0]
	}

	isEdu := "0"
	if strings.HasSuffix(strings.ToLower(req.Email), ".edu.cn") {
		isEdu = "1"
	}

	money := "0.00"
	if refBy > 0 && config.GlobalConfig.Invite.InviteGiftMoney > 0 {
		money = fmt.Sprintf("%.2f", config.GlobalConfig.Invite.InviteGiftMoney)
	}

	refRebateStatus := int32(0)
	if refBy > 0 {
		refRebateStatus = 1 // v5: 待被邀人累计充值达标后由 InviteRebate::onRecharge 发放
	}

	hashedPassword := service.DefaultAuthService.SHA256Hash(req.Password, config.GlobalConfig.App.Salt)

	user := model.User{
		UserName:       userName,
		Email:          req.Email,
		Pass:           hashedPassword,
		Passwd:         generateRandomStr(8),
		Port:           findAvailablePort(),
		TransferEnable: 100 * 1024 * 1024 * 1024, // 默认 100GB
		Enable:         1,
		Class:          1,
		ExpireIn:       time.Now().AddDate(1, 0, 0),
		ClassExpire:    time.Now().AddDate(1, 0, 0),
		Money:          money,
		RegDate:        time.Now(),
		RefBy:          refBy,
		Method:         "aes-256-gcm",
		RegIP:          c.ClientIP(),
		NodeSpeedlimit: "0.00",
		V2rayUUID:      generateUUID(),
		Protocol:       "origin",
		Obfs:           "plain",
		SubLimit:       0,
		Theme:          "material",
		GaToken:        generateBase32Secret(16),
		GaEnable:       0,
		IsEdu:          isEdu,
		RefRebateStatus: refRebateStatus,
	}
	if req.ImType > 0 {
		user.IMType = int32(req.ImType)
		user.IMValue = req.ImValue
	}

	if err := database.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "创建用户失败：" + err.Error()})
		return
	}

	// 9. 返利 v5: 邀请人 invite_num-1 (原子条件更新防并发超发)
	if refBy > 0 {
		database.DB.Model(&model.User{}).Where("id = ? AND invite_num > 0", refBy).
			UpdateColumn("invite_num", gorm.Expr("invite_num - 1"))
	}

	// 10. EV 验证码核销 (注册成功即删除该邮箱全部记录)
	if config.GlobalConfig.Security.EnableEmailVerify {
		database.DB.Where("email = ?", req.Email).Delete(&model.EmailVerify{})
	}

	// 11. 默认订阅建档 (type=11)
	subToken := generateRandomStr(16)
	database.DB.Create(&model.Link{Type: 11, UserID: int64(user.ID), Token: subToken})

	// 12. 自动登录 (Set-Cookie, §4.0)
	ttl := service.DefaultAuthService.LoginTTL(false)
	token, err := service.DefaultAuthService.GenerateTokenEx(&user, ttl, 0)
	if err == nil {
		service.DefaultAuthService.SetAuthCookie(c, token, ttl)
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "注册成功，已自动登录",
	})
}

// ---- 邮箱验证码 ----

// SendCode 发送注册邮箱验证码 (真实落库 EmailVerify + SMTP 下发; 限频/失败回滚)
func (h *PublicHandler) SendCode(c *gin.Context) {
	var req SendCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "邮箱参数错误"})
		return
	}

	// 1. 开关门禁 (未开启邮箱验证时直接引导注册)
	if !config.GlobalConfig.Security.EnableEmailVerify {
		c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "当前环境已免邮箱验证，您可以直接点注册！"})
		return
	}

	// 2. 图形验证码
	if config.GlobalConfig.Security.EnableRegCaptcha {
		if !captcha.VerifyCaptchaCode(req.CaptchaID, req.CaptchaCode) {
			c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "图形验证码错误，请重新输入"})
			return
		}
	}

	if database.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "系统维护中：数据库连接异常，请稍后重试"})
		return
	}

	// 3. 已注册邮箱不发码
	var existing model.User
	if err := database.DB.Where("email = ?", req.Email).First(&existing).Error; err == nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "该邮箱已注册，请直接登录或找回密码"})
		return
	}

	// 4. 限频: 同 IP 在途数 + 同邮箱单条在途 (PHP 同口径)
	ip := c.ClientIP()
	now := time.Now().Unix()
	var ipInflight, emailInflight int64
	database.DB.Model(&model.EmailVerify{}).Where("ip = ? AND expire_in > ?", ip, now).Count(&ipInflight)
	if ipInflight >= int64(emailVerifyIPLimit()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"ret": 0, "msg": "该 IP 验证码请求过于频繁，请稍后再试"})
		return
	}
	database.DB.Model(&model.EmailVerify{}).Where("email = ? AND expire_in > ?", req.Email, now).Count(&emailInflight)
	if emailInflight >= 1 {
		c.JSON(http.StatusTooManyRequests, gin.H{"ret": 0, "msg": "验证码已发送，请查收邮箱（注意垃圾箱），稍后可重新获取"})
		return
	}

	// 5. 生成 6 位数字码并落库
	code := fmt.Sprintf("%06d", generateRandInt(0, 999999))
	ev := model.EmailVerify{
		Email:    req.Email,
		IP:       ip,
		Code:     code,
		ExpireIn: now + int64(emailVerifyTTL()),
	}
	if err := database.DB.Create(&ev).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "验证码写入失败，请稍后重试"})
		return
	}

	// 6. 发送邮件 (失败回滚删除, 防锁死)
	ttlMin := emailVerifyTTL() / 60
	if ttlMin < 1 {
		ttlMin = 1
	}
	subject := config.GlobalConfig.App.Name + " 注册验证码"
	body := fmt.Sprintf(`<div style="max-width:520px;margin:0 auto;font-family:sans-serif">
<h2 style="color:#6366f1">%s</h2>
<p>您好，您正在注册 %s 账户，本次验证码为：</p>
<p style="font-size:32px;font-weight:700;letter-spacing:8px;color:#6366f1">%s</p>
<p>验证码 %d 分钟内有效。如非本人操作请忽略本邮件。</p>
</div>`, config.GlobalConfig.App.Name, config.GlobalConfig.App.Name, code, ttlMin)
	if err := mailer.Send(mailer.Mail{To: req.Email, Subject: subject, HTMLBody: body}); err != nil {
		database.DB.Delete(&ev)
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "邮件发送失败，请稍后重试或联系管理员"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "验证码已发送至您的邮箱，请注意查收"})
}

// ---- 忘记密码 / 重置 ----

// PasswordReset 发起密码重置 (24h 一条; 邮件带 /auth/token.html?token= 链接)
func (h *PublicHandler) PasswordReset(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "请输入有效的邮箱地址"})
		return
	}

	// 图形验证码 (复用注册开关)
	if config.GlobalConfig.Security.EnableRegCaptcha {
		if !captcha.VerifyCaptchaCode(c.Query("captchaId"), c.Query("captchaCode")) {
			c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "图形验证码错误"})
			return
		}
	}

	if database.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "系统维护中，请稍后重试"})
		return
	}

	var user model.User
	if err := database.DB.Where("email = ?", req.Email).First(&user).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "该邮箱未注册"})
		return
	}

	// 24h 内仅限一次申请
	now := time.Now().Unix()
	var pending model.PasswordReset
	if err := database.DB.Where("email = ? AND expire_time > ?", req.Email, now-int64(86400)).First(&pending).Error; err == nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "24 小时内仅限申请一次密码重置，请查收邮件或明日再试"})
		return
	}

	token := generateTokenHex(32)
	pr := model.PasswordReset{
		Email:      req.Email,
		Token:      token,
		InitTime:   now,
		ExpireTime: now + 86400,
	}
	if err := database.DB.Create(&pr).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "申请失败，请稍后重试"})
		return
	}

	baseURL := baseURLFromRequest(c)
	link := baseURL + "/auth/token.html?token=" + token
	subject := config.GlobalConfig.App.Name + " 密码重置"
	body := fmt.Sprintf(`<div style="max-width:520px;margin:0 auto;font-family:sans-serif">
<h2 style="color:#6366f1">%s 密码重置</h2>
<p>您好，我们收到了您账户的密码重置请求：</p>
<p><a href="%s" style="display:inline-block;background:#6366f1;color:#fff;padding:12px 32px;border-radius:8px;text-decoration:none;font-weight:600">点击重置密码</a></p>
<p style="word-break:break-all;color:#94a3b8;font-size:12px">若按钮无法点击，请复制链接：%s</p>
<p>链接 24 小时内有效。如非本人操作请忽略本邮件。</p>
</div>`, config.GlobalConfig.App.Name, link, link)
	if err := mailer.Send(mailer.Mail{To: req.Email, Subject: subject, HTMLBody: body}); err != nil {
		database.DB.Delete(&pr)
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "邮件发送失败，请稍后重试或联系管理员"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "重置邮件已发送，请查收邮箱（注意垃圾箱）"})
}

// PasswordToken 执行密码重置 (校验 token → 更新 pass + 关 2FA + tv+1 全端下线 + 清订阅)
func (h *PublicHandler) PasswordToken(c *gin.Context) {
	tokenStr := c.Param("token")
	var req struct {
		Password   string `json:"password" binding:"required"`
		Repassword string `json:"repassword"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "请输入新密码"})
		return
	}

	if len(req.Password) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "密码长度至少 8 位"})
		return
	}
	if req.Repassword != "" && req.Repassword != req.Password {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "两次输入的密码不一致"})
		return
	}

	if database.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "系统维护中，请稍后重试"})
		return
	}

	// token 存在且在期
	var pr model.PasswordReset
	now := time.Now().Unix()
	if err := database.DB.Where("token = ? AND expire_time > ?", tokenStr, now).First(&pr).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "重置链接无效或已过期，请重新申请"})
		return
	}

	var user model.User
	if err := database.DB.Where("email = ?", pr.Email).First(&user).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "账户不存在"})
		return
	}

	newHash := service.DefaultAuthService.SHA256Hash(req.Password, config.GlobalConfig.App.Salt)
	if err := database.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
		"pass":          newHash,
		"ga_enable":     0, // PHP 语义: 重置密码即关闭 2FA (逃生通道)
		"token_version": user.TokenVersion + 1, // 旧登录态全端失效
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "重置失败，请稍后重试"})
		return
	}

	// 清订阅 Link (PHP clean_link)
	database.DB.Where("userid = ?", user.ID).Delete(&model.Link{})
	// 核销 token
	database.DB.Delete(&pr)

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "密码重置成功，请使用新密码登录"})
}

// ---- 登出 ----

// Logout 登出 (服务端过期 HttpOnly Cookie; JWT 无黑名单, tv 机制兜底)
func (h *PublicHandler) Logout(c *gin.Context) {
	service.DefaultAuthService.ClearAuthCookie(c)
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "已安全退出"})
}

// ---- 辅助函数 ----

// baseURLFromRequest 从请求推导站点根 URL (尊重反代头)
func baseURLFromRequest(c *gin.Context) string {
	scheme := "https"
	if c.Request.TLS == nil && c.GetHeader("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	return scheme + "://" + c.Request.Host
}

// generateBase32Secret 生成 TOTP Base32 密钥 (RFC 4648 字母表, 与 PHP GA::createSecret 同源)
func generateBase32Secret(length int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	b := make([]byte, length)
	for i := range b {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			b[i] = 'A'
		} else {
			b[i] = alphabet[num.Int64()]
		}
	}
	return string(b)
}

// generateTokenHex 生成 n 字节随机数的十六进制串 (2n 字符)
func generateTokenHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// generateRandInt 区间随机整数 [min, max]
func generateRandInt(min, max int) int {
	diff := max - min + 1
	num, err := rand.Int(rand.Reader, big.NewInt(int64(diff)))
	if err != nil {
		return min
	}
	return min + int(num.Int64())
}

// generateRandomStr 生成指定长度的强随机字符串 (SS 连接密码)
func generateRandomStr(length int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			b[i] = 'a'
		} else {
			b[i] = letters[num.Int64()]
		}
	}
	return string(b)
}

// generateUUID 生成标准 v4 UUID (V2Ray 用户识别码)
func generateUUID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		return "00000000-0000-0000-0000-000000000000"
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// findAvailablePort 查找数据库中未被占用的端口 (MAX(port)+1)
func findAvailablePort() int32 {
	var maxPort int32
	row := database.DB.Table("user").Select("MAX(port)").Row()
	if row != nil {
		_ = row.Scan(&maxPort)
	}
	if maxPort < 10000 {
		return 10001
	}
	return maxPort + 1
}
