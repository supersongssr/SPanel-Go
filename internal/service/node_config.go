// 节点 Xray 配置生成服务 —— 对齐 PHP app/Services/NodeConfigService.php 的忠实移植。
//
// 输出契约: 生成成功返回完整 xray 配置 JSON 字符串 (对应 PHP
// json_encode($config, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT)),
// 该字符串同时是 NodeComputeConfigHash 的哈希输入 (X-Config-Hash 响应头 /
// ss_node.config_hash 缓存 / 节点本地运行配置 hash 三方比对依据), 必须
// 确定性 (同一节点重复生成逐字节一致) 且值与 PHP 语义等价。
//
// 实现要点:
//   - xray JSON 模板原样内嵌 (文件尾 xrayTemplate* 常量, 与 PHP
//     resources/templates/xray/*.json 逐字节一致);
//   - 占位符替换沿用 PHP 的"先拼串再解码"流程 (str_replace 后 json_decode);
//   - 解码/再编码用保序 JSON 对象模型 (orderedObj), 键序 = 模板出现顺序,
//     新键追加在末尾, 与 PHP 关联数组行为一致, 保证输出确定性;
//   - 编码器复刻 PHP json_encode 的 PRETTY_PRINT (4 空格缩进) 与
//     UNESCAPED_SLASHES / UNESCAPED_UNICODE 转义语义 (仅转义 " \ 与控制字符)。
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"spanel-go/internal/config"
	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"

	"gorm.io/gorm"
)

// ─────────────────────────────────────────────────────────────────────────────
// 入口: generateXrayConfig / computeConfigHash
// ─────────────────────────────────────────────────────────────────────────────

// NodeGenerateXrayConfig 生成节点 xray 配置 (对齐 PHP NodeConfigService::generateXrayConfig)。
//
// 返回值:
//   - configJSON: 成功时的完整 JSON 字符串 (直接回写 body, 不二次编码);
//   - errMsg: 业务错误 (对应 PHP result['status']==='error' 时的 message);
//   - internalErr: 内部异常 (DB 故障等, handler 记日志并返回 internal_error)。
//
// 成功时 errMsg=="" 且 internalErr==nil。
func NodeGenerateXrayConfig(nodeID int64) (configJSON string, errMsg string, internalErr error) {
	if database.DB == nil {
		return "", "", fmt.Errorf("database not initialized")
	}

	// 对齐 PHP Node::find($nodeId)
	var node model.Node
	if err := database.DB.Where("id = ?", nodeID).First(&node).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "node_not_found", nil
		}
		return "", "", fmt.Errorf("load node %d: %w", nodeID, err)
	}

	configJSON, errMsg = nodeGenerateXrayConfigForNode(&node)
	return configJSON, errMsg, nil
}

// nodeGenerateXrayConfigForNode 纯配置生成 (NodeGenerateXrayConfig 去掉 DB
// 查库后的主体, 便于无库环境校验/测试)。nodeID 即 node.ID。
func nodeGenerateXrayConfigForNode(node *model.Node) (configJSON string, errMsg string) {
	nodeID := int64(node.ID)
	// 解析 server 字符串 (对齐 PHP parse_str($node->server, $serverParams))
	serverParams, _ := url.ParseQuery(node.Server)

	// 确定协议组; 未知组回退默认组 (对齐 PHP getWithDefault('node_default_v2_name', ...))
	v2Name := serverParams.Get("v2_name")
	groupConfig := presetsGroup(v2Name)
	if groupConfig == nil {
		v2Name = nodeConfigDefaultV2Name()
		groupConfig = presetsGroup(v2Name)
	}
	if groupConfig == nil {
		return "", "unknown_v2_name"
	}

	// 从组配置取 xray 模板 (对齐 file_exists 检查)
	templateStr, ok := xrayTemplates[groupConfig.Template]
	if !ok {
		return "", "config_template_not_found"
	}

	configRoot, err := parseOrderedJSON(templateStr)
	if err != nil {
		return "", "internal_error"
	}

	// 判定本节点承载的子协议 (clone 节点只处理一个)
	thisProtocol := nodeConfigGetNodeProtocol(node.Server)

	// 多 ID (cluster) 节点处理组内全部协议
	isCluster := node.NodeIDs != ""

	// 构造本节点的 inbound tag 列表
	var inboundTags []string
	vlessVisionTag := ""
	v2Flow := ""

	if thisProtocol != "" && !isCluster && groupConfig.proto(thisProtocol) != nil {
		tag := "proxy-" + thisProtocol
		inboundTags = append(inboundTags, tag)
		if thisProtocol == "vision" {
			vlessVisionTag = tag
			v2Flow = "xtls-rprx-vision"
		}
	} else {
		// cluster / 主节点 / 未知: 包含组内全部 tag
		for _, proto := range groupConfig.Protocols {
			inboundTags = append(inboundTags, "proxy-"+proto.Name)
			if proto.Name == "vision" {
				vlessVisionTag = "proxy-vision"
				v2Flow = "xtls-rprx-vision"
			}
		}
	}

	// 裁剪 inbounds: 仅保留本节点协议 (非 cluster) + api
	if thisProtocol != "" && !isCluster {
		thisTag := "proxy-" + thisProtocol
		if inbounds, ok := configRoot.Get("inbounds").([]any); ok {
			kept := make([]any, 0, len(inbounds))
			for _, inb := range inbounds {
				tag := ""
				if o, ok := inb.(*orderedObj); ok {
					if t, ok := o.Get("tag").(string); ok {
						tag = t
					}
				}
				if tag == thisTag || tag == "api" {
					kept = append(kept, inb)
				}
			}
			configRoot.Set("inbounds", kept)
		}
	}

	// 域名信息: host (域名) 拆根域 (对齐 PHP explode('.', $address, 2))
	address := serverParams.Get("host")
	rootDomain := ""
	if parts := strings.SplitN(address, ".", 2); len(parts) == 2 {
		rootDomain = parts[1]
	}

	// 数据库连接信息 (对齐 PHP getDbConfig)
	dbConfig := nodeConfigDBConfig()

	// node_port (server 串自定义端口, 缺省 443; 对齐 PHP (int)($serverParams['node_port'] ?? 443))
	nodePort := int64(443)
	if vs, ok := serverParams["node_port"]; ok && len(vs) > 0 {
		nodePort = phpInt(vs[0])
	}

	// 随机 path: 优先节点 server 串 (register 期生成), 缺失回退协议组默认值
	paths := presetsResolvePaths(v2Name, serverParams)

	// REALITY per-node 密钥从 server 串读取; 非 REALITY 组返回 nil
	reality := presetsResolveReality(v2Name, serverParams)

	// 按组配置构造占位符替换表 (顺序对齐 PHP $replacements 插入顺序)
	type repl struct{ placeholder, value string }
	var replacements []repl

	for _, proto := range groupConfig.Protocols {
		name := proto.Name
		switch {
		case name == "hy2" || name == "vision":
			// 对外协议 → 用 node_port
			replacements = append(replacements, repl{"__" + name + "Port__", strconv.FormatInt(nodePort, 10)})
		case presetsIsReality(v2Name) && name == "xhttp":
			// xhttp-reality: xray 直接监听 node_port (443), reality 负责 TLS 握手,
			// xhttp 传输在 reality 解密后的隧道内运行 — 不经 nginx 反代
			replacements = append(replacements, repl{"__" + name + "Port__", strconv.FormatInt(nodePort, 10)})
		default:
			// 内部协议 (xhttp/ws/grpc) → 保持 local_port
			replacements = append(replacements, repl{"__" + name + "Port__", strconv.Itoa(proto.LocalPort)})
		}

		// path 占位符 (xhttp / ws) — 使用随机 path
		if (name == "xhttp" || name == "ws") && paths[name] != "" {
			replacements = append(replacements, repl{"__" + name + "Path__", paths[name]})
		}
	}

	// grpc serviceName — 使用随机 path
	if paths["grpc"] != "" {
		replacements = append(replacements, repl{"__v2ServiceName__", paths["grpc"]})
	}

	replacements = append(replacements, repl{"__nodeDomain__", rootDomain})
	fallbackHost := nodeConfigFallbackHost()
	replacements = append(replacements, repl{"__HYSTERIA_URL__", "http://" + fallbackHost + ":80"})
	replacements = append(replacements, repl{"__v2Fallback__", "remote-" + fallbackHost})

	// XHTTP padding 混淆占位符 (xhttp-cdn 系列): 键名/header 按节点解析
	// (server 串优先, 缺失时按节点 ID 种子现算 — 与注册期/订阅渲染期同种子幂等);
	// 数值范围取 V2Presets 常量 (单一事实源)。非 CDN 组模板不含这些占位符,
	// 替换为空操作, 安全。
	if xobfs := presetsResolveXobfs(v2Name, serverParams, int64(node.ID)); xobfs != nil {
		replacements = append(replacements, repl{"__xPaddingKey__", phpJSONEscape(xobfs["key"])})
		replacements = append(replacements, repl{"__xPaddingHeader__", phpJSONEscape(xobfs["header"])})
		replacements = append(replacements, repl{"__xPaddingBytes__", presetsXHTTPXPaddingBytes})
		replacements = append(replacements, repl{"__scMaxEachPostBytes__", presetsXHTTPSCMaxEachPostBytes})
		replacements = append(replacements, repl{"__scMinPostsIntervalMs__", presetsXHTTPSCMinPostsIntervalMs})
	}

	// REALITY 占位符: private/shortId 仅服务端持有; serverName = 偷自己 (节点 host)
	if presetsIsReality(v2Name) {
		private, shortID := "", ""
		if reality != nil {
			private, shortID = reality["private"], reality["shortId"]
		}
		replacements = append(replacements, repl{"__realityPrivateKey__", private})
		replacements = append(replacements, repl{"__realityShortId__", shortID})
		replacements = append(replacements, repl{"__realityServerName__", address})
	}

	// __inboundTags__ 按 JSON 片段构造 (implode '", "'), 不得过 jsonEscape
	replacements = append(replacements, repl{"__inboundTags__", strings.Join(inboundTags, `", "`)})
	replacements = append(replacements, repl{"__vlessVisionTag__", vlessVisionTag})
	replacements = append(replacements, repl{"__v2Flow__", v2Flow})

	// 外部配置值经 jsonEscape 后注入: 值含 " 或 \ 等 JSON 元字符时产生合法转义,
	// 否则替换后整份模板 JSON 非法、解码返回 null (旧插件拿到无用户段的空配置
	// 且面板侧无错误可见 — 静默故障)
	replacements = append(replacements, repl{"__dbHost__", phpJSONEscape(dbConfig["host"])})
	replacements = append(replacements, repl{"__dbUser__", phpJSONEscape(dbConfig["user"])})
	replacements = append(replacements, repl{"__dbPassword__", phpJSONEscape(dbConfig["password"])})
	replacements = append(replacements, repl{"__dbName__", phpJSONEscape(dbConfig["dbname"])})

	// v2 面板 API 模式 (xray-plugin-api 插件, 配置段 panelApi):
	// api_token 已签发的节点 → 填入真实 baseURL/token; 未签发节点 → 渲染后
	// 删除 panelApi 段。baseURL 优先取专用 node_api_base_url, 回退站点 baseUrl。
	apiToken := node.APIToken
	if apiToken != "" {
		apiBase := nodeConfigAPIBaseURL()
		apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
		// 与 routes.php 的 /api/v2/backend 路由组对应
		replacements = append(replacements, repl{"__panelBaseURL__", phpJSONEscape(apiBase + "/api/v2/backend")})
		replacements = append(replacements, repl{"__panelToken__", phpJSONEscape(apiToken)})
	}

	// 在原始模板串上做替换 (保留模板格式), 再解码校验
	rendered := templateStr
	for _, r := range replacements {
		rendered = strings.ReplaceAll(rendered, r.placeholder, r.value)
	}
	configRoot, err = parseOrderedJSON(rendered)
	if err != nil {
		// 解码失败必须显式报错, 不能静默继续 (注入值已转义, 此处为兜底防线)
		return "", "config_render_failed"
	}

	// 仅 node_group=1 保留测速限制; 其他分组不将测速域名路由到 block
	nodeConfigApplySpeedtestPolicy(configRoot, int(node.NodeGroup))

	// ssrpanel 段写入节点 ID
	if ssrpanel, ok := configRoot.Get("ssrpanel").(*orderedObj); ok {
		ssrpanel.Set("nodeId", nodeID)
	}

	// panelApi 段裁剪:
	//   - 未签发 api_token: 删除 panelApi 段 → 输出与旧 srp 时代一致 (ssrpanel 保留)
	//   - 已签发 api_token: 保留 panelApi, 默认删除 ssrpanel 段 (节点已走 HTTP API,
	//     不应继续持有库地址/账号/密码); 回滚开关 node_api_keep_mysql_credentials
	//     可临时恢复双段下发
	//   - Phase 4 收尾开关 node_api_drop_mysql_credentials: 已签发 token 的节点删除
	//     ssrpanel 段; 未签发节点保留 (无 panelApi 替代用户源, 删掉即静默故障)
	// 注意: 两个 isset 按顺序判定 — 前一步删除 panelApi 后, 第二个 isset 已不成立
	if _, ok := configRoot.Get("panelApi").(*orderedObj); ok && apiToken == "" {
		configRoot.Del("panelApi")
	}
	if _, ok := configRoot.Get("panelApi").(*orderedObj); ok && !nodeConfigFlagKeepMySQLCredentials() {
		configRoot.Del("ssrpanel")
	}
	if nodeConfigFlagDropMySQLCredentials() {
		if apiToken != "" {
			configRoot.Del("ssrpanel")
		} else {
			log.Printf("[NodeConfig] node_api_drop_mysql_credentials=true 但 node_id=%d 未签发 api_token, "+
				"无 panelApi 替代用户源, 保留 ssrpanel 段(请先补签发 token)", nodeID)
		}
	}

	// 远程解锁模板注入: 仅节点本地未解锁 (No) 的服务注入 (Yes/未上报均直连)
	nodeUnlockParams, _ := url.ParseQuery(node.NodeUnlock)
	nodeConfigInjectUnlockTemplates(configRoot, nodeUnlockParams)

	// 端口统一为整数
	nodeConfigEnsureIntPorts(configRoot)

	// PHP 此处将 policy.levels / stats / ssrpanel.user.flows 由数组强制转为对象
	// ((object) 强转, 规避 PHP 空数组/数字键数组编码成 [] 的问题) — Go 的
	// orderedObj 天然是 JSON 对象, 解码即对象, 无需转换。

	// panelApi.user.flows: 过滤空键/空值占位符残留; 全空则删除 flows
	// (vision 不在本节点时占位符残留空键; 新插件 GetFlow 查不到时回退 flow 字段)
	if panelAPI, ok := configRoot.Get("panelApi").(*orderedObj); ok {
		if user, ok := panelAPI.Get("user").(*orderedObj); ok {
			if flows, ok := user.Get("flows").(*orderedObj); ok {
				filtered := newOrderedObj()
				for _, k := range flows.keys {
					v, isStr := flows.vals[k].(string)
					if strings.TrimSpace(k) != "" && (!isStr || strings.TrimSpace(v) != "") {
						filtered.Set(k, flows.vals[k])
					}
				}
				if len(filtered.keys) == 0 {
					user.Del("flows")
				} else {
					user.Set("flows", filtered)
				}
			}
		}
	}

	// inbounds / outbounds 里的空 settings 保持对象 (PHP: 空数组 (object) 强转 → {})
	for _, key := range []string{"inbounds", "outbounds"} {
		if arr, ok := configRoot.Get(key).([]any); ok {
			for _, item := range arr {
				if o, ok := item.(*orderedObj); ok {
					if settings, ok := o.Get("settings").([]any); ok && len(settings) == 0 {
						o.Set("settings", newOrderedObj())
					}
				}
			}
		}
	}

	// 元数据作为顶层键 (xray 忽略)
	configRoot.Set("status", "success")
	configRoot.Set("node_id", nodeID)

	// 单次干净编码 — 不转义斜杠/unicode, 4 空格缩进
	return phpJSONEncodePretty(configRoot), ""
}

// NodeComputeConfigHash 对齐 PHP NodeConfigService::computeConfigHash。
// 计算节点当前 xray 配置的 sha256 指纹 (与 getConfig 下发 body 逐字节同源,
// X-Config-Hash 响应头 / ss_node.config_hash / 节点本地配置 hash 三方比对)。
// 生成失败 (节点不存在/模板缺失等) 返回 ""。
func NodeComputeConfigHash(nodeID int64) string {
	configJSON, errMsg, internalErr := NodeGenerateXrayConfig(nodeID)
	if internalErr != nil {
		log.Printf("[NodeConfig] computeConfigHash failed: node_id=%d %v", nodeID, internalErr)
		return ""
	}
	if errMsg != "" {
		return "" // error array → 无指纹
	}
	sum := sha256.Sum256([]byte(configJSON))
	return hex.EncodeToString(sum[:])
}

// ─────────────────────────────────────────────────────────────────────────────
// 配置读取 (PHP Config::get 对应物; Go config 未含的键硬编码 config.default.php 默认值)
// ─────────────────────────────────────────────────────────────────────────────

// nodeConfigDefaultV2Name 对齐 PHP Config::getWithDefault('node_default_v2_name', 'vision-curvePreferences')。
func nodeConfigDefaultV2Name() string {
	if config.GlobalConfig != nil && config.GlobalConfig.Node.DefaultV2Name != "" {
		return config.GlobalConfig.Node.DefaultV2Name
	}
	return "vision-curvePreferences"
}

// nodeConfigFallbackHost 对齐 PHP Config::get('node_fallback_host') ?: 'www.apple.com'
// (PHP config.default.php 未定义该键 → 恒回退默认值; Go 侧 Node.FallbackHost 为空亦回退)。
func nodeConfigFallbackHost() string {
	if config.GlobalConfig != nil && config.GlobalConfig.Node.FallbackHost != "" {
		return config.GlobalConfig.Node.FallbackHost
	}
	return "www.apple.com"
}

// nodeConfigAPIBaseURL 对齐 PHP Config::getWithDefault('node_api_base_url', ”) 回退
// Config::getWithDefault('baseUrl', ”)。两键 Go 配置均未含 → 硬编码 PHP
// config.default.php 默认值 (node_api_base_url=” → 回退 baseUrl='https://abc.xyz')。
func nodeConfigAPIBaseURL() string {
	return "https://abc.xyz"
}

// nodeConfigFlagKeepMySQLCredentials 对齐 PHP flagEnabled('node_api_keep_mysql_credentials')
// (严格布尔: 仅 true/'true' 开启; config.default.php 未定义 → 缺省 false)。
func nodeConfigFlagKeepMySQLCredentials() bool {
	return false
}

// nodeConfigFlagDropMySQLCredentials 对齐 PHP flagEnabled('node_api_drop_mysql_credentials')
// (严格布尔: 仅 true/'true' 开启; config.default.php 未定义 → 缺省 false)。
func nodeConfigFlagDropMySQLCredentials() bool {
	return false
}

// nodeConfigDBConfig 对齐 PHP NodeConfigService::getDbConfig。
// db_remote_host (Go/PHP 默认配置均未定义 → 恒空) 回退 db_host (Go Database.Host),
// 再回退 '127.0.0.1'; user/password/dbname 回退 'root' / ” / 'sspanel'。
func nodeConfigDBConfig() map[string]string {
	host, user, password, dbname := "", "", "", ""
	if config.GlobalConfig != nil {
		host = config.GlobalConfig.Database.Host
		user = config.GlobalConfig.Database.Username
		password = config.GlobalConfig.Database.Password
		dbname = config.GlobalConfig.Database.Database
	}
	const dbRemoteHost = "" // PHP Config::get('db_remote_host'), config.default.php 未定义
	if dbRemoteHost != "" {
		host = dbRemoteHost
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if user == "" {
		user = "root"
	}
	if dbname == "" {
		dbname = "sspanel"
	}
	return map[string]string{
		"host":     host,
		"user":     user,
		"password": password,
		"dbname":   dbname,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 私有辅助 (对齐 PHP NodeConfigService 私有方法)
// ─────────────────────────────────────────────────────────────────────────────

// nodeConfigGetNodeProtocol 对齐 PHP getNodeProtocol: 按 server 串 net= 判定本节点协议。
func nodeConfigGetNodeProtocol(server string) string {
	if server == "" {
		return ""
	}
	params, _ := url.ParseQuery(server)
	switch params.Get("net") {
	case "ws":
		return "ws"
	case "grpc":
		return "grpc"
	case "hysteria2":
		return "hy2"
	case "tcp":
		return "vision"
	case "xhttp":
		return "xhttp"
	}
	return ""
}

// nodeConfigApplySpeedtestPolicy 对齐 PHP applySpeedtestPolicy。
// node_group=1 保留模板的测速封锁条目, 其他分组移除测速域名 (无关 block 规则不变)。
func nodeConfigApplySpeedtestPolicy(root *orderedObj, nodeGroup int) {
	if nodeGroup == 1 {
		return
	}
	routing, ok := root.Get("routing").(*orderedObj)
	if !ok {
		return
	}
	rules, ok := routing.Get("rules").([]any)
	if !ok || len(rules) == 0 {
		return
	}

	speedtestDomains := map[string]bool{
		"geosite:ookla-speedtest":      true,
		"domain:cachefly.cachefly.net": true,
		"domain:speed.cloudflare.com":  true,
	}

	for _, r := range rules {
		rule, ok := r.(*orderedObj)
		if !ok {
			continue
		}
		outboundTag, _ := rule.Get("outboundTag").(string)
		if outboundTag != "block" {
			continue
		}
		domain, ok := rule.Get("domain").([]any)
		if !ok {
			continue
		}
		// 对齐 PHP array_values(array_diff(...)): 保序剔除, 重新索引
		kept := make([]any, 0, len(domain))
		for _, d := range domain {
			if s, ok := d.(string); ok && speedtestDomains[s] {
				continue
			}
			kept = append(kept, d)
		}
		rule.Set("domain", kept)
	}
}

// unlockSvcConf 远程解锁代理配置 (对齐 PHP Config::get('node_unlock') 每服务条目)。
type unlockSvcConf struct {
	Address  string
	Port     string // PHP 配置值原样 (字符串), 空 = 不替换模板默认端口
	Method   string // 空默认 aes-256-gcm
	Password string
}

// nodeUnlockProxyConfig 对齐 PHP Config::get('node_unlock')。
// PHP config.default.php 仅定义 'unlock' 键 (代码读取的 'node_unlock' 键未定义,
// 即便读 'unlock' 其各 address/password 默认亦为空) → 默认空配置 = 所有服务
// 因 address/password 为空而不注入远程解锁路由, 与 PHP 默认行为一致。
// 生产若需启用, 在此填入各服务 address/port/method/password。
var nodeUnlockProxyConfig = map[string]unlockSvcConf{}

// unlockPortRe 对齐 PHP preg_replace('/"port":\d+/', ..., 1): 替换首个端口字面量。
var unlockPortRe = regexp.MustCompile(`"port":\d+`)

// nodeConfigInjectUnlockTemplates 对齐 PHP injectUnlockTemplates。
// 语义: No = 节点未解锁 → 注入远程解锁 outbound (shadowsocks) + geosite 路由;
// Yes / 未上报 → 直连不注入。同模板多别名 (claude/anthropic) 只注入一次。
func nodeConfigInjectUnlockTemplates(root *orderedObj, nodeUnlock url.Values) {
	// 解锁服务 → 配置键 (顺序对齐 PHP 数组, 保证多服务注入顺序确定)
	type svcEntry struct{ key, templateName string }
	unlockServices := []svcEntry{
		{"openai", "openai"},
		{"claude", "claude"},
		{"anthropic", "claude"},
		{"netflix", "netflix"},
		{"tiktok", "tiktok"},
		{"disney", "disney"},
		{"bahamut", "bahamut"},
		{"google_scholar", "google_scholar"},
	}

	injectedTemplates := map[string]bool{}

	for _, svc := range unlockServices {
		if injectedTemplates[svc.templateName] {
			continue
		}
		// 三态: 仅明确 'No' (含 no/false/0/off 前缀) 才注入;
		// 'Yes' 或未上报 (含检测失败) 均直连不注入
		if nodeUnlockStatus(nodeUnlock, svc.key) != "No" {
			continue
		}
		injectedTemplates[svc.templateName] = true

		templateStr, ok := xrayUnlockTemplates[svc.templateName]
		if !ok {
			continue
		}
		tpl, err := parseOrderedJSON(templateStr)
		if err != nil {
			continue
		}

		svcCfg := nodeUnlockProxyConfig[svc.templateName]
		address := svcCfg.Address
		port := 0
		if svcCfg.Port != "" {
			port = int(phpInt(svcCfg.Port))
		}
		method := svcCfg.Method
		if method == "" {
			method = "aes-256-gcm"
		}
		password := svcCfg.Password

		if address == "" || password == "" {
			continue
		}

		// 解锁代理配置值同样经 jsonEscape 注入 (与主模板占位符同口径):
		// 值含 JSON 元字符时裸替换会使子模板 JSON 非法、解锁 outbound/路由被静默跳过
		rendered := phpJSONEncodeCompact(tpl)
		rendered = strings.ReplaceAll(rendered, "__address__", phpJSONEscape(address))
		rendered = strings.ReplaceAll(rendered, "__method__", phpJSONEscape(method))
		rendered = strings.ReplaceAll(rendered, "__password__", phpJSONEscape(password))
		if port > 0 && port != 10010 {
			if loc := unlockPortRe.FindStringIndex(rendered); loc != nil {
				rendered = rendered[:loc[0]] + `"port":` + strconv.Itoa(port) + rendered[loc[1]:]
			}
		}
		tpl, err = parseOrderedJSON(rendered)
		if err != nil {
			// 兜底防线: 值已转义, 走到这里说明模板文件本身损坏; 留痕后跳过
			log.Printf("[NodeConfig] unlock template decode failed service=%s, skip remote unlock injection", svc.templateName)
			continue
		}

		// 合并 outbounds (追加在既有之后)
		if tplOutbounds, ok := tpl.Get("outbounds").([]any); ok {
			cfgOutbounds, _ := root.Get("outbounds").([]any)
			merged := make([]any, 0, len(cfgOutbounds)+len(tplOutbounds))
			merged = append(merged, cfgOutbounds...)
			merged = append(merged, tplOutbounds...)
			root.Set("outbounds", merged)
		}

		// 合并路由规则 (解锁规则插在最前)
		if tplRouting, ok := tpl.Get("routing").(*orderedObj); ok {
			if tplRules, ok := tplRouting.Get("rules").([]any); ok {
				cfgRouting, ok := root.Get("routing").(*orderedObj)
				if !ok {
					cfgRouting = newOrderedObj()
					root.Set("routing", cfgRouting)
				}
				cfgRules, _ := cfgRouting.Get("rules").([]any)
				merged := make([]any, 0, len(tplRules)+len(cfgRules))
				merged = append(merged, tplRules...)
				merged = append(merged, cfgRules...)
				cfgRouting.Set("rules", merged)
			}
		}
	}
}

// nodeConfigEnsureIntPorts 对齐 PHP ensureIntPorts: inbounds 端口统一整数。
func nodeConfigEnsureIntPorts(root *orderedObj) {
	inbounds, ok := root.Get("inbounds").([]any)
	if !ok {
		return
	}
	for _, inb := range inbounds {
		o, ok := inb.(*orderedObj)
		if !ok {
			continue
		}
		if v, ok := o.vals["port"]; ok { // 对齐 PHP isset 检查
			o.Set("port", phpIntValue(v))
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// NodeUnlock 三态判定 (对齐 PHP app/Utils/NodeUnlock.php + NodeApiService 常量)
// ─────────────────────────────────────────────────────────────────────────────

// nodeUnlockAliasToStorageKey 对齐 PHP NodeApiService::UNLOCK_ALIAS_TO_STORAGE_KEY。
var nodeUnlockAliasToStorageKey = map[string]string{
	"netflix":        "Netflix",
	"openai":         "OpenAI",
	"chatgpt":        "OpenAI",
	"disney":         "Disney",
	"bing":           "Bing",
	"claude":         "Claude",
	"anthropic":      "Claude",
	"gemini":         "Gemini",
	"bahamut":        "Bahamut",
	"mewatch":        "MeWatch",
	"tiktok":         "TikTok",
	"bilibili":       "Bilibili",
	"iqiyi":          "iQIYI",
	"google_scholar": "Google_Scholar",
	"notebooklm":     "NotebookLM",
}

// 未解锁前缀 (对齐 PHP NodeApiService::UNLOCK_NO_PREFIX = '/^(0|no|false|off)/i')
var nodeUnlockNoPrefix = regexp.MustCompile(`(?i)^(0|no|false|off)`)

// 检测失败前缀 (对齐 PHP UNLOCK_FAILED_PREFIX = '/^(failed|error|timeout|null|unknown)/i')
var nodeUnlockFailedPrefix = regexp.MustCompile(`(?i)^(failed|error|timeout|null|unknown)`)

// nodeUnlockCanonical 对齐 PHP NodeUnlock::canonical (别名归一, 未知服务名小写原样)。
func nodeUnlockCanonical(service string) string {
	lower := strings.ToLower(strings.TrimSpace(service))
	if v, ok := nodeUnlockAliasToStorageKey[lower]; ok {
		return v
	}
	return lower
}

// nodeUnlockTristate 对齐 PHP NodeUnlock::tristate: 检测失败 → ""(未知);
// No 前缀 → "No"; 其余 → "Yes"。
func nodeUnlockTristate(value string) string {
	value = strings.TrimSpace(value)
	if nodeUnlockFailedPrefix.MatchString(value) {
		return ""
	}
	if nodeUnlockNoPrefix.MatchString(value) {
		return "No"
	}
	return "Yes"
}

// nodeUnlockStatus 对齐 PHP NodeUnlock::status (三态: "Yes" | "No" | ""=未上报/未知)。
// 入参为已解析的 node_unlock 键值 (对应 PHP 传入 parse_str 后的数组)。
func nodeUnlockStatus(raw url.Values, service string) string {
	if len(raw) == 0 {
		return ""
	}
	target := nodeUnlockCanonical(service)
	for k, vs := range raw {
		if nodeUnlockCanonical(k) != target {
			continue
		}
		v := ""
		if len(vs) > 0 {
			v = vs[0]
		}
		return nodeUnlockTristate(v)
	}
	return ""
}

// ─────────────────────────────────────────────────────────────────────────────
// PHP 语义工具
// ─────────────────────────────────────────────────────────────────────────────

// phpInt 复刻 PHP (int) 字符串强转: 跳过前导空白, 可选符号, 取前导连续数字,
// 无数字返回 0, 溢出饱和到 int64 边界。
func phpInt(s string) int64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '\v' || s[i] == '\f') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return 0
	}
	n, err := strconv.ParseInt(s[start:i], 10, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			if neg {
				return math.MinInt64
			}
			return math.MaxInt64
		}
		return 0
	}
	if neg {
		n = -n
	}
	return n
}

// phpIntValue 对任意 JSON 值复刻 PHP (int) 强转 (端口等数值规整用)。
func phpIntValue(v any) int64 {
	switch t := v.(type) {
	case json.Number:
		return phpInt(t.String())
	case string:
		return phpInt(t)
	case int64:
		return t
	case int:
		return int64(t)
	case bool:
		if t {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// phpJSONEscape 对齐 PHP NodeConfigService::jsonEscape:
// json_encode(UNESCAPED_SLASHES|UNESCAPED_UNICODE) 后去掉首尾引号 —
// 即仅转义 " \ 与控制字符 (不转义 / 与非 ASCII 字符)。
// 值不含 JSON 元字符时输出与原值逐字节一致 (模板渲染零回归);
// __inboundTags__ 之类按 JSON 片段构造的值不得过此函数。
func phpJSONEscape(value string) string {
	var b strings.Builder
	writePHPStringBody(&b, value)
	return b.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// 保序 JSON 对象模型 + PHP 语义编码器
// (复刻 PHP json_decode(assoc) + json_encode(PRETTY|UNESCAPED_*) 行为)
// ─────────────────────────────────────────────────────────────────────────────

// orderedObj 保序对象: keys 记录插入顺序, vals 存值。
// 对应 PHP 关联数组: 已有键赋值保持原位, 新键追加末尾。
type orderedObj struct {
	keys []string
	vals map[string]any
}

func newOrderedObj() *orderedObj {
	return &orderedObj{vals: make(map[string]any)}
}

// Has 对齐 PHP isset($arr[$k])。
func (o *orderedObj) Has(k string) bool {
	_, ok := o.vals[k]
	return ok
}

// Get 取值, 缺失返回 nil。
func (o *orderedObj) Get(k string) any {
	return o.vals[k]
}

// Set 赋值: 已有键原位替换, 新键追加末尾 (对齐 PHP 数组赋值语义)。
func (o *orderedObj) Set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// Del 删除键 (对齐 PHP unset)。
func (o *orderedObj) Del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, kk := range o.keys {
		if kk == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// parseOrderedJSON 解析 JSON 文本为保序对象树 (对齐 PHP json_decode($s, true),
// 数字保留字面量 json.Number — 再编码时逐字节还原)。
func parseOrderedJSON(s string) (*orderedObj, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	v, err := parseOrderedValue(dec)
	if err != nil {
		return nil, err
	}
	root, ok := v.(*orderedObj)
	if !ok {
		return nil, fmt.Errorf("json root is not an object")
	}
	// 对齐 PHP json_decode: 尾随非空白内容视为非法
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing data after json document")
	}
	return root, nil
}

// parseOrderedValue 读取下一个 token 并递归解析为值。
func parseOrderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return parseOrderedToken(dec, tok)
}

// parseOrderedToken 将已取出的 token 归一为值 (容器型 token 递归展开, 保序)。
func parseOrderedToken(dec *json.Decoder, tok json.Token) (any, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := newOrderedObj()
			for {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				if d, ok := kt.(json.Delim); ok && d == '}' {
					return o, nil
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("unexpected object key token %v", kt)
				}
				v, err := parseOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(key, v)
			}
		case '[':
			arr := []any{}
			for {
				t2, err := dec.Token()
				if err != nil {
					return nil, err
				}
				if d, ok := t2.(json.Delim); ok && d == ']' {
					return arr, nil
				}
				v, err := parseOrderedToken(dec, t2)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	case string:
		return t, nil
	case json.Number:
		return t, nil
	case bool:
		return t, nil
	case nil:
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected token %v", tok)
}

// phpJSONEncodePretty 复刻 PHP json_encode($v, JSON_UNESCAPED_SLASHES |
// JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT): 4 空格缩进, "key": value,
// 空对象/空数组内联 {} / []。
func phpJSONEncodePretty(v any) string {
	var b strings.Builder
	writePHPValue(&b, v, 0, true)
	return b.String()
}

// phpJSONEncodeCompact 复刻 PHP json_encode($v) 紧凑形态 (占位符替换的中间载体,
// 替换后会重新解码, 转义细节与 PHP 差异无影响)。
func phpJSONEncodeCompact(v any) string {
	var b strings.Builder
	writePHPValue(&b, v, 0, false)
	return b.String()
}

func writePHPIndent(b *strings.Builder, depth int) {
	for i := 0; i < depth; i++ {
		b.WriteString("    ")
	}
}

// writePHPValue 递归输出 PHP 语义 JSON。
func writePHPValue(b *strings.Builder, v any, depth int, pretty bool) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		b.WriteByte('"')
		writePHPStringBody(b, t)
		b.WriteByte('"')
	case json.Number:
		b.WriteString(t.String())
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case int:
		b.WriteString(strconv.Itoa(t))
	case *orderedObj:
		if len(t.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{")
		for i, k := range t.keys {
			if pretty {
				b.WriteString("\n")
				writePHPIndent(b, depth+1)
			}
			b.WriteByte('"')
			writePHPStringBody(b, k)
			b.WriteByte('"')
			if pretty {
				b.WriteString(": ")
			} else {
				b.WriteString(":")
			}
			writePHPValue(b, t.vals[k], depth+1, pretty)
			if i < len(t.keys)-1 {
				b.WriteString(",")
			}
		}
		if pretty {
			b.WriteString("\n")
			writePHPIndent(b, depth)
		}
		b.WriteString("}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[")
		for i, item := range t {
			if pretty {
				b.WriteString("\n")
				writePHPIndent(b, depth+1)
			}
			writePHPValue(b, item, depth+1, pretty)
			if i < len(t)-1 {
				b.WriteString(",")
			}
		}
		if pretty {
			b.WriteString("\n")
			writePHPIndent(b, depth)
		}
		b.WriteString("]")
	case []string:
		// 兼容路径 (正常不应出现)
		arr := make([]any, len(t))
		for i, s := range t {
			arr[i] = s
		}
		writePHPValue(b, arr, depth, pretty)
	default:
		// 兜底: 其余标量按字符串化输出 (对齐 PHP (string) 强转倾向)
		b.WriteByte('"')
		writePHPStringBody(b, fmt.Sprint(t))
		b.WriteByte('"')
	}
}

// writePHPStringBody 复刻 PHP json_encode 字符串转义 (UNESCAPED_SLASHES |
// UNESCAPED_UNICODE): 仅转义 " \ \b \f \n \r \t 与其余控制字符 (\u00xx 小写 hex),
// 斜杠与多字节字符原样输出。
func writePHPStringBody(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\b':
			b.WriteString(`\b`)
		case c == '\f':
			b.WriteString(`\f`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20:
			b.WriteString(`\u00`)
			const hexDigits = "0123456789abcdef"
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xf])
		default:
			b.WriteByte(c)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// xray 配置模板 (原样内嵌, 与 PHP resources/templates/xray/*.json 逐字节一致;
// 由脚本从 PHP 模板目录生成, 勿手改 — 修改请同步 PHP 侧后重新生成)
// ─────────────────────────────────────────────────────────────────────────────
// vision-curvePreferences-chrome.json — 对应 PHP resources/templates/xray/vision-curvePreferences-chrome.json
const xrayTemplateVisionCurvePreferencesChrome = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__visionPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none",
                "fallbacks": [
                    {
                        "dest": "127.0.0.1:8088"
                    }
                ]
            },
            "streamSettings": {
                "network": "tcp",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.2",
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ],
                    "curvePreferences": [
                        "x25519mlkem768"
                    ]
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-vision"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// vision-curvePreferences-hy2.json — 对应 PHP resources/templates/xray/vision-curvePreferences-hy2.json
const xrayTemplateVisionCurvePreferencesHy2 = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__visionPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none",
                "fallbacks": [
                    {
                        "dest": "127.0.0.1:8088"
                    }
                ]
            },
            "streamSettings": {
                "network": "tcp",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.2",
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ],
                    "curvePreferences": [
                        "x25519mlkem768"
                    ]
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-vision"
        },
        {
            "port": "__hy2Port__",
            "protocol": "hysteria",
            "settings": {
                "version": 2
            },
            "streamSettings": {
                "network": "hysteria",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.3",
                    "maxVersion": "1.3",
                    "alpn": ["h3"],
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ]
                },
                "hysteriaSettings": {
                    "version": 2,
                    "udpIdleTimeout": 60,
                    "masquerade": {
                        "type": "proxy",
                        "url": "__HYSTERIA_URL__",
                        "rewriteHost": true
                    }
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-hy2"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// vision-curvePreferences.json — 对应 PHP resources/templates/xray/vision-curvePreferences.json
const xrayTemplateVisionCurvePreferences = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__visionPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none",
                "fallbacks": [
                    {
                        "dest": "127.0.0.1:8088"
                    }
                ]
            },
            "streamSettings": {
                "network": "tcp",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.2",
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ],
                    "curvePreferences": [
                        "x25519mlkem768"
                    ]
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-vision"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// vision-hy2-ws-grpc.json — 对应 PHP resources/templates/xray/vision-hy2-ws-grpc.json
const xrayTemplateVisionHy2WsGrpc = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__visionPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none",
                "fallbacks": [
                    {
                        "dest": "127.0.0.1:8088"
                    }
                ]
            },
            "streamSettings": {
                "network": "tcp",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.2",
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ]
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-vision"
        },
        {
            "port": "__hy2Port__",
            "protocol": "hysteria",
            "settings": {
                "version": 2
            },
            "streamSettings": {
                "network": "hysteria",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.3",
                    "maxVersion": "1.3",
                    "alpn": ["h3"],
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ]
                },
                "hysteriaSettings": {
                    "version": 2,
                    "udpIdleTimeout": 60,
                    "masquerade": {
                        "type": "proxy",
                        "url": "__HYSTERIA_URL__",
                        "rewriteHost": true
                    }
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-hy2"
        },
        {
            "listen": "127.0.0.1",
            "port": "__wsPort__",
            "protocol": "vmess",
            "settings": {},
            "streamSettings": {
                "network": "ws",
                "wsSettings": {
                    "path": "/__wsPath__"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-ws"
        },
        {
            "listen": "127.0.0.1",
            "port": "__grpcPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "grpc",
                "grpcSettings": {
                    "serviceName": "__v2ServiceName__"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-grpc"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// vision-hy2.json — 对应 PHP resources/templates/xray/vision-hy2.json
const xrayTemplateVisionHy2 = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__visionPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none",
                "fallbacks": [
                    {
                        "dest": "127.0.0.1:8088"
                    }
                ]
            },
            "streamSettings": {
                "network": "tcp",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.2",
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ]
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-vision"
        },
        {
            "port": "__hy2Port__",
            "protocol": "hysteria",
            "settings": {
                "version": 2
            },
            "streamSettings": {
                "network": "hysteria",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.3",
                    "maxVersion": "1.3",
                    "alpn": ["h3"],
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ]
                },
                "hysteriaSettings": {
                    "version": 2,
                    "udpIdleTimeout": 60,
                    "masquerade": {
                        "type": "proxy",
                        "url": "__HYSTERIA_URL__",
                        "rewriteHost": true
                    }
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-hy2"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// vision-no-fallback.json — 对应 PHP resources/templates/xray/vision-no-fallback.json
const xrayTemplateVisionNoFallback = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__visionPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "tcp",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.2",
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ]
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-vision"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// vision-reality-min-firefox.json — 对应 PHP resources/templates/xray/vision-reality-min-firefox.json
const xrayTemplateVisionRealityMinFirefox = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__visionPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "tcp",
                "security": "reality",
                "realitySettings": {
                    "show": false,
                    "dest": "127.0.0.1:8443",
                    "xver": 0,
                    "serverNames": ["__realityServerName__"],
                    "privateKey": "__realityPrivateKey__",
                    "shortIds": ["__realityShortId__"],
                    "maxTimeDiff": 0,
                    "minClientVer": "26.3.27",
                    "fingerprint": "firefox"
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-vision"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// vision-reality-minClientVer.json — 对应 PHP resources/templates/xray/vision-reality-minClientVer.json
const xrayTemplateVisionRealityMinClientVer = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__visionPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "tcp",
                "security": "reality",
                "realitySettings": {
                    "show": false,
                    "dest": "127.0.0.1:8443",
                    "xver": 0,
                    "serverNames": ["__realityServerName__"],
                    "privateKey": "__realityPrivateKey__",
                    "shortIds": ["__realityShortId__"],
                    "maxTimeDiff": 0,
                    "minClientVer": "26.3.1"
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-vision"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// vision-reality-pq.json — 对应 PHP resources/templates/xray/vision-reality-pq.json
const xrayTemplateVisionRealityPq = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__visionPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "tcp",
                "security": "reality",
                "realitySettings": {
                    "show": false,
                    "dest": "127.0.0.1:8443",
                    "xver": 0,
                    "serverNames": ["__realityServerName__"],
                    "privateKey": "__realityPrivateKey__",
                    "shortIds": ["__realityShortId__"],
                    "maxTimeDiff": 0
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-vision"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// vision-reality.json — 对应 PHP resources/templates/xray/vision-reality.json
const xrayTemplateVisionReality = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__visionPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "tcp",
                "security": "reality",
                "realitySettings": {
                    "show": false,
                    "dest": "127.0.0.1:8443",
                    "xver": 0,
                    "serverNames": ["__realityServerName__"],
                    "privateKey": "__realityPrivateKey__",
                    "shortIds": ["__realityShortId__"],
                    "maxTimeDiff": 0
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-vision"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// vision-ws-grpc.json — 对应 PHP resources/templates/xray/vision-ws-grpc.json
const xrayTemplateVisionWsGrpc = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__visionPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none",
                "fallbacks": [
                    {
                        "dest": "127.0.0.1:8088"
                    }
                ]
            },
            "streamSettings": {
                "network": "tcp",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.2",
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ]
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-vision"
        },
        {
            "listen": "127.0.0.1",
            "port": "__wsPort__",
            "protocol": "vmess",
            "settings": {},
            "streamSettings": {
                "network": "ws",
                "wsSettings": {
                    "path": "/__wsPath__"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-ws"
        },
        {
            "listen": "127.0.0.1",
            "port": "__grpcPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "grpc",
                "grpcSettings": {
                    "serviceName": "__v2ServiceName__"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-grpc"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// vision.json — 对应 PHP resources/templates/xray/vision.json
const xrayTemplateVision = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__visionPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none",
                "fallbacks": [
                    {
                        "dest": "127.0.0.1:8088"
                    }
                ]
            },
            "streamSettings": {
                "network": "tcp",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.2",
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ]
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-vision"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xhttp-cdn-hy2.json — 对应 PHP resources/templates/xray/xhttp-cdn-hy2.json
const xrayTemplateXhttpCdnHy2 = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "listen": "127.0.0.1",
            "port": "__xhttpPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "xhttp",
                "xhttpSettings": {
                    "path": "/__xhttpPath__",
                    "mode": "auto",
                    "xPaddingObfsMode": true,
                    "xPaddingMethod": "tokenish",
                    "xPaddingPlacement": "queryInHeader",
                    "xPaddingKey": "__xPaddingKey__",
                    "xPaddingHeader": "__xPaddingHeader__",
                    "xPaddingBytes": "__xPaddingBytes__",
                    "scMaxEachPostBytes": "__scMaxEachPostBytes__",
                    "scMinPostsIntervalMs": "__scMinPostsIntervalMs__"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-xhttp"
        },
        {
            "port": "__hy2Port__",
            "protocol": "hysteria",
            "settings": {
                "version": 2
            },
            "streamSettings": {
                "network": "hysteria",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.3",
                    "maxVersion": "1.3",
                    "alpn": ["h3"],
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ]
                },
                "hysteriaSettings": {
                    "version": 2,
                    "udpIdleTimeout": 60,
                    "masquerade": {
                        "type": "proxy",
                        "url": "__HYSTERIA_URL__",
                        "rewriteHost": true
                    }
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-hy2"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {},
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xhttp-cdn.json — 对应 PHP resources/templates/xray/xhttp-cdn.json
const xrayTemplateXhttpCdn = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "listen": "127.0.0.1",
            "port": "__xhttpPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "xhttp",
                "xhttpSettings": {
                    "path": "/__xhttpPath__",
                    "mode": "auto",
                    "xPaddingObfsMode": true,
                    "xPaddingMethod": "tokenish",
                    "xPaddingPlacement": "queryInHeader",
                    "xPaddingKey": "__xPaddingKey__",
                    "xPaddingHeader": "__xPaddingHeader__",
                    "xPaddingBytes": "__xPaddingBytes__",
                    "scMaxEachPostBytes": "__scMaxEachPostBytes__",
                    "scMinPostsIntervalMs": "__scMinPostsIntervalMs__"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-xhttp"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {},
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xhttp-hy2-ws-grpc.json — 对应 PHP resources/templates/xray/xhttp-hy2-ws-grpc.json
const xrayTemplateXhttpHy2WsGrpc = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "listen": "127.0.0.1",
            "port": "__xhttpPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "xhttp",
                "xhttpSettings": {
                    "path": "/__xhttpPath__",
                    "mode": "auto"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-xhttp"
        },
        {
            "port": "__hy2Port__",
            "protocol": "hysteria",
            "settings": {
                "version": 2
            },
            "streamSettings": {
                "network": "hysteria",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.3",
                    "maxVersion": "1.3",
                    "alpn": ["h3"],
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ]
                },
                "hysteriaSettings": {
                    "version": 2,
                    "udpIdleTimeout": 60,
                    "masquerade": {
                        "type": "proxy",
                        "url": "__HYSTERIA_URL__",
                        "rewriteHost": true
                    }
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-hy2"
        },
        {
            "listen": "127.0.0.1",
            "port": "__wsPort__",
            "protocol": "vmess",
            "settings": {},
            "streamSettings": {
                "network": "ws",
                "wsSettings": {
                    "path": "/__wsPath__"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-ws"
        },
        {
            "listen": "127.0.0.1",
            "port": "__grpcPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "grpc",
                "grpcSettings": {
                    "serviceName": "__v2ServiceName__"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-grpc"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xhttp-hy2.json — 对应 PHP resources/templates/xray/xhttp-hy2.json
const xrayTemplateXhttpHy2 = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "listen": "127.0.0.1",
            "port": "__xhttpPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "xhttp",
                "xhttpSettings": {
                    "path": "/__xhttpPath__",
                    "mode": "auto"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-xhttp"
        },
        {
            "port": "__hy2Port__",
            "protocol": "hysteria",
            "settings": {
                "version": 2
            },
            "streamSettings": {
                "network": "hysteria",
                "security": "tls",
                "tlsSettings": {
                    "rejectUnknownSni": true,
                    "minVersion": "1.3",
                    "maxVersion": "1.3",
                    "alpn": ["h3"],
                    "certificates": [
                        {
                            "ocspStapling": 3600,
                            "certificateFile": "/etc/ssl/__nodeDomain__.pem",
                            "keyFile": "/etc/ssl/__nodeDomain__.key"
                        }
                    ]
                },
                "hysteriaSettings": {
                    "version": 2,
                    "udpIdleTimeout": 60,
                    "masquerade": {
                        "type": "proxy",
                        "url": "__HYSTERIA_URL__",
                        "rewriteHost": true
                    }
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-hy2"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {},
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xhttp-nginx-444.json — 对应 PHP resources/templates/xray/xhttp-nginx-444.json
const xrayTemplateXhttpNginx444 = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "listen": "127.0.0.1",
            "port": "__xhttpPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "xhttp",
                "xhttpSettings": {
                    "path": "/__xhttpPath__",
                    "mode": "auto"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-xhttp"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {},
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xhttp-pq.json — 对应 PHP resources/templates/xray/xhttp-pq.json
const xrayTemplateXhttpPq = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "listen": "127.0.0.1",
            "port": "__xhttpPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "xhttp",
                "xhttpSettings": {
                    "path": "/__xhttpPath__",
                    "mode": "auto"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-xhttp"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {},
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xhttp-reality-min-firefox.json — 对应 PHP resources/templates/xray/xhttp-reality-min-firefox.json
const xrayTemplateXhttpRealityMinFirefox = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__xhttpPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "xhttp",
                "xhttpSettings": {
                    "path": "/__xhttpPath__",
                    "mode": "auto"
                },
                "security": "reality",
                "realitySettings": {
                    "show": false,
                    "dest": "127.0.0.1:8443",
                    "xver": 0,
                    "serverNames": ["__realityServerName__"],
                    "privateKey": "__realityPrivateKey__",
                    "shortIds": ["__realityShortId__"],
                    "maxTimeDiff": 0,
                    "minClientVer": "26.3.27",
                    "fingerprint": "firefox"
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-xhttp"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {},
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xhttp-reality-minClientVer.json — 对应 PHP resources/templates/xray/xhttp-reality-minClientVer.json
const xrayTemplateXhttpRealityMinClientVer = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__xhttpPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "xhttp",
                "xhttpSettings": {
                    "path": "/__xhttpPath__",
                    "mode": "auto"
                },
                "security": "reality",
                "realitySettings": {
                    "show": false,
                    "dest": "127.0.0.1:8443",
                    "xver": 0,
                    "serverNames": ["__realityServerName__"],
                    "privateKey": "__realityPrivateKey__",
                    "shortIds": ["__realityShortId__"],
                    "maxTimeDiff": 0,
                    "minClientVer": "26.3.1"
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-xhttp"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {},
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xhttp-reality.json — 对应 PHP resources/templates/xray/xhttp-reality.json
const xrayTemplateXhttpReality = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "port": "__xhttpPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "xhttp",
                "xhttpSettings": {
                    "path": "/__xhttpPath__",
                    "mode": "auto"
                },
                "security": "reality",
                "realitySettings": {
                    "show": false,
                    "dest": "127.0.0.1:8443",
                    "xver": 0,
                    "serverNames": ["__realityServerName__"],
                    "privateKey": "__realityPrivateKey__",
                    "shortIds": ["__realityShortId__"],
                    "maxTimeDiff": 0
                }
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-xhttp"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {},
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xhttp-split.json — 对应 PHP resources/templates/xray/xhttp-split.json
const xrayTemplateXhttpSplit = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "listen": "127.0.0.1",
            "port": "__xhttpPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "xhttp",
                "xhttpSettings": {
                    "path": "/__xhttpPath__",
                    "mode": "auto"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-xhttp"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {},
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xhttp-verify.json — 对应 PHP resources/templates/xray/xhttp-verify.json
const xrayTemplateXhttpVerify = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "listen": "127.0.0.1",
            "port": "__xhttpPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "xhttp",
                "xhttpSettings": {
                    "path": "/__xhttpPath__",
                    "mode": "auto"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-xhttp"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {},
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xhttp-ws-grpc.json — 对应 PHP resources/templates/xray/xhttp-ws-grpc.json
const xrayTemplateXhttpWsGrpc = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "listen": "127.0.0.1",
            "port": "__xhttpPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "xhttp",
                "xhttpSettings": {
                    "path": "/__xhttpPath__",
                    "mode": "auto"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-xhttp"
        },
        {
            "listen": "127.0.0.1",
            "port": "__wsPort__",
            "protocol": "vmess",
            "settings": {},
            "streamSettings": {
                "network": "ws",
                "wsSettings": {
                    "path": "/__wsPath__"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-ws"
        },
        {
            "listen": "127.0.0.1",
            "port": "__grpcPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "grpc",
                "grpcSettings": {
                    "serviceName": "__v2ServiceName__"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-grpc"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xhttp.json — 对应 PHP resources/templates/xray/xhttp.json
const xrayTemplateXhttp = `{
    "log": {
        "loglevel": "error"
    },
    "inbounds": [
        {
            "listen": "127.0.0.1",
            "port": "__xhttpPort__",
            "protocol": "vless",
            "settings": {
                "decryption": "none"
            },
            "streamSettings": {
                "network": "xhttp",
                "xhttpSettings": {
                    "path": "/__xhttpPath__",
                    "mode": "auto"
                },
                "security": "none"
            },
            "sniffing": {
                "enabled": true,
                "destOverride": ["http", "tls"]
            },
            "tag": "proxy-xhttp"
        },
        {
            "listen": "127.0.0.1",
            "port": 10085,
            "protocol": "dokodemo-door",
            "settings": {
                "address": "127.0.0.1"
            },
            "tag": "api"
        }
    ],

    "outbounds": [
        {
            "protocol": "freedom",
            "settings": {
                "domainStrategy": "UseIPv4v6"
            }
        },

        {
            "protocol": "blackhole",
            "settings": {},
            "tag": "block"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "block",
                "domain": [
                    "geosite:falungong",
                    "geosite:category-bank-cn",
                    "geosite:ookla-speedtest",
                    "geosite:category-public-tracker",
                    "domain:cachefly.cachefly.net",
                    "domain:speed.cloudflare.com"
                ]
            },
            {
                "type": "field",
                "outboundTag": "block",
                "protocol": ["bittorrent"]
            },
            {
                "type": "field",
                "inboundTag": ["api"],
                "outboundTag": "api"
            },
            {
                "type": "field",
                "port": "25, 465, 587",
                "outboundTag": "block"
            }
        ],
        "strategy": "rules"
    },

    "policy": {
        "levels": {
            "0": {
                "statsUserUplink": true,
                "statsUserDownlink": true,
                "statsUserOnline": true
            }
        },
        "system": {
            "statsInboundUplink": true,
            "statsInboundDownlink": true
        }
    },

    "stats": {},

    "api": {
        "tag": "api",
        "services": ["HandlerService", "LoggerService", "StatsService"]
    },

    "ssrpanel": {
        "nodeId": 123,
        "checkRate": 600,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": ["__inboundTags__"],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {},
            "security": "none"
        },
        "mysql": {
            "host": "__dbHost__",
            "port": 3306,
            "user": "__dbUser__",
            "password": "__dbPassword__",
            "dbname": "__dbName__"
        }
    },
    "panelApi": {
        "api": {
            "baseURL": "__panelBaseURL__",
            "token": "__panelToken__",
            "timeoutSeconds": 10,
            "insecure": false
        },
        "pollIntervalSeconds": 60,
        "ipLimit": 3,
        "ignoreEmptyVmessID": true,
        "user": {
            "inboundTag": "proxy",
            "inboundTags": [
                "__inboundTags__"
            ],
            "level": 0,
            "alterId": 0,
            "flow": "",
            "flows": {
                "__vlessVisionTag__": "__v2Flow__"
            },
            "securityConfig": "auto"
        }
    }
}
`

// xrayTemplates 组名模板文件名 → 模板内容 (对齐 PHP resources/templates/xray/)
var xrayTemplates = map[string]string{
	"vision-curvePreferences-chrome.json": xrayTemplateVisionCurvePreferencesChrome,
	"vision-curvePreferences-hy2.json":    xrayTemplateVisionCurvePreferencesHy2,
	"vision-curvePreferences.json":        xrayTemplateVisionCurvePreferences,
	"vision-hy2-ws-grpc.json":             xrayTemplateVisionHy2WsGrpc,
	"vision-hy2.json":                     xrayTemplateVisionHy2,
	"vision-no-fallback.json":             xrayTemplateVisionNoFallback,
	"vision-reality-min-firefox.json":     xrayTemplateVisionRealityMinFirefox,
	"vision-reality-minClientVer.json":    xrayTemplateVisionRealityMinClientVer,
	"vision-reality-pq.json":              xrayTemplateVisionRealityPq,
	"vision-reality.json":                 xrayTemplateVisionReality,
	"vision-ws-grpc.json":                 xrayTemplateVisionWsGrpc,
	"vision.json":                         xrayTemplateVision,
	"xhttp-cdn-hy2.json":                  xrayTemplateXhttpCdnHy2,
	"xhttp-cdn.json":                      xrayTemplateXhttpCdn,
	"xhttp-hy2-ws-grpc.json":              xrayTemplateXhttpHy2WsGrpc,
	"xhttp-hy2.json":                      xrayTemplateXhttpHy2,
	"xhttp-nginx-444.json":                xrayTemplateXhttpNginx444,
	"xhttp-pq.json":                       xrayTemplateXhttpPq,
	"xhttp-reality-min-firefox.json":      xrayTemplateXhttpRealityMinFirefox,
	"xhttp-reality-minClientVer.json":     xrayTemplateXhttpRealityMinClientVer,
	"xhttp-reality.json":                  xrayTemplateXhttpReality,
	"xhttp-split.json":                    xrayTemplateXhttpSplit,
	"xhttp-verify.json":                   xrayTemplateXhttpVerify,
	"xhttp-ws-grpc.json":                  xrayTemplateXhttpWsGrpc,
	"xhttp.json":                          xrayTemplateXhttp,
}

// unlock/bahamut.json — 对应 PHP resources/templates/xray/unlock/bahamut.json
const xrayUnlockTemplateBahamut = `{
    "outbounds": [
        {
            "protocol": "shadowsocks",
            "settings": {
                "servers": [
                    {
                        "address": "__address__",
                        "port": 10010,
                        "method": "__method__",
                        "password": "__password__"
                    }
                ]
            },
            "tag": "ssbahamut"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "ssbahamut",
                "domain": ["geosite:bahamut"]
            }
        ]
    }
}
`

// unlock/claude.json — 对应 PHP resources/templates/xray/unlock/claude.json
const xrayUnlockTemplateClaude = `{
    "outbounds": [
        {
            "protocol": "shadowsocks",
            "settings": {
                "servers": [
                    {
                        "address": "__address__",
                        "port": 10010,
                        "method": "__method__",
                        "password": "__password__"
                    }
                ]
            },
            "tag": "ssclaude"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "ssclaude",
                "domain": ["geosite:anthropic"]
            }
        ]
    }
}
`

// unlock/disney.json — 对应 PHP resources/templates/xray/unlock/disney.json
const xrayUnlockTemplateDisney = `{
    "outbounds": [
        {
            "protocol": "shadowsocks",
            "settings": {
                "servers": [
                    {
                        "address": "__address__",
                        "port": 10010,
                        "method": "__method__",
                        "password": "__password__"
                    }
                ]
            },
            "tag": "ssdisney"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "ssdisney",
                "domain": ["geosite:disney"]
            }
        ]
    }
}
`

// unlock/google_scholar.json — 对应 PHP resources/templates/xray/unlock/google_scholar.json
const xrayUnlockTemplateGoogleScholar = `{
    "outbounds": [
        {
            "protocol": "shadowsocks",
            "settings": {
                "servers": [
                    {
                        "address": "__address__",
                        "port": 10010,
                        "method": "__method__",
                        "password": "__password__"
                    }
                ]
            },
            "tag": "ssgooglescholar"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "ssgooglescholar",
                "domain": ["geosite:google-scholar"]
            }
        ]
    }
}
`

// unlock/netflix.json — 对应 PHP resources/templates/xray/unlock/netflix.json
const xrayUnlockTemplateNetflix = `{
    "outbounds": [
        {
            "protocol": "shadowsocks",
            "settings": {
                "servers": [
                    {
                        "address": "__address__",
                        "port": 10010,
                        "method": "__method__",
                        "password": "__password__"
                    }
                ]
            },
            "tag": "ssnetflix"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "ssnetflix",
                "domain": ["geosite:netflix"]
            }
        ]
    }
}
`

// unlock/openai.json — 对应 PHP resources/templates/xray/unlock/openai.json
const xrayUnlockTemplateOpenai = `{
    "outbounds": [
        {
            "protocol": "shadowsocks",
            "settings": {
                "servers": [
                    {
                        "address": "__address__",
                        "port": 10010,
                        "method": "__method__",
                        "password": "__password__"
                    }
                ]
            },
            "tag": "ssopenai"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "ssopenai",
                "domain": ["geosite:openai"]
            }
        ]
    }
}
`

// unlock/tiktok.json — 对应 PHP resources/templates/xray/unlock/tiktok.json
const xrayUnlockTemplateTiktok = `{
    "outbounds": [
        {
            "protocol": "shadowsocks",
            "settings": {
                "servers": [
                    {
                        "address": "__address__",
                        "port": 10010,
                        "method": "__method__",
                        "password": "__password__"
                    }
                ]
            },
            "tag": "sstiktok"
        }
    ],

    "routing": {
        "rules": [
            {
                "type": "field",
                "outboundTag": "sstiktok",
                "domain": ["geosite:tiktok"]
            }
        ]
    }
}
`

// xrayUnlockTemplates 远程解锁服务名 → 子模板 (对齐 PHP resources/templates/xray/unlock/)
var xrayUnlockTemplates = map[string]string{
	"bahamut":        xrayUnlockTemplateBahamut,
	"claude":         xrayUnlockTemplateClaude,
	"disney":         xrayUnlockTemplateDisney,
	"google_scholar": xrayUnlockTemplateGoogleScholar,
	"netflix":        xrayUnlockTemplateNetflix,
	"openai":         xrayUnlockTemplateOpenai,
	"tiktok":         xrayUnlockTemplateTiktok,
}
