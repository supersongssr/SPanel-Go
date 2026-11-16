package handler

// 用户端 edit 页 16 个子端点 + 账户操作 (kill/reactive/disable) + 2FA/Telegram + back-to-admin
// 移植基线: PHP UserController update* 族 (config/routes.php /user 组)

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

// ---- 用户端业务开关缺省口径 (PHP config.default.php) ----

func userMinPort() int {
	if v := config.GlobalConfig.User.MinPort; v > 0 {
		return v
	}
	return 11111
}

func userMaxPort() int {
	if v := config.GlobalConfig.User.MaxPort; v > 0 {
		return v
	}
	return 55555
}

func userPortPrice() float64 { return config.GlobalConfig.User.PortPrice }       // <0 = 关闭
func userPortPriceSpecify() float64 { return config.GlobalConfig.User.PortPriceSpecify } // <0 = 关闭

// chargeMoney 原子扣费 (条件更新防并发双花; 返回是否成功)
func chargeMoney(userID int32, amount float64) bool {
	if amount <= 0 {
		return true
	}
	res := database.DB.Model(&model.User{}).
		Where("id = ? AND money >= ?", userID, amount).
		UpdateColumn("money", gorm.Expr("money - ?", amount))
	return res.Error == nil && res.RowsAffected == 1
}

// ---- edit 页: 联络方式 ----

// UpdateWechat 更新联络方式 (绑定了 Telegram 则拒绝, PHP 同口径)
func (h *UserHandler) UpdateWechat(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		ImType  int    `json:"imtype"`
		Wechat  string `json:"wechat"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Wechat) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "请输入有效的联络方式"})
		return
	}

	if user.TelegramID > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "您已绑定 Telegram，请使用 Telegram 与我们沟通"})
		return
	}

	// 联络方式查重
	var dup int64
	database.DB.Model(&model.User{}).Where("im_value = ? AND id <> ?", req.Wechat, user.ID).Count(&dup)
	if dup > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "该联络方式已被其他账户使用"})
		return
	}

	if err := database.DB.Model(user).Updates(map[string]interface{}{
		"im_type":  req.ImType,
		"im_value": strings.TrimSpace(req.Wechat),
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新失败，请稍后重试"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "联络方式更新成功"})
}

// ---- edit 页: 协议/混淆 ----

// UpdateSSR 更新协议与混淆 (白名单校验)
func (h *UserHandler) UpdateSSR(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Protocol  string `json:"protocol"`
		Obfs      string `json:"obfs"`
		ObfsParam string `json:"obfs_param"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	protocols := map[string]bool{"origin": true, "auth_sha1_v4": true, "auth_aes128_md5": true, "auth_aes128_sha1": true, "auth_chain_a": true, "auth_chain_b": true}
	obfss := map[string]bool{"plain": true, "http_simple": true, "http_post": true, "tls1.2_ticket_auth": true, "tls1.2_ticket_fastauth": true}
	if !protocols[req.Protocol] {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "协议无效"})
		return
	}
	if !obfss[req.Obfs] {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "混淆方式无效"})
		return
	}

	if err := database.DB.Model(user).Updates(map[string]interface{}{
		"protocol":   req.Protocol,
		"obfs":       req.Obfs,
		"obfs_param": req.ObfsParam,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "协议与混淆更新成功"})
}

// ---- edit 页: 主题 / 分组 / 日报 / 订阅上限 / 匿名 ----

// UpdateTheme 更换面板主题 (白名单)
func (h *UserHandler) UpdateTheme(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Theme string `json:"theme" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}

	themes := map[string]bool{"material": true, "light": true, "dark": true}
	if !themes[req.Theme] {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "不支持的主题"})
		return
	}

	if err := database.DB.Model(user).Update("theme", req.Theme).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "主题已切换"})
}

// UpdateGroup 自助更换节点分组 (开关 + 1~4)
func (h *UserHandler) UpdateGroup(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	if !config.GlobalConfig.User.EnableUserCustomGroup {
		c.JSON(http.StatusForbidden, gin.H{"ret": 0, "msg": "本站未开放自助更换分组，如有需要请联系客服"})
		return
	}

	var req struct {
		Group int `json:"group" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Group < 1 || req.Group > 4 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "分组无效 (1-4)"})
		return
	}

	if err := database.DB.Model(user).Updates(map[string]interface{}{
		"node_group":        req.Group,
		"status_changed_at": time.Now().Unix(),
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "分组已切换，节点列表将随后刷新"})
}

// UpdateMail 每日报告邮件开关
func (h *UserHandler) UpdateMail(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Mail int `json:"mail"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Mail != 0 && req.Mail != 1) {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误 (0/1)"})
		return
	}

	if err := database.DB.Model(user).Update("sendDailyMail", req.Mail).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新失败"})
		return
	}
	if req.Mail == 1 {
		c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "每日报告已订阅"})
	} else {
		c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "每日报告已关闭"})
	}
}

// UpdateSubLimit 自设订阅节点数上限
func (h *UserHandler) UpdateSubLimit(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		SubLimit int `json:"sub_limit" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.SubLimit < 0 || req.SubLimit > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "订阅上限无效 (0-200, 0 为不限)"})
		return
	}

	if err := database.DB.Model(user).Update("sub_limit", req.SubLimit).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "订阅节点上限已更新"})
}

// UpdateHide 捐赠公示匿名开关
func (h *UserHandler) UpdateHide(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Hide int `json:"hide"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Hide != 0 && req.Hide != 1) {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误 (0/1)"})
		return
	}

	if err := database.DB.Model(user).Update("is_hide", req.Hide).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新失败"})
		return
	}
	if req.Hide == 1 {
		c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "已开启匿名模式，捐赠榜将隐藏您的昵称"})
	} else {
		c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "已关闭匿名模式"})
	}
}

// ---- edit 页: 中转线路 / 优选 IP ----

// UpdateCncdn 更换中转加速线路 (计数清零)
func (h *UserHandler) UpdateCncdn(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Cncdn string `json:"cncdn" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "请选择线路"})
		return
	}

	// 校验线路存在且启用
	var cnt int64
	database.DB.Model(&model.Cncdn{}).Where("areaid = ? AND status = 1", req.Cncdn).Count(&cnt)
	if cnt == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "线路无效或未启用"})
		return
	}

	if err := database.DB.Model(user).Updates(map[string]interface{}{
		"cncdn":       req.Cncdn,
		"cncdn_count": 0,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "中转线路已切换"})
}

// UpdateCfcdn 更新优选 IP (合法 IP 校验 + 计数清零)
func (h *UserHandler) UpdateCfcdn(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Cfcdn string `json:"cfcdn" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "请输入优选 IP"})
		return
	}

	ip := strings.TrimSpace(req.Cfcdn)
	if net.ParseIP(ip) == nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "请输入合法的 IP 地址"})
		return
	}

	if err := database.DB.Model(user).Updates(map[string]interface{}{
		"cfcdn":       ip,
		"cfcdn_count": 0,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "优选 IP 已更新"})
}

// ---- edit 页: 端口 ----

// SpecifyPort 钦定端口 (范围/占用校验 + 原子扣费 port_price_specify)
func (h *UserHandler) SpecifyPort(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Port int `json:"port" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "请输入端口号"})
		return
	}

	price := userPortPriceSpecify()
	if price < 0 {
		c.JSON(http.StatusForbidden, gin.H{"ret": 0, "msg": "钦定端口功能未开放"})
		return
	}
	if req.Port < userMinPort() || req.Port > userMaxPort() {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "端口范围无效"})
		return
	}

	// 占用检查
	var occupied int64
	database.DB.Model(&model.User{}).Where("port = ? AND id <> ?", req.Port, user.ID).Count(&occupied)
	if occupied > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "该端口已被占用，请换一个"})
		return
	}

	// 原子扣费 → 更新 (扣费失败 = 余额不足)
	if !chargeMoney(user.ID, price) {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "余额不足，请先充值"})
		return
	}

	if err := database.DB.Model(user).Update("port", req.Port).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "端口更新失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "端口钦定成功", "data": gin.H{"charged": price}})
}

// ---- edit 页: PAC / 解封 ----

// PacSet 自定义 PAC 文本
func (h *UserHandler) PacSet(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Pac string `json:"pac"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "参数有误"})
		return
	}
	if strings.TrimSpace(req.Pac) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "PAC 内容不能为空"})
		return
	}

	if err := database.DB.Model(user).Update("pac", req.Pac).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "PAC 已保存"})
}

// Unblock 解封当前 IP (删 BlockIp + 写 UnblockIp 命令记录)
func (h *UserHandler) Unblock(c *gin.Context) {
	user := c.MustGet("user").(*model.User)
	ip := c.ClientIP()

	res := database.DB.Where("ip = ?", ip).Delete(&model.BlockIp{})
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "当前 IP 不在封禁列表中"})
		return
	}

	database.DB.Create(&model.UnblockIp{IP: ip, DateTime: time.Now().Unix(), UserID: int64(user.ID)})
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "解封指令已下发，请在 1-2 分钟后重试连接"})
}

// ---- 2FA ----

// GaCheck 测试当前 ga_token 的 TOTP 是否正确
func (h *UserHandler) GaCheck(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "请输入 6 位验证码"})
		return
	}

	if !verifyTOTP(user.GaToken, req.Code) {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "验证失败，请确认时间同步后重试"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "验证成功"})
}

// GaEnable 开启 2FA (验码后落库 ga_enable=1)
func (h *UserHandler) GaEnable(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Secret string `json:"secret" binding:"required"`
		Code   string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "两步验证参数有误"})
		return
	}

	if !verifyTOTP(req.Secret, req.Code) {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "验证失败！6 位动态验证码错误，请确保时间已同步"})
		return
	}

	if err := database.DB.Model(user).Updates(map[string]interface{}{
		"ga_token":  req.Secret,
		"ga_enable": 1,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "保存 2FA 配置失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "双重验证保护绑定成功！下一次登录将被激活安全屏障"})
}

// GaDisable 关闭 2FA (安全增强: 需密码或 TOTP 二次校验)
func (h *UserHandler) GaDisable(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	_ = c.ShouldBindJSON(&req)

	if req.Password == "" && req.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "安全校验失败：请输入登录密码或动态验证码以确认解绑"})
		return
	}
	ok := false
	if req.Password != "" && service.DefaultAuthService.VerifyPassword(user, req.Password) {
		ok = true
	}
	if !ok && req.Code != "" && verifyTOTP(user.GaToken, req.Code) {
		ok = true
	}
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "校验失败，密码或动态验证码错误"})
		return
	}

	if err := database.DB.Model(user).Updates(map[string]interface{}{
		"ga_enable": 0,
		"ga_token":  "",
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "解绑 2FA 失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "两步验证已解除，请注意防范登录爆破风险"})
}

// GaReset 重生成 2FA 密钥 (返回新 Base32 secret, 前端展示新二维码)
func (h *UserHandler) GaReset(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	newSecret := generateBase32Secret(16)
	if err := database.DB.Model(user).Updates(map[string]interface{}{
		"ga_token":  newSecret,
		"ga_enable": 0,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "重置失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "2FA 密钥已重置，请重新扫码绑定",
		"data": gin.H{
			"ga_token":  newSecret,
			"ga_enable": false,
		},
	})
}

// TelegramReset 解绑 Telegram
func (h *UserHandler) TelegramReset(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	if err := database.DB.Model(user).Update("telegram_id", 0).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "解绑失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "Telegram 已解绑"})
}

// ---- 账户: 注销 / 自助解封 / 禁用信息 ----

// DisableInfo 被禁引导页数据 (warming 文案 + 基础信息)
func (h *UserHandler) DisableInfo(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "获取成功",
		"data": gin.H{
			"email":     user.Email,
			"user_name": user.UserName,
			"ban_times": user.BanTimes,
			"warming":   user.Warming,
		},
	})
}

// Reactive 自助解封 (enable=1 + ban_times=0 + 清 warming)
func (h *UserHandler) Reactive(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	if err := database.DB.Model(user).Updates(map[string]interface{}{
		"enable":     1,
		"ban_times":  0,
		"warming":    "",
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "解封失败，请稍后重试"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "账户已解封，祝您使用愉快"})
}

// Kill 注销账户 (密码确认 + W9 守卫: 在服用户不可注销 + enable_kill 开关)
func (h *UserHandler) Kill(c *gin.Context) {
	user := c.MustGet("user").(*model.User)

	var req struct {
		Passwd string `json:"passwd" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "请输入登录密码确认注销"})
		return
	}

	if !config.GlobalConfig.User.EnableKill {
		c.JSON(http.StatusForbidden, gin.H{"ret": 0, "msg": "本站未开放自助注销，如有需要请联系客服"})
		return
	}

	if !service.DefaultAuthService.VerifyPassword(user, req.Passwd) {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "密码错误"})
		return
	}

	// W9 守卫: 未到期且流量有余 (在服用户) 不可注销
	inService := user.ExpireTime == 0 || time.Now().Unix() < int64(user.ExpireTime)
	trafficLeft := user.TransferEnable - (user.U + user.D)
	if inService && trafficLeft > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "您的账户仍在服务期内且有剩余流量，暂不可注销；请联系客服处理"})
		return
	}

	if err := database.DB.Model(user).Updates(map[string]interface{}{
		"enable":         0,
		"ban_times":      user.BanTimes + 1,
		"remark":         "用户自助注销",
		"token_version":  user.TokenVersion + 1, // 全端下线
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "注销失败，请稍后重试"})
		return
	}

	service.DefaultAuthService.ClearAuthCookie(c)
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "账户已注销，期待您再次回来"})
}

// ---- backtoadmin / 用户侧登出 ----

// BackToAdmin 管理员切换身份还原 (§4.0-7: masquerade JWT 携带 act_as_admin claim,
// 校验后重签管理员 cookie, 无服务端状态)
func (h *UserHandler) BackToAdmin(c *gin.Context) {
	claimsVal, _ := c.Get("claims")
	claims, ok := claimsVal.(jwt.MapClaims)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"ret": 0, "msg": "当前会话无管理员身份可还原"})
		return
	}

	adminIDf, ok := claims["act_as_admin"].(float64)
	if !ok || adminIDf <= 0 {
		c.JSON(http.StatusForbidden, gin.H{"ret": 0, "msg": "当前会话无管理员身份可还原"})
		return
	}

	var admin model.User
	if err := database.DB.First(&admin, int32(adminIDf)).Error; err != nil || admin.IsAdmin != 1 || admin.Enable != 1 {
		c.JSON(http.StatusForbidden, gin.H{"ret": 0, "msg": "原管理员账户已不可用"})
		return
	}

	token, err := service.DefaultAuthService.GenerateTokenEx(&admin, int64(24*3600), 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "还原失败"})
		return
	}
	service.DefaultAuthService.SetAuthCookie(c, token, 24*3600)

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "已还原管理员身份",
		"data": gin.H{
			"user": gin.H{
				"id":        admin.ID,
				"user_name": admin.UserName,
				"email":     admin.Email,
				"is_admin":  admin.IsAdmin,
				"isAdmin":   admin.IsAdmin == 1,
			},
		},
	})
}

// Logout 用户侧登出 (与 /api/v1/auth/logout 同实现, 路由别名)
func (h *UserHandler) Logout(c *gin.Context) {
	service.DefaultAuthService.ClearAuthCookie(c)
	c.JSON(http.StatusOK, gin.H{"ret": 1, "msg": "已安全退出"})
}

// ---- Redis 工单限频 (PHP 同口径: 建单 3 次/h, 回复 10 次/h) ----

func ticketRateAllow(ctx context.Context, key string, limit int64) bool {
	if database.Redis == nil {
		return true
	}
	n, _ := database.Redis.Incr(ctx, key).Result()
	if n == 1 {
		database.Redis.Expire(ctx, key, time.Hour)
	}
	return n <= limit
}

// parsePage 统一分页参数 (默认 15/页, 上限 100)
func parsePage(c *gin.Context) (page, size int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ = strconv.Atoi(c.DefaultQuery("page_size", "15"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 15
	}
	return
}
