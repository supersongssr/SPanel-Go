package handler

import (
	"errors"
	"net/http"
	"strings"

	"spanel-go/internal/app/middleware"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/timex"

	"github.com/gin-gonic/gin"
)

// APIv2UserHandler v2 用户查询 API (GET /api/v2/user)
//
// 对齐 PHP app/Controllers/V2/UserController.php:
// 面板运维/外部系统只读查询 —— 按邮箱或用户 ID 查询单个用户信息 (含流量汇总)。
// 信封契约: 成功 {"data":{...}}, 失败 {"error":{"code","message"}} + HTTP 状态码。
type APIv2UserHandler struct{}

var DefaultAPIv2UserHandler = &APIv2UserHandler{}

// v2UserMaxTrafficLogs ?traffic_logs= 上限, 防一次拉全表
const v2UserMaxTrafficLogs = 100

// UserInfo GET /api/v2/user?id={id} 或 ?email={email} [?traffic_logs=N]
func (h *APIv2UserHandler) UserInfo(c *gin.Context) {
	// 鉴权: 仅 Bearer 头 (不接受 query 传 key, 防主密钥落入代理日志)
	if !middleware.CheckMuKey(c, middleware.ModeBearerOnly) {
		errEnvelope(c, http.StatusUnauthorized, "INVALID_TOKEN", "token is invalid")
		return
	}

	// 参数: id / email 二选一
	idRaw := strings.TrimSpace(c.Query("id"))
	emailRaw := strings.TrimSpace(c.Query("email"))
	if idRaw != "" && emailRaw != "" {
		errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "provide only one of id or email")
		return
	}
	if idRaw == "" && emailRaw == "" {
		errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "id or email is required")
		return
	}

	var user model.User
	if idRaw != "" {
		// 规范正整数字符串: 拒绝 1e3/+5/01 等会被隐式换算成其它 ID 的写法
		id, ok := canonicalPositiveInt(idRaw)
		if !ok {
			errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "id must be a positive integer")
			return
		}
		if err := queryUserByID(c, &user, id); err != nil {
			return // 响应已在内部写出
		}
	} else {
		// 与登录/找回密码同口径: 邮箱统一小写后精确匹配
		if len(emailRaw) > 64 || !validEmail(emailRaw) {
			errEnvelope(c, http.StatusBadRequest, "BAD_REQUEST", "email is invalid")
			return
		}
		if err := queryUserByEmail(c, &user, strings.ToLower(emailRaw)); err != nil {
			return
		}
	}

	// ---- 流量汇总 (u/d 为累计值已乘各节点倍率, 单位字节; transfer_enable 为总量配额) ----
	upload := float64(user.U)
	download := float64(user.D)
	used := upload + download
	transferEnable := float64(user.TransferEnable)
	remaining := transferEnable - used
	if remaining < 0 {
		remaining = 0 // 已超量不返回负数, 超量状态看 used_percent >= 100
	}

	// 今日用量 (user_traffic_log 原始字节求和, 未乘倍率; 失败降级 0 但留痕)
	todayUsed := 0.0
	var today struct{ Total float64 }
	if err := database.DB.Table("user_traffic_log").
		Select("COALESCE(SUM(u + d), 0) AS total").
		Where("user_id = ? AND log_time >= ?", user.ID, timex.TodayStart().Unix()).
		Scan(&today).Error; err != nil {
		// 降级为 0, 但 DB 真实故障不应伪装成"今日 0 流量"而无迹可查
		logApiV2("today_used query failed user_id=%d err=%v", user.ID, err)
	} else {
		todayUsed = today.Total
	}

	usedPercent := 0.0
	if transferEnable > 0 {
		usedPercent = roundF(used*100/transferEnable, 2)
	}

	data := gin.H{
		"id":                 user.ID,
		"email":              user.Email,
		"user_name":          user.UserName,
		"remark":             user.Remark,
		"port":               user.Port,
		"method":             user.Method,
		"enable":             user.Enable,
		"is_admin":           user.IsAdmin,
		"class":              user.Class,
		"class_expire":       timeStr(user.ClassExpire),
		"node_group":         user.NodeGroup,
		"node_speedlimit":    user.NodeSpeedlimit,
		"node_connector":     user.NodeConnector,
		"money":              user.Money,
		"ref_by":             user.RefBy,
		"reg_date":           timeStr(user.RegDate),
		"expire_in":          timeStr(user.ExpireIn),
		"last_use_time":      user.T, // 最近一次流量上报时间 (未用过为 0)
		"last_check_in_time": user.LastCheckInTime,
		"traffic": gin.H{
			"upload_bytes":          user.U,
			"download_bytes":        user.D,
			"used_bytes":            int64(used),
			"transfer_enable_bytes": user.TransferEnable,
			"remaining_bytes":       int64(remaining),
			"used_percent":          usedPercent,
			"today_used_bytes":      int64(todayUsed),
		},
	}

	// ---- 可选: 最近 N 条流量明细 ----
	if logCount := parseIntDefault(c.Query("traffic_logs"), 0); logCount > 0 {
		if logCount > v2UserMaxTrafficLogs {
			logCount = v2UserMaxTrafficLogs
		}
		data["traffic_logs"] = loadTrafficLogs(user.ID, logCount)
	}

	dataOK(c, data)
}

// queryUserByID 按 ID 查询用户; DB 离线 → 500 INTERNAL, 无行 → 404 USER_NOT_FOUND。
// 失败时响应已写出, 返回 error 仅用于调用方早退。
func queryUserByID(c *gin.Context, user *model.User, id int64) error {
	if database.DB == nil {
		errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "database is offline")
		return errHandled
	}
	if err := database.DB.Where("id = ?", id).First(user).Error; err != nil {
		errEnvelope(c, http.StatusNotFound, "USER_NOT_FOUND", "user not found")
		return errHandled
	}
	return nil
}

// queryUserByEmail 按邮箱查询用户 (小写精确匹配); 错误响应同 queryUserByID
func queryUserByEmail(c *gin.Context, user *model.User, email string) error {
	if database.DB == nil {
		errEnvelope(c, http.StatusInternalServerError, "INTERNAL", "database is offline")
		return errHandled
	}
	if err := database.DB.Where("email = ?", email).First(user).Error; err != nil {
		errEnvelope(c, http.StatusNotFound, "USER_NOT_FOUND", "user not found")
		return errHandled
	}
	return nil
}

// errHandled 哨兵错误: 响应已写出, 调用方只需早退
var errHandled = errors.New("response already written")

func loadTrafficLogs(userID int32, limit int) []gin.H {
	logs := make([]model.UserTrafficLog, 0, limit)
	if err := database.DB.Where("user_id = ?", userID).
		Order("log_time desc").Limit(limit).Find(&logs).Error; err != nil {
		logApiV2("traffic_logs query failed user_id=%d err=%v", userID, err)
		return []gin.H{}
	}

	// 节点名 map (缺失填 unknown)
	nodeIDs := make([]int32, 0, len(logs))
	seen := map[int32]bool{}
	for _, l := range logs {
		if !seen[l.NodeID] {
			seen[l.NodeID] = true
			nodeIDs = append(nodeIDs, l.NodeID)
		}
	}
	nodeNames := map[int32]string{}
	if len(nodeIDs) > 0 {
		var nodes []model.Node
		if err := database.DB.Select("id, name").Where("id IN ?", nodeIDs).Find(&nodes).Error; err == nil {
			for _, n := range nodes {
				nodeNames[n.ID] = n.Name
			}
		}
	}

	out := make([]gin.H, 0, len(logs))
	for _, l := range logs {
		name := "unknown"
		if n, ok := nodeNames[l.NodeID]; ok {
			name = n
		}
		out = append(out, gin.H{
			"log_time":       l.LogTime,
			"node_id":        l.NodeID,
			"node_name":      name,
			"upload_bytes":   l.U,
			"download_bytes": l.D,
			"rate":           l.Rate,
			"traffic_text":   l.Traffic, // 乘率后的人类可读值 (入库时生成)
		})
	}
	return out
}
