package handler

import (
	"net/http"

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
	Code        string `json:"code"` // 邮箱验证码
	CaptchaID   string `json:"captchaId"`
	CaptchaCode string `json:"captchaCode"`
}

type SendCodeRequest struct {
	Email       string `json:"email" binding:"required,email"`
	CaptchaID   string `json:"captchaId"`
	CaptchaCode string `json:"captchaCode"`
}

// Captcha 获取图形验证码 SVG
func (h *PublicHandler) Captcha(c *gin.Context) {
	captchaId := captcha.GenerateRandomUUID()
	code := captcha.GenerateRandomCode()
	svg := captcha.GenerateCaptchaSVG(code)

	captcha.StoreCaptchaCode(captchaId, code)

	c.JSON(http.StatusOK, gin.H{
		"ret":              1,
		"msg":              "获取成功",
		"captcha_provider": "graphic",
		"captchaId":        captchaId,
		"svg":              svg,
	})
}

// Login 用户登录接口 (校验密码哈希、图形验证码并颁发 JWT)
func (h *PublicHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"ret": 0,
			"msg": "输入格式有误，请输入有效的邮箱与密码",
		})
		return
	}

	// 1. 校验图形验证码
	if req.CaptchaID != "" || req.CaptchaCode != "" {
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

	// 7. 组装响应成功对象
	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "登录成功",
		"data": gin.H{
			"token": token,
			"user": gin.H{
				"id":        user.ID,
				"user_name": user.UserName,
				"email":     user.Email,
				"is_admin":  user.IsAdmin,
				"class":     user.Class,
			},
		},
	})
}

// Register 用户注册接口 Stub
func (h *PublicHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"ret": 0,
			"msg": "注册参数错误",
		})
		return
	}

	// 1. 校验图形验证码
	if !captcha.VerifyCaptchaCode(req.CaptchaID, req.CaptchaCode) {
		c.JSON(http.StatusBadRequest, gin.H{
			"ret": 0,
			"msg": "图形验证码错误或已过期，请点击验证码图片刷新重试",
		})
		return
	}

	// 2. 模拟注册成功
	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "注册成功，请登录您的账户 (已对接测试通道)",
	})
}

// SendCode 邮箱验证码发送接口 Stub
func (h *PublicHandler) SendCode(c *gin.Context) {
	var req SendCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"ret": 0,
			"msg": "邮箱参数错误",
		})
		return
	}

	// 1. 校验图形验证码
	if !captcha.VerifyCaptchaCode(req.CaptchaID, req.CaptchaCode) {
		c.JSON(http.StatusBadRequest, gin.H{
			"ret": 0,
			"msg": "图形验证码错误，请重新输入",
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
