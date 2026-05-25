package handler

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"

	"github.com/gin-gonic/gin"
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
	query := database.DB.Where("type = 1") // 仅拉取可见节点

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

	// 2. 奖励 100MB - 500MB
	randomMB := generateRandomNum(100, 500)
	rewardBytes := int64(randomMB) * 1024 * 1024
	nextTransfer := user.TransferEnable + rewardBytes
	nowUnix := int32(time.Now().Unix())

	// 3. 执行更新
	if err := database.DB.Model(user).Updates(map[string]interface{}{
		"transfer_enable":    nextTransfer,
		"last_check_in_time": nowUnix,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"ret": 0,
			"msg": "签到失败，数据库更新异常",
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
