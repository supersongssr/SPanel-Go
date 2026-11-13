package handler

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"spanel-go/internal/app/middleware"
	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
	"spanel-go/internal/pkg/netutil"
	"spanel-go/internal/pkg/notify"
	"spanel-go/internal/service"

	"github.com/gin-gonic/gin"
)

// APIv2LifecycleHandler v2 节点生命周期族 (/api/node/* + GET /api/nodes)
//
// 对齐 PHP V2ApiController (docs/api2/node.md 契约):
//   - 鉴权: muKey 三态 (Authorization: Bearer / X-API-Token 头 / token 参数);
//   - 信封: {"status":"error","message":...} (HTTP 200); nginx_config 例外用 HTTP 状态码;
//   - 服务层: internal/service/node_api*.go (NodeApiService 移植)。
type APIv2LifecycleHandler struct{}

var DefaultAPIv2LifecycleHandler = &APIv2LifecycleHandler{}

// authFail 生命周期族鉴权失败响应 (对齐 PHP errorResponse('invalid_token'))
func lifecycleAuth(c *gin.Context) bool {
	if !middleware.CheckMuKey(c, middleware.ModeV2ThreeWay) {
		lifecycleError(c, "invalid_token")
		return false
	}
	return true
}

// allParams 合并 query + form 参数 (对齐 Slim $request->getParams())
func allParams(c *gin.Context) url.Values {
	out := url.Values{}
	for k, vs := range c.Request.URL.Query() {
		for _, v := range vs {
			out.Add(k, v)
		}
	}
	if c.Request.Method == http.MethodPost {
		if err := c.Request.ParseForm(); err == nil {
			for k, vs := range c.Request.PostForm {
				for _, v := range vs {
					out.Add(k, v)
				}
			}
		}
	}
	return out
}

// phpTruthy FILTER_VALIDATE_BOOLEAN 口径: "true"/"1"/"yes"/"on"/"y"/"t" → true
func phpTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes", "y", "t":
		return true
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/node/apply_id — 申请节点 ID (恒新建不复用 + IP 登记 + 保留区保护)
// ─────────────────────────────────────────────────────────────────────────────

// ApplyID POST /api/node/apply_id
func (h *APIv2LifecycleHandler) ApplyID(c *gin.Context) {
	if !lifecycleAuth(c) {
		return
	}
	p := allParams(c)
	nodeID, err := service.AllocateNodeID(p.Get("node_ip"), p.Get("node_ipv6"))
	if err != nil {
		logApiV2("applyId failed err=%v", err)
		lifecycleError(c, "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"node_id": nodeID})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/node/register — 节点注册 (node_id 可省: 面板代分配, 响应回显)
// ─────────────────────────────────────────────────────────────────────────────

// Register POST /api/node/register
func (h *APIv2LifecycleHandler) Register(c *gin.Context) {
	if !lifecycleAuth(c) {
		return
	}
	if database.DB == nil {
		lifecycleError(c, "internal_error")
		return
	}
	p := allParams(c)

	nodeIDRaw := strings.TrimSpace(p.Get("node_id"))
	var nodeID int64
	if nodeIDRaw == "" {
		// 未上报 id: 面板代为分配 (恒新建, 不复用死行)
		var err error
		nodeID, err = service.AllocateNodeID(p.Get("node_ip"), p.Get("node_ipv6"))
		if err != nil {
			logApiV2("register allocate failed err=%v", err)
			lifecycleError(c, "internal_error")
			return
		}
	} else {
		n, err := strconv.ParseInt(nodeIDRaw, 10, 64)
		if err != nil || n <= 0 {
			lifecycleError(c, "internal_error")
			return
		}
		nodeID = n
	}

	nodePort := 443
	if v, err := strconv.Atoi(strings.TrimSpace(p.Get("node_port"))); err == nil && v > 0 {
		nodePort = v
	}
	p.Set("node_port", strconv.Itoa(nodePort))

	result, err := service.RegisterNode(nodeID, p)
	if err != nil {
		logApiV2("register failed node_id=%d err=%v", nodeID, err)
		lifecycleError(c, "internal_error")
		return
	}
	if result == nil {
		result = map[string]any{}
	}
	// 有无上报都回显, 节点端只认响应里的 node_id (对齐 PHP)
	result["node_id"] = nodeID
	result["node_port"] = nodePort
	c.JSON(http.StatusOK, result)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/node/status — 状态/流量心跳 (traffic_used 唯一权威写入方)
// ─────────────────────────────────────────────────────────────────────────────

// Status POST /api/node/status
func (h *APIv2LifecycleHandler) Status(c *gin.Context) {
	if !lifecycleAuth(c) {
		return
	}
	p := allParams(c)
	nodeIDRaw := strings.TrimSpace(p.Get("node_id"))
	if nodeIDRaw == "" {
		lifecycleError(c, "missing_node_id")
		return
	}
	nodeID, err := strconv.ParseInt(nodeIDRaw, 10, 64)
	if err != nil {
		nodeID = 0 // PHP (int) 弱转
	}
	// 上报者真实 IP: 与记录的 node_ip 比对, 检测节点身份盗用 (与 register 同口径)
	result := service.ReportStatus(nodeID, p, remoteIPStr(c))
	c.JSON(http.StatusOK, result)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/node/delete — 节点直接删除通道 (全量级联, 不可逆)
// ─────────────────────────────────────────────────────────────────────────────

// Delete POST /api/node/delete
func (h *APIv2LifecycleHandler) Delete(c *gin.Context) {
	if !lifecycleAuth(c) {
		return
	}
	if database.DB == nil {
		lifecycleError(c, "internal_error")
		return
	}
	p := allParams(c)
	rawID := strings.TrimSpace(p.Get("node_id"))
	nodeID, err := strconv.ParseInt(rawID, 10, 64)
	if rawID == "" || err != nil || nodeID <= 0 {
		lifecycleError(c, "invalid_node_id")
		return
	}
	// 保留区 (id < 100) 不给 API 删, 只能 admin 后台手动删
	if nodeID < minAllocatableID {
		lifecycleError(c, "reserved_id")
		return
	}

	var node model.Node
	if err := database.DB.First(&node, nodeID).Error; err != nil {
		// 幂等: 已被 GC / 重复调用 / purge 后残留检查 → success
		c.JSON(http.StatusOK, gin.H{"status": "success", "node_id": nodeID, "deleted": 0})
		return
	}

	name := node.Name
	stats := service.PurgeNode(&node, "all", 0)

	notify.Send(fmt.Sprintf("节点被删除~%s (id=%d, 直接删除通道)", name, nodeID))

	c.JSON(http.StatusOK, gin.H{
		"status":           "success",
		"node_id":          nodeID,
		"deleted":          1,
		"clones_deleted":   stats["clones_deleted"],
		"dns_deleted":      stats["dns_deleted"],
		"redis_cleared":    stats["redis_cleared"],
		"log_rows_deleted": stats["log_rows_deleted"],
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/node/resolve_dns — 整组 DNS 对账 (CF + DB)
// ─────────────────────────────────────────────────────────────────────────────

// ResolveDNS POST /api/node/resolve_dns
func (h *APIv2LifecycleHandler) ResolveDNS(c *gin.Context) {
	if !lifecycleAuth(c) {
		return
	}
	p := allParams(c)
	nodeIDRaw := strings.TrimSpace(p.Get("node_id"))
	if nodeIDRaw == "" {
		lifecycleError(c, "missing_node_id")
		return
	}
	nodeID, err := strconv.ParseInt(nodeIDRaw, 10, 64)
	if err != nil {
		nodeID = 0
	}
	force := phpTruthy(p.Get("force"))
	result, err := service.ReconcileClusterDNS(nodeID, force)
	if err != nil {
		logApiV2("resolveDns failed node_id=%d err=%v", nodeID, err)
		lifecycleError(c, "internal_error")
		return
	}
	c.JSON(http.StatusOK, result)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/node/config — 生成 xray 配置 (raw JSON + X-Config-Hash)
// ─────────────────────────────────────────────────────────────────────────────

// GetConfig POST /api/node/config
func (h *APIv2LifecycleHandler) GetConfig(c *gin.Context) {
	if !lifecycleAuth(c) {
		return
	}
	p := allParams(c)
	nodeIDRaw := strings.TrimSpace(p.Get("node_id"))
	if nodeIDRaw == "" {
		lifecycleError(c, "missing_node_id")
		return
	}
	if database.DB == nil {
		lifecycleError(c, "internal_error")
		return
	}
	nodeID, err := strconv.ParseInt(nodeIDRaw, 10, 64)
	if err != nil {
		nodeID = 0
	}

	// 结果是 raw JSON 字符串, 直接回写不二次编码;
	// X-Config-Hash = body 的 sha256 —— 节点拉取时记录, 心跳上报与面板侧比对驱动
	// 配置刷新闭环; 同时回填 ss_node.config_hash 缓存 (getConfig 即权威生成)
	configJSON, errMsg, internalErr := service.NodeGenerateXrayConfig(nodeID)
	if internalErr != nil {
		logApiV2("getConfig failed node_id=%d err=%v", nodeID, internalErr)
		lifecycleError(c, "internal_error")
		return
	}
	if errMsg != "" {
		lifecycleError(c, errMsg)
		return
	}

	configHash := fmt.Sprintf("%x", sha256.Sum256([]byte(configJSON)))
	if err := database.DB.Model(&model.Node{}).Where("id = ?", nodeID).
		Update("config_hash", configHash).Error; err != nil {
		logApiV2("getConfig cache config_hash failed node_id=%d err=%v", nodeID, err)
	}

	c.Header("X-Config-Hash", configHash)
	c.Data(http.StatusOK, "application/json", []byte(configJSON))
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/node/nginx_config — 渲染 nginx 配置 (text/plain + HTTP 状态码)
// ─────────────────────────────────────────────────────────────────────────────

// NginxConfig POST /api/node/nginx_config
func (h *APIv2LifecycleHandler) NginxConfig(c *gin.Context) {
	// 例外信封: 鉴权失败直接 401 (对齐 PHP withStatus(401))
	if !middleware.CheckMuKey(c, middleware.ModeV2ThreeWay) {
		c.Status(http.StatusUnauthorized)
		return
	}
	p := allParams(c)
	nodeIDRaw := strings.TrimSpace(p.Get("node_id"))
	if nodeIDRaw == "" {
		c.Status(http.StatusNotFound)
		return
	}
	if database.DB == nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	nodeID, err := strconv.ParseInt(nodeIDRaw, 10, 64)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}

	var node model.Node
	if err := database.DB.First(&node, nodeID).Error; err != nil {
		c.Status(http.StatusNotFound)
		return
	}

	// 解析 server 串取 v2_name; 缺失/未知组回退默认组 (node_default_v2_name)
	serverParams := url.Values{}
	if node.Server != "" {
		if q, err := url.ParseQuery(node.Server); err == nil {
			serverParams = q
		}
	}
	v2Name := serverParams.Get("v2_name")
	if v2Name == "" || service.PresetsGroupConfig(v2Name) == nil {
		v2Name = defaultV2Name()
	}
	if service.PresetsGroupConfig(v2Name) == nil {
		c.Status(http.StatusNotFound)
		return
	}
	tpl, ok := service.NginxTemplate(v2Name)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}

	content := renderNginxTemplate(tpl, v2Name, serverParams, &node)
	c.Data(http.StatusOK, "text/plain", []byte(content))
}

// defaultV2Name 协议组缺省名 (PHP getWithDefault('node_default_v2_name', ...))
func defaultV2Name() string {
	if config.GlobalConfig != nil && config.GlobalConfig.Node.DefaultV2Name != "" {
		return config.GlobalConfig.Node.DefaultV2Name
	}
	return "vision-curvePreferences"
}

// renderNginxTemplate 模板占位符替换 (对齐 PHP V2ApiController::nginxConfig)
func renderNginxTemplate(tpl, v2Name string, serverParams url.Values, node *model.Node) string {
	replacements := map[string]string{}

	// 域名信息: server 串 host 参数首段为子域, 余下为根域
	address := serverParams.Get("host")
	rootDomain := ""
	if parts := strings.SplitN(address, ".", 2); len(parts) == 2 {
		rootDomain = parts[1]
	}
	replacements["__nodeDomain__"] = rootDomain
	replacements["__nodeDomainRegex__"] = rootDomain

	// node_port (server 串自定义端口, 默认 443)
	nodePort := 443
	if v, err := strconv.Atoi(serverParams.Get("node_port")); err == nil && v > 0 {
		nodePort = v
	}
	replacements["__nginxPort__"] = strconv.Itoa(nodePort)

	// 随机 path: 优先 server 串 (register 时生成), 缺失回退协议组默认值
	paths := service.PresetsResolvePaths(v2Name, serverParams)
	// xhttp-verify: server 串读取 (register 时生成的随机 UUID)
	replacements["__xhttpVerify__"] = service.PresetsResolveXhttpVerify(v2Name, serverParams)

	// 协议端口与路径
	if gc := service.PresetsGroupConfig(v2Name); gc != nil {
		if protocols, ok := gc["protocols"].(map[string]any); ok {
			// 保序遍历由 map 顺序决定 (PHP 数组有序); 输出互不冲突, 顺序无影响
			for name, pv := range protocols {
				proto, ok := pv.(map[string]any)
				if !ok {
					continue
				}
				replacements["__"+name+"Port__"] = fmt.Sprintf("%v", proto["local_port"])
				if (name == "xhttp" || name == "ws") && paths[name] != "" {
					replacements["__"+name+"Path__"] = paths[name]
				}
			}
		}
	}
	// grpc service name
	if paths["grpc"] != "" {
		replacements["__v2ServiceName__"] = paths["grpc"]
	}
	// 伪装回落域名
	fallback := "www.apple.com"
	if config.GlobalConfig != nil && config.GlobalConfig.Node.FallbackHost != "" {
		fallback = config.GlobalConfig.Node.FallbackHost
	}
	replacements["__httpProxyHost__"] = fallback

	content := tpl
	for placeholder, value := range replacements {
		content = strings.ReplaceAll(content, placeholder, value)
	}
	return content
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/node/unlock_check — 流媒体解锁检测上报
// ─────────────────────────────────────────────────────────────────────────────

// UnlockCheck POST /api/node/unlock_check
func (h *APIv2LifecycleHandler) UnlockCheck(c *gin.Context) {
	if !lifecycleAuth(c) {
		return
	}
	p := allParams(c)
	nodeIDRaw := strings.TrimSpace(p.Get("node_id"))
	if nodeIDRaw == "" {
		lifecycleError(c, "missing_node_id")
		return
	}
	nodeID, err := strconv.ParseInt(nodeIDRaw, 10, 64)
	if err != nil {
		nodeID = 0
	}
	result, err := service.UpdateUnlock(nodeID, p)
	if err != nil {
		logApiV2("unlockCheck failed node_id=%d err=%v", nodeID, err)
		lifecycleError(c, "internal_error")
		return
	}
	c.JSON(http.StatusOK, result)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/node/edit — 单/多字段编辑 (JSON 信封 {"id":N,"params":{...}})
// ─────────────────────────────────────────────────────────────────────────────

// Edit POST /api/node/edit
func (h *APIv2LifecycleHandler) Edit(c *gin.Context) {
	if !lifecycleAuth(c) {
		return
	}
	// JSON 信封解析: 优先框架解析, 兜底手动解析原始 body (兼容未带 Content-Type 的客户端)
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	// JSON 信封解析: id 用 RawMessage 接收 (对齐 PHP: 先解 JSON, id 非数字字符串
	// 报 invalid_node_id 而不是 invalid_json)
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Params json.RawMessage `json:"params"`
	}
	if err != nil || json.Unmarshal(raw, &envelope) != nil {
		lifecycleError(c, "invalid_json")
		return
	}
	// id 缺省/空串 → missing_node_id; 非数字/非正整数 → invalid_node_id (PHP is_numeric 口径)
	idRaw := strings.TrimSpace(string(envelope.ID))
	if idRaw == "" || idRaw == "null" {
		lifecycleError(c, "missing_node_id")
		return
	}
	idRaw = strings.Trim(idRaw, `"`) // 字符串数字 "123" 同样接受 (PHP is_numeric)
	nodeID, nodeIDErr := strconv.ParseInt(idRaw, 10, 64)
	if nodeIDErr != nil || nodeID <= 0 {
		lifecycleError(c, "invalid_node_id")
		return
	}
	params := map[string]any{}
	if len(envelope.Params) == 0 || json.Unmarshal(envelope.Params, &params) != nil || len(params) == 0 {
		lifecycleError(c, "missing_params")
		return
	}

	c.JSON(http.StatusOK, service.EditNode(nodeID, params))
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/nodes — 全量节点导出 (原始数据库格式, 旧 ret/data 信封)
// ─────────────────────────────────────────────────────────────────────────────

// ListNodes GET /api/nodes
//
// 对齐 PHP V2ApiController::listNodes (Node::all()->toArray()):
// raw SELECT * → map 输出全列原始行, 不经 Go 结构体白名单, 保持与 PHP 输出等价。
func (h *APIv2LifecycleHandler) ListNodes(c *gin.Context) {
	if !lifecycleAuth(c) {
		return
	}
	if database.DB == nil {
		lifecycleError(c, "internal_error")
		return
	}
	rows := make([]map[string]any, 0, 64)
	if err := database.DB.Raw("SELECT * FROM ss_node").Scan(&rows).Error; err != nil {
		logApiV2("listNodes failed err=%v", err)
		lifecycleError(c, "internal_error")
		return
	}
	for _, row := range rows {
		normalizeRawRow(row)
	}
	c.JSON(http.StatusOK, gin.H{"ret": 1, "data": rows})
}

// normalizeRawRow 原始行规整: []byte → string (driver 扫描的字符串/decimal 值)
func normalizeRawRow(row map[string]any) {
	for k, v := range row {
		if b, ok := v.([]byte); ok {
			row[k] = string(b)
		}
	}
}

// remoteIPStr 直连 IP (安全决策专用)
func remoteIPStr(c *gin.Context) string {
	return netutil.RemoteIP(c.Request)
}
