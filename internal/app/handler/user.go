package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type UserHandler struct{}

var DefaultUserHandler = &UserHandler{}

// Announcement 内部定义公告实体 (对应数据库 announcement 表)
type Announcement struct {
	ID       int32     `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Date     time.Time `gorm:"column:date;type:datetime" json:"date"`
	Content  string    `gorm:"column:content" json:"content"`
	Markdown string    `gorm:"column:markdown" json:"markdown"`
}

// TableName 映射公告表名
func (Announcement) TableName() string {
	return "announcement"
}

// Profile 获取当前用户的详细资料与状态数据 (包含专属订阅 Token 并兼顾大小写命名适配)
func (h *UserHandler) Profile(c *gin.Context) {
	userVal, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"ret": 0,
			"msg": "未授权的访问",
		})
		return
	}

	user := userVal.(*model.User)

	// 查找或分配专属的 RSS 订阅 Token (type=11)
	var link model.Link
	if err := database.DB.Where("userid = ? AND type = 11", user.ID).First(&link).Error; err != nil {
		// 若因旧版本测试漏配，现场高强度冷补档一个以保证 100% 连通容错
		subToken := generateLocalRandomStr(16)
		link = model.Link{
			Type:    11,
			UserID:  int64(user.ID),
			Token:   subToken,
			Address: "",
			Port:    0,
			Ios:     0,
			Geo:     0,
			Method:  "",
		}
		database.DB.Create(&link)
	}

	// 1. 查询最近 5 分钟内活跃 IP 连接足迹 (300秒内)
	var aliveIPList []model.AliveIP
	fiveMinutesAgo := time.Now().Unix() - 300
	database.DB.Where("userid = ? AND datetime > ?", user.ID, fiveMinutesAgo).Order("datetime DESC").Find(&aliveIPList)

	type formattedAliveIP struct {
		IP       string `json:"ip"`
		DateTime string `json:"datetime"`
	}
	formattedAlive := make([]formattedAliveIP, 0)
	seenAliveIPs := make(map[string]bool)
	for _, ipLog := range aliveIPList {
		if !seenAliveIPs[ipLog.IP] {
			seenAliveIPs[ipLog.IP] = true
			formattedAlive = append(formattedAlive, formattedAliveIP{
				IP:       ipLog.IP,
				DateTime: time.Unix(ipLog.DateTime, 0).Format("2006-01-02 15:04:05"),
			})
		}
	}

	// 2. 查询最近 10 次网页端登录历史
	var loginIPList []model.LoginIP
	database.DB.Where("userid = ?", user.ID).Order("datetime DESC").Limit(10).Find(&loginIPList)

	type formattedLoginIP struct {
		IP       string `json:"ip"`
		DateTime string `json:"datetime"`
	}
	formattedLogin := make([]formattedLoginIP, 0)
	for _, log := range loginIPList {
		formattedLogin = append(formattedLogin, formattedLoginIP{
			IP:       log.IP,
			DateTime: time.Unix(log.DateTime, 0).Format("2006-01-02 15:04:05"),
		})
	}

	// 3. 2FA 状况 (ga_enable 真列; 未绑定时 ga_token 为空, 由前端走绑定流程取新密钥)
	gaEnable := user.GaEnable == 1
	gaToken := user.GaToken

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "获取成功",
		"data": gin.H{
			"id":              user.ID,
			"user_name":       user.UserName,
			"userName":        user.UserName,
			"email":           user.Email,
			"port":            user.Port,
			"money":           user.Money,
			"class":           user.Class,
			"class_expire":    user.ClassExpire.Format("2006-01-02 15:04:05"),
			"expire_in":       user.ExpireIn.Format("2006-01-02 15:04:05"),
			"node_group":      user.NodeGroup,
			"method":          user.Method,
			"passwd":          user.Passwd, // 真实 SS 连接密码
			"u":               user.U,
			"d":               user.D,
			"transfer_enable": user.TransferEnable,
			"unused_traffic":  user.TransferEnable - (user.U + user.D),
			"is_admin":        user.IsAdmin,
			"isAdmin":         user.IsAdmin == 1,
			"sub_token":       link.Token, // 真正合法的 16 位订阅 Token
			"alive_ips":       formattedAlive,
			"login_ips":       formattedLogin,
			"ga_enable":       gaEnable,
			"ga_token":        gaToken,
		},
	})
}

// Nodes 获取当前用户已授权的所有可用代理节点列表
func (h *UserHandler) Nodes(c *gin.Context) {
	userVal, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"ret": 0,
			"msg": "未授权的访问",
		})
		return
	}

	user := userVal.(*model.User)

	if database.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"ret": 0,
			"msg": "系统维护中：数据库连接异常，请稍后重试",
		})
		return
	}

	var nodes []model.Node
	// PHP /user/node 同口径: type=1 可见 + id>9 + node_class<=10 + 分组匹配 (管理员免分组)
	query := database.DB.Where("type = 1 AND id > 9 AND node_class <= 10")

	if user.IsAdmin != 1 {
		query = query.Where("(node_group = ? OR node_group = 0)", user.NodeGroup)
	}

	if err := query.Order("sort ASC").Find(&nodes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"ret": 0,
			"msg": "获取节点列表失败，请稍后重试",
		})
		return
	}

	formattedNodes := make([]gin.H, 0, len(nodes))
	for _, n := range nodes {
		formattedNodes = append(formattedNodes, gin.H{
			"id":             n.ID,
			"name":           n.Name,
			"server":         n.Server,
			"method":         n.Method,
			"info":           n.Info,
			"status":         n.Status,
			"traffic_rate":   n.TrafficRate,
			"node_bandwidth": n.NodeBandwidth,
			"node_group":     n.NodeGroup,
			"node_heartbeat": n.NodeHeartbeat,
			"country_code":   n.CountryCode,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"ret":  1,
		"msg":  "获取成功",
		"data": formattedNodes,
	})
}

// Dashboard 用户仪表盘数据看板 (完全对齐前端 expected JSON schema，打通扁平化数据)
func (h *UserHandler) Dashboard(c *gin.Context) {
	userVal, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"ret": 0,
			"msg": "未授权的访问",
		})
		return
	}

	user := userVal.(*model.User)

	// 1. 系统公告拉取 (取最新 5 条)
	var announcements []Announcement
	_ = database.DB.Order("id DESC").Limit(5).Find(&announcements)

	// 格式化公告日期
	type formattedAnn struct {
		ID       int32  `json:"id"`
		Date     string `json:"date"`
		Content  string `json:"content"`
		Markdown string `json:"markdown"`
	}
	formattedList := make([]formattedAnn, 0, len(announcements))
	for _, a := range announcements {
		formattedList = append(formattedList, formattedAnn{
			ID:       a.ID,
			Date:     a.Date.Format("2006-01-02 15:04:05"),
			Content:  a.Content,
			Markdown: a.Markdown,
		})
	}

	// 2. 签到审计 (判定今日是否签到)
	lastCheckinTime := user.LastCheckInTime
	canCheckin := true
	lastCheckinStr := "从未签到"

	if lastCheckinTime > 0 {
		lastDate := time.Unix(int64(lastCheckinTime), 0)
		todayStr := time.Now().Format("2006-01-02")
		lastCheckinDayStr := lastDate.Format("2006-01-02")
		if lastCheckinDayStr == todayStr {
			canCheckin = false
		}
		lastCheckinStr = lastDate.Format("2006-01-02 15:04:05")
	}

	// 3. 查找专属 RSS 订阅 Token (type=11)
	var link model.Link
	if err := database.DB.Where("userid = ? AND type = 11", user.ID).First(&link).Error; err != nil {
		subToken := generateLocalRandomStr(16)
		link = model.Link{
			Type:    11,
			UserID:  int64(user.ID),
			Token:   subToken,
			Address: "",
			Port:    0,
			Ios:     0,
			Geo:     0,
			Method:  "",
		}
		database.DB.Create(&link)
	}

	// 扁平结构输出，防止 BigInt(undefined) 在前端引起运行时报错
	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "获取成功",
		"data": gin.H{
			"money":           user.Money,
			"u":               fmt.Sprintf("%d", user.U),
			"d":               fmt.Sprintf("%d", user.D),
			"transfer_enable": fmt.Sprintf("%d", user.TransferEnable),
			"class":           user.Class,
			"class_expire":    user.ClassExpire.Format("2006-01-02 15:04:05"),
			"checked_in":      !canCheckin,
			"checkin_status": gin.H{
				"can_checkin":       canCheckin,
				"last_checkin_time": lastCheckinStr,
			},
			"announcements": formattedList,
			"sub_token":     link.Token, // 专有订阅 token
		},
	})
}

// Checkin 用户每日签到接口 (真正写入数据库以分配随机流量)
func (h *UserHandler) Checkin(c *gin.Context) {
	userVal, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"ret": 0,
			"msg": "未授权的访问",
		})
		return
	}

	user := userVal.(*model.User)

	// 1. 检查今日是否签到过
	lastCheckinTime := user.LastCheckInTime
	if lastCheckinTime > 0 {
		lastDate := time.Unix(int64(lastCheckinTime), 0)
		todayStr := time.Now().Format("2006-01-02")
		lastCheckinDayStr := lastDate.Format("2006-01-02")
		if lastCheckinDayStr == todayStr {
			c.JSON(http.StatusBadRequest, gin.H{
				"ret": 0,
				"msg": "您今天已经签到过了，请明天再来。",
			})
			return
		}
	}

	// 2. 奖励 100MB - 500MB + 原子条件更新 (并发双签防护: 今日零点之前才允许覆盖)
	randomMB := generateRandomNum(100, 500)
	rewardBytes := int64(randomMB) * 1024 * 1024
	nowUnix := int32(time.Now().Unix())
	todayStart := int32(time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.Local).Unix())

	res := database.DB.Model(&model.User{}).
		Where("id = ? AND (last_check_in_time IS NULL OR last_check_in_time < ?)", user.ID, todayStart).
		Updates(map[string]interface{}{
			"transfer_enable":    gorm.Expr("transfer_enable + ?", rewardBytes),
			"last_check_in_time": nowUnix,
		})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"ret": 0,
			"msg": "签到失败，数据库更新异常",
		})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"ret": 0,
			"msg": "您今天已经签到过了，请明天再来。",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "签到成功",
		"data": gin.H{
			"traffic":    fmt.Sprintf("%d", rewardBytes),
			"checked_in": true,
		},
	})
}

// 本地辅助函数：生成强随机字符串
func generateLocalRandomStr(length int) string {
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

// 本地辅助函数：生成区间随机数
func generateRandomNum(min, max int) int {
	diff := max - min + 1
	num, err := rand.Int(rand.Reader, big.NewInt(int64(diff)))
	if err != nil {
		return min
	}
	return min + int(num.Int64())
}

// ResetPort 随机重设 Shadowsocks 监听端口 (10000-60000 区间且唯一)
func (h *UserHandler) ResetPort(c *gin.Context) {
	userVal, _ := c.Get("user")
	user := userVal.(*model.User)

	// 获取全站已分配端口，去重闲置端口计算
	var usedPorts []int32
	database.DB.Model(&model.User{}).Pluck("port", &usedPorts)
	usedPortMap := make(map[int32]bool)
	for _, p := range usedPorts {
		usedPortMap[p] = true
	}

	var newPort int32
	for {
		newPort = int32(generateRandomNum(10000, 60000))
		if !usedPortMap[newPort] {
			break
		}
	}

	if err := database.DB.Model(user).Update("port", newPort).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "重置端口失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": fmt.Sprintf("端口重置成功，新分配端口为: %d", newPort),
	})
}

// UpdateSSPassword 修改 Shadowsocks 节点连接密码 (passwd)
func (h *UserHandler) UpdateSSPassword(c *gin.Context) {
	userVal, _ := c.Get("user")
	user := userVal.(*model.User)

	var req struct {
		Passwd string `json:"passwd" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "密码无效"})
		return
	}

	if len(req.Passwd) < 6 || len(req.Passwd) > 16 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "密码长度必须在 6-16 位之间"})
		return
	}

	if err := database.DB.Model(user).Update("passwd", req.Passwd).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "更新密码失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "连接密码更新成功！",
	})
}

// UpdateSSMethod 修改 Shadowsocks 节点加密方法
func (h *UserHandler) UpdateSSMethod(c *gin.Context) {
	userVal, _ := c.Get("user")
	user := userVal.(*model.User)

	var req struct {
		Method string `json:"method" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "加密方法参数有误"})
		return
	}

	// 简单过滤防 SQL 注入或非法加密算法
	allowed := map[string]bool{
		"chacha20-ietf-poly1305": true,
		"aes-256-gcm":            true,
		"aes-128-gcm":            true,
		"aes-256-cfb":            true,
		"aes-128-cfb":            true,
		"rc4-md5":                true,
	}
	if !allowed[req.Method] {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "不支持的加密算法"})
		return
	}

	if err := database.DB.Model(user).Update("method", req.Method).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "应用加密方式失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "加密算法修改应用成功！",
	})
}

// UpdateWebPassword 修改网页 Web 控制台登录密码
func (h *UserHandler) UpdateWebPassword(c *gin.Context) {
	userVal, _ := c.Get("user")
	user := userVal.(*model.User)

	var req struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "输入表单有误"})
		return
	}

	if len(req.NewPassword) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "新密码长度必须大于等于 8 位"})
		return
	}

	// 校验旧登录密码 (使用 AuthService)
	if !service.DefaultAuthService.VerifyPassword(user, req.OldPassword) {
		c.JSON(http.StatusBadRequest, gin.H{"ret": 0, "msg": "旧密码验证失败，请重新确认"})
		return
	}

	// 更新新密码 (SHA256)
	salt := config.GlobalConfig.App.Salt
	newHash := service.DefaultAuthService.SHA256Hash(req.NewPassword, salt)

	// §4.0: 改密即 tv+1, 全端旧凭证 (cookie/Bearer) 即时失效
	if err := database.DB.Model(user).Updates(map[string]interface{}{
		"pass":          newHash,
		"token_version": user.TokenVersion + 1,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ret": 0, "msg": "密码修改失败，请重试"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "网页登录密码修改成功，请使用新密码重新登录！",
	})
}

// verifyTOTP 纯 Go 实现 RFC 6238 基于时间的动态令牌密码 TOTP 验证器 ( clock drift +/- 30s)
func verifyTOTP(secret string, code string) bool {
	secret = strings.ToUpper(strings.ReplaceAll(secret, " ", ""))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		key, err = base32.StdEncoding.DecodeString(secret)
		if err != nil {
			return false
		}
	}

	currentTime := time.Now().Unix()
	steps := currentTime / 30

	for i := -1; i <= 1; i++ {
		stepBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(stepBytes, uint64(steps+int64(i)))

		hmacHasher := hmac.New(sha1.New, key)
		hmacHasher.Write(stepBytes)
		hash := hmacHasher.Sum(nil)

		offset := hash[len(hash)-1] & 0x0F
		truncatedHash := binary.BigEndian.Uint32(hash[offset : offset+4])
		truncatedHash &= 0x7FFFFFFF
		otp := truncatedHash % 1000000

		if fmt.Sprintf("%06d", otp) == code {
			return true
		}
	}
	return false
}

// GetInvite 获取当前用户的推广返佣详情 (对接前端 user/invite.html)
func (h *UserHandler) GetInvite(c *gin.Context) {
	userVal, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"ret": 0,
			"msg": "未授权的访问",
		})
		return
	}

	user := userVal.(*model.User)

	// 1. 累计已赚取佣金：对 payback 表 ref_get 字段求和
	var totalEarned float64
	database.DB.Model(&model.Payback{}).Where("ref_by = ?", user.ID).Select("COALESCE(SUM(ref_get), 0)").Scan(&totalEarned)

	// 2. 成功邀请下线好友：在 user 表中检索 ref_by = 当前用户 ID
	var downlines []model.User
	database.DB.Where("ref_by = ?", user.ID).Order("id DESC").Find(&downlines)

	type formattedFriend struct {
		ID      int32  `json:"id"`
		Email   string `json:"email"`
		RegDate string `json:"reg_date"`
	}
	friendsList := make([]formattedFriend, 0, len(downlines))
	for _, f := range downlines {
		parts := strings.Split(f.Email, "@")
		obfuscated := f.Email
		if len(parts) == 2 {
			name := parts[0]
			domain := parts[1]
			if len(name) > 3 {
				obfuscated = name[:3] + "***@" + domain
			} else {
				obfuscated = name + "***@" + domain
			}
		}
		friendsList = append(friendsList, formattedFriend{
			ID:      f.ID,
			Email:   obfuscated,
			RegDate: f.RegDate.Format("2006-01-02 15:04:05"),
		})
	}

	// 3. 返利提成流水账单：在 payback 表中查询 ref_by = 当前用户 ID
	var paybacks []model.Payback
	database.DB.Where("ref_by = ?", user.ID).Order("id DESC").Find(&paybacks)

	consumerIDs := make([]int64, 0, len(paybacks))
	for _, p := range paybacks {
		consumerIDs = append(consumerIDs, p.UserID)
	}

	consumerMap := make(map[int64]string)
	if len(consumerIDs) > 0 {
		var consumers []model.User
		database.DB.Where("id IN ?", consumerIDs).Find(&consumers)
		for _, c := range consumers {
			parts := strings.Split(c.Email, "@")
			obfuscated := c.Email
			if len(parts) == 2 {
				name := parts[0]
				domain := parts[1]
				if len(name) > 3 {
					obfuscated = name[:3] + "***@" + domain
				} else {
					obfuscated = name + "***@" + domain
				}
			}
			consumerMap[int64(c.ID)] = obfuscated
		}
	}

	type formattedConsumer struct {
		Email string `json:"email"`
	}
	type formattedRebate struct {
		ID       int64             `json:"id"`
		Total    string            `json:"total"`
		RefGet   string            `json:"ref_get"`
		Datetime string            `json:"datetime"`
		Consumer formattedConsumer `json:"consumer"`
	}

	rebateList := make([]formattedRebate, 0, len(paybacks))
	for _, p := range paybacks {
		cEmail := consumerMap[p.UserID]
		if cEmail == "" {
			cEmail = "已注销账户"
		}
		rebateList = append(rebateList, formattedRebate{
			ID:       p.ID,
			Total:    p.Total,
			RefGet:   p.RefGet,
			Datetime: time.Unix(p.DateTime, 0).Format("2006-01-02 15:04:05"),
			Consumer: formattedConsumer{Email: cEmail},
		})
	}

	// 4. 专属推广链接与邀请码获取 (优先从 ss_invite_code 载入)
	var inviteCode model.InviteCode
	if err := database.DB.Where("user_id = ?", user.ID).First(&inviteCode).Error; err != nil {
		// 若因首次上架漏配置，则现场高强度生成一个 10 位长度邀请码建档
		codeStr := generateLocalRandomStr(10)
		inviteCode = model.InviteCode{
			Code:      codeStr,
			UserID:    user.ID,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		database.DB.Create(&inviteCode)
	}

	inviteURL := fmt.Sprintf("/auth/register.html?ref=%s", inviteCode.Code)

	// 5. 邀请码列表 (多码并存) + 待达标口径 (v5 ref_rebate_status=1)
	var codes []model.InviteCode
	database.DB.Where("user_id = ?", user.ID).Order("id ASC").Find(&codes)
	codeItems := make([]gin.H, 0, len(codes))
	for _, ic := range codes {
		codeItems = append(codeItems, gin.H{"id": ic.ID, "code": ic.Code})
	}

	var pending int64
	database.DB.Model(&model.User{}).Where("ref_by = ? AND ref_rebate_status = 1", user.ID).Count(&pending)

	// 注册返利口径: 邀请注册人数 (v5: 待达标)
	c.JSON(http.StatusOK, gin.H{
		"ret": 1,
		"msg": "获取成功",
		"data": gin.H{
			"invite_num":   user.InviteNum,
			"total_earned": fmt.Sprintf("%.2f", totalEarned),
			"invite_url":   inviteURL,
			"downlines":    friendsList,
			"rebates":      rebateList,
			"codes":        codeItems,
			"pending":      pending,
		},
	})
}
