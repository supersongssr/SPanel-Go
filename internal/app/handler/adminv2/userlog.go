package adminv2

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"

	"github.com/gin-gonic/gin"
)

/* 对齐 PHP app/Modules/Log/UserActionLog/UserActionLogger.php —
 * admin v2 侧的最小实现 (写入/字段 diff); 容错: 任何失败不影响业务。 */

const (
	uaMaxMsg    = 255
	uaMaxDetail = 16384
	uaMaxValue  = 256
	uaMaxUA     = 255
)

// 凭据类字段: 只记是否变化, 不记值
var uaCredentialFields = map[string]string{
	"pass": "登录密码", "passwd": "连接密码", "ga_token": "GA密钥", "v2ray_uuid": "UUID", "upswd": "二级密码",
}

func uaTruncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// uaLog 写一条操作记录 (失败静默, 审计不阻断业务)
func uaLog(c *gin.Context, userID int64, level, category, action, message string, detail map[string]interface{}) {
	if userID <= 0 {
		userID = 0
	}
	operatorID := int32(0)
	operatorType := int8(0) // 0=系统
	if c != nil {
		if u, ok := c.MustGet("user").(*model.User); ok && u != nil {
			operatorID = u.ID
			if u.IsAdmin == 1 {
				operatorType = 2
			} else {
				operatorType = 1
			}
		}
	}
	detailJSON := ""
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			detailJSON = uaTruncate(string(b), uaMaxDetail)
		}
	}
	ip, ua := "", ""
	if c != nil && c.Request != nil {
		ip = uaTruncate(c.ClientIP(), 64)
		ua = uaTruncate(c.Request.UserAgent(), uaMaxUA)
	}
	row := model.UserActionLog{
		UserID: int32(userID), OperatorID: operatorID, OperatorType: operatorType,
		Level: level, Category: uaTruncate(category, 64), Action: uaTruncate(action, 128),
		Message: uaTruncate(message, uaMaxMsg), Detail: detailJSON,
		IP: ip, UserAgent: ua, CreatedAt: time.Now(),
	}
	_ = database.DB.Create(&row).Error
}

// uaDiff 字段级 diff (对齐 PHP UserActionLogger::diff): {field:{from,to}}, 凭据字段只记 changed
func uaDiff(before map[string]interface{}, after map[string]interface{}, fields []string) map[string]interface{} {
	result := map[string]interface{}{}
	norm := func(v interface{}) string {
		if v == nil {
			return ""
		}
		var s string
		switch t := v.(type) {
		case string:
			s = t
		default:
			b, _ := json.Marshal(t)
			s = string(b)
		}
		// 数字归一化: "0.00" 与 "0" / 0 视为同值, 避免 decimal 格式差产生的假 diff
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return strconv.FormatFloat(f, 'g', -1, 64)
		}
		return s
	}
	for _, f := range fields {
		av, ok := after[f]
		if !ok {
			continue
		}
		oldV := norm(before[f])
		newV := norm(av)
		if oldV == newV {
			continue
		}
		if label, cred := uaCredentialFields[f]; cred {
			result[f] = gin.H{"changed": true, "label": label}
			continue
		}
		result[f] = gin.H{"from": uaTruncate(oldV, uaMaxValue), "to": uaTruncate(newV, uaMaxValue)}
	}
	return result
}

// uaDiffHasSensitive 敏感字段变更判定 (决定审计 level)
func uaDiffHasSensitive(diff map[string]interface{}) bool {
	sensitive := []string{"enable", "pass", "passwd", "ga_enable", "ga_token", "v2ray_uuid", "upswd",
		"port", "email", "money", "node_group", "class", "ref_by", "is_admin"}
	for _, f := range sensitive {
		if _, ok := diff[f]; ok {
			return true
		}
	}
	return false
}

// uaAuditUpdate 管理员修改用户资料的审计口径 (对齐 PHP Admin\UserController::update 尾部)
func uaAuditUpdate(c *gin.Context, userID int64, oldEnable, newEnable string, diff map[string]interface{}) {
	if len(diff) == 0 {
		return
	}
	detail := map[string]interface{}{"diff": diff}
	switch {
	case oldEnable != newEnable && newEnable == "0":
		uaLog(c, userID, "warning", "ban", "user.ban", "管理员禁用账号", detail)
	case oldEnable != newEnable && newEnable == "1":
		uaLog(c, userID, "info", "ban", "user.unban", "管理员启用账号", detail)
	case uaDiffHasSensitive(diff):
		uaLog(c, userID, "warning", "admin", "user.update_settings", "管理员修改用户敏感资料", detail)
	}
	// 其余 (未改动/仅非敏感) 不记, 避免噪音 — PHP 同口径
}

// linesToCSV textarea 多行 → 逗号分隔 (PHP: str_replace(PHP_EOL, ",", ...))
func linesToCSV(s string) string {
	s = strings.ReplaceAll(s, "\r\n", ",")
	s = strings.ReplaceAll(s, "\n", ",")
	s = strings.ReplaceAll(s, "\r", ",")
	return s
}

// ==================== 用户操作记录列表 (GET /user-log) ====================

var userLogSortable = map[string]string{
	"id": "id", "user_id": "user_id", "operator_id": "operator_id",
	"level": "level", "category": "category", "created_at": "created_at",
}

// ListUserLogs 操作审计列表 (对齐 PHP AdminController::userLog: 50/页 + 过滤 + 用户名映射)
func (h *Handler) ListUserLogs(c *gin.Context) {
	q := ParseListQuery(c, userLogSortable, "id", "desc")

	query := database.DB.Model(&model.UserActionLog{})
	if v := c.Query("user_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			query = query.Where("user_id = ?", n)
		}
	}
	if v := c.Query("operator_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			query = query.Where("operator_id = ?", n)
		}
	}
	switch c.Query("level") {
	case "info", "warning", "error":
		query = query.Where("level = ?", c.Query("level"))
	}
	if cat := c.Query("category"); cat != "" {
		query = query.Where("category = ?", cat)
	}
	if from := c.Query("from"); from != "" {
		query = query.Where("created_at >= ?", from+" 00:00:00")
	}
	if to := c.Query("to"); to != "" {
		query = query.Where("created_at <= ?", to+" 23:59:59")
	}
	if q.Search != "" {
		like := "%" + q.Search + "%"
		query = query.Where("message LIKE ? OR action LIKE ? OR ip LIKE ?", like, like, like)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}
	var logs []model.UserActionLog
	dbq := query.Order(q.OrderBy + " " + q.OrderDir)
	if q.Limit >= 0 {
		dbq = dbq.Offset(q.Offset).Limit(q.Limit)
	}
	if err := dbq.Find(&logs).Error; err != nil {
		RespondErr(c, "查询失败")
		return
	}

	// 用户名映射 (目标用户 + 操作者)
	uidMap := map[int32]bool{}
	for _, l := range logs {
		uidMap[l.UserID] = true
		uidMap[l.OperatorID] = true
	}
	ids := make([]int32, 0, len(uidMap))
	for id := range uidMap {
		if id > 0 {
			ids = append(ids, id)
		}
	}
	nameMap := map[int32]string{}
	if len(ids) > 0 {
		var rows []struct {
			ID       int32
			UserName string
		}
		database.DB.Model(&model.User{}).Select("id, user_name").Where("id IN ?", ids).Scan(&rows)
		for _, r := range rows {
			nameMap[r.ID] = r.UserName
		}
	}

	type item struct {
		ID           int64  `json:"id"`
		UserID       int32  `json:"user_id"`
		UserName     string `json:"user_name"`
		OperatorID   int32  `json:"operator_id"`
		OperatorName string `json:"operator_name"`
		OperatorType int8   `json:"operator_type"`
		Level        string `json:"level"`
		Category     string `json:"category"`
		Action       string `json:"action"`
		Message      string `json:"message"`
		Detail       string `json:"detail"`
		IP           string `json:"ip"`
		CreatedAt    string `json:"created_at"`
	}
	items := make([]item, 0, len(logs))
	for _, l := range logs {
		items = append(items, item{
			ID: l.ID, UserID: l.UserID, UserName: nameMap[l.UserID],
			OperatorID: l.OperatorID, OperatorName: nameMap[l.OperatorID],
			OperatorType: l.OperatorType, Level: l.Level, Category: l.Category,
			Action: l.Action, Message: l.Message, Detail: l.Detail, IP: l.IP,
			CreatedAt: l.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	RespondList(c, items, total)
}
