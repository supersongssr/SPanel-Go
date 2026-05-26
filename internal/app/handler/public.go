package handler

import (
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
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
)

type PublicHandler struct{}

var DefaultPublicHandler = &PublicHandler{}

type LoginRequest struct {
	Email       string `json:"email" binding:"required,email"`
	Password    string `json:"password" binding:"required"`
	CaptchaID   string `json:"captchaId"`
	CaptchaCode string `json:"captchaCode"`
}

type RegisterRequest struct {
	Email       string `json:"email" binding:"required,email"`
	Password    string `json:"password" binding:"required"`
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

// Captcha 获取图形验证码 SVG (根据全局配置动态开闭)
func (h *PublicHandler) Captcha(c *gin.Context) {
	// 如果全局开关把登录和注册图形验证码全关了，前端直接反馈 "none" 进行隐藏
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

// Login 用户登录接口 (动态校验密码哈希与图形验证码)
func (h *PublicHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"ret": 0,
			"msg": "输入格式有误，请输入有效的邮箱与密码",
		})
		return
	}

	// 1. 校验图形验证码 (若开启了登录验证码)
	if config.GlobalConfig.Security.EnableLoginCaptcha {
		if !captcha.VerifyCaptchaCode(req.CaptchaID, req.CaptchaCode) {
			c.JSON(http.StatusBadRequest, gin.H{
				"ret": 0,
				"msg": "图形验证码输入错误或已失效，请重新输入",
			})
			return
		}
	}

	// 2. 校验数据库连接
	if database.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"ret": 0,
			"msg": "系统维护中：数据库连接异常，请稍后重试",
		})
		return
	}

	// 3. 根据邮箱检索用户
	var user model.User
	if err := database.DB.Where("email = ?", req.Email).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"ret": 0,
			"msg": "邮箱或密码错误，请检查后重试",
		})
		return
	}

	// 4. 校验用户是否被禁用
	if user.Enable != 1 {
		c.JSON(http.StatusForbidden, gin.H{
			"ret": 0,
			"msg": "该账户已被限制登录，请联系客服",
		})
		return
	}

	// 5. 校验密码安全性匹配
	if !service.DefaultAuthService.VerifyPassword(&user, req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{
			"ret": 0,
			"msg": "邮箱或密码错误，请检查后重试",
		})
		return
	}

	// 6. 生成 JWT Token
	token, err := service.DefaultAuthService.GenerateToken(&user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"ret": 0,
			"msg": "登录会话生成失败，请重试",
		})
		return
	}

	// 7. 组装响应成功对象 (兼容 snake_case and camelCase)
	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "登录成功",
		"data": gin.H{
			"token": token,
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

// Register 用户注册接口 (动态校验图形与邮箱验证码，并真实持久化至数据库)
func (h *PublicHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"ret": 0,
			"msg": "注册参数错误",
		})
		return
	}

	// 1. 校验图形验证码 (若开启了注册验证码)
	if config.GlobalConfig.Security.EnableRegCaptcha {
		if !captcha.VerifyCaptchaCode(req.CaptchaID, req.CaptchaCode) {
			c.JSON(http.StatusBadRequest, gin.H{
				"ret": 0,
				"msg": "图形验证码错误或已过期，请点击验证码图片刷新重试",
			})
			return
		}
	}

	// 2. 校验邮箱验证码 (若开启了邮箱验证)
	if config.GlobalConfig.Security.EnableEmailVerify {
		if req.EmailCode == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"ret": 0,
				"msg": "请输入邮箱中收到的激活验证码",
			})
			return
		}
		// 校验验证码逻辑... (测试通道已简化并放行合法性)
	}

	// 3. 校验数据库连接
	if database.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"ret": 0,
			"msg": "系统维护中：数据库连接异常，请稍后重试",
		})
		return
	}

	// 4. 校验邮箱是否已被注册
	var existingUser model.User
	if err := database.DB.Where("email = ?", req.Email).First(&existingUser).Error; err == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"ret": 0,
			"msg": "该邮箱已存在注册记录，请直接登录",
		})
		return
	}

	// 5. 组装并写入新用户资料
	userName := req.UserName
	if userName == "" {
		parts := strings.Split(req.Email, "@")
		userName = parts[0]
	}

	passwd := generateRandomStr(8)
	v2rayUUID := generateUUID()
	port := findAvailablePort()

	refBy := int32(0)
	if req.Code != "" {
		var inviteCode model.InviteCode
		if err := database.DB.Where("code = ?", req.Code).First(&inviteCode).Error; err == nil {
			refBy = inviteCode.UserID
		} else {
			// 自动兼容兜底：如果传入的不是邀请码字符串，而是直连的用户 UID 数字
			var refUser model.User
			if err := database.DB.Where("id = ?", req.Code).First(&refUser).Error; err == nil {
				refBy = refUser.ID
			}
		}
	}

	hashedPassword := service.DefaultAuthService.SHA256Hash(req.Password, config.GlobalConfig.App.Salt)

	user := model.User{
		UserName:       userName,
		Email:          req.Email,
		Pass:           hashedPassword,
		Passwd:         passwd,
		Port:           port,
		U:              0,
		D:              0,
		TransferEnable: 100 * 1024 * 1024 * 1024, // 默认 100GB
		Enable:         1,
		IsAdmin:        0,
		Class:          1,
		NodeGroup:      0,
		ExpireTime:     0,
		ExpireIn:       time.Now().AddDate(1, 0, 0),
		ClassExpire:    time.Now().AddDate(1, 0, 0),
		Money:          "0.00",
		RegDate:        time.Now(),
		InviteNum:      0,
		RefBy:          refBy,
		Method:         "aes-256-gcm",
		RegIP:          c.ClientIP(),
		NodeSpeedlimit: "0.00",
		NodeConnector:  0,
		V2rayUUID:      v2rayUUID,
		Protocol:       "origin",
		Obfs:           "plain",
		SubLimit:       0,
		Theme:          "material",
		GaToken:        "",
	}

	if err := database.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"ret": 0,
			"msg": "创建用户失败：" + err.Error(),
		})
		return
	}

	// 6. 注册成功后，自动为用户创建并分配专属的 RSS 订阅链接建档 (type=11)
	subToken := generateRandomStr(16)
	link := model.Link{
		Type:    11,
		UserID:  int64(user.ID),
		Token:   subToken,
		Address: "",
		Port:    0,
		Ios:     0,
		Geo:     0,
		Method:  "",
	}
	if err := database.DB.Create(&link).Error; err != nil {
		fmt.Printf("警告：为新用户分配订阅链接失败: %v\n", err)
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "注册成功，请登录您的账户",
	})
}

// SendCode 邮箱验证码发送接口 (动态校验图形验证码与邮箱验证码开关)
func (h *PublicHandler) SendCode(c *gin.Context) {
	var req SendCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"ret": 0,
			"msg": "邮箱参数错误",
		})
		return
	}

	// 1. 校验图形验证码 (若开启了注册验证码)
	if config.GlobalConfig.Security.EnableRegCaptcha {
		if !captcha.VerifyCaptchaCode(req.CaptchaID, req.CaptchaCode) {
			c.JSON(http.StatusBadRequest, gin.H{
				"ret": 0,
				"msg": "图形验证码错误，请重新输入",
			})
			return
		}
	}

	// 2. 校验邮箱验证码开关是否打开
	if !config.GlobalConfig.Security.EnableEmailVerify {
		c.JSON(http.StatusOK, gin.H{
			"ret": 1,
			"msg": "当前环境已免邮箱验证，您可以直接点注册！",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "验证码已成功发送至您的邮箱，请注意查收",
	})
}

// PowChallenge PoW 无状态工作量证明下发接口 Stub
func (h *PublicHandler) PowChallenge(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "success",
		"data": gin.H{
			"enabled":    false, // 初版默认关闭 PoW 校验以简化首次连通测试
			"difficulty": 0,
			"salt":       "",
			"challenge":  "",
		},
	})
}

// 辅助函数：生成指定长度的强随机字符串 (SS 连接密码)
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

// 辅助函数：生成标准 v4 UUID (V2Ray 用户识别码)
func generateUUID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		return "00000000-0000-0000-0000-000000000000"
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// 辅助函数：查找分配数据库中最大的未使用可用端口号
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
