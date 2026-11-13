// V2 协议组预设配置 —— 对齐 PHP app/Utils/V2Presets.php 的忠实移植。
//
// 每个 v2_name 是一个独立配置块, 所有子协议参数内联, 无跨组共享/合并/覆盖逻辑。
//
//	local_port   = xray 内部监听端口 (nginx proxy_pass 目标)
//	external_port = 对外端口 (客户端连接, server 字段)
//
// 与 PHP 的结构差异:
//   - PHP 关联数组 → Go 结构体 (v2GroupPreset / v2ProtoPreset);
//     协议组名顺序与协议键顺序均按 PHP 数组定义顺序保留 (slice 保序)。
//   - PHP 已停用的 ws/grpc 协议组 (xhttp-ws-grpc / vision-ws-grpc /
//     xhttp-hy2-ws-grpc / vision-hy2-ws-grpc) 与 V2Presets.php 中被注释掉的
//     GROUP_CONFIGS 条目一样, 不在本表内 (模板 .json/.conf 文件仍保留)。
package service

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"hash/crc32"
	"net/url"
	"strconv"
	"strings"
)

// v2ProtoPreset 单个子协议参数块 (对齐 PHP GROUP_CONFIGSprotocols 内条目)
type v2ProtoPreset struct {
	Name          string // 子协议短名: xhttp / hy2 / vision / ws / grpc
	Type          string // 'type'
	V2Net         string // 'v2_net'
	LocalPort     int    // 'local_port'
	ExternalPort  int    // 'external_port'
	V2TLS         string // 'v2_tls'
	V2Flow        string // 'v2_flow'
	V2FP          string // 'v2_fp'
	V2Alpn        string // 'v2_alpn'
	V2Path        string // 'v2_path'
	V2Host        string // 'v2_host' (仅历史 ws 组)
	V2Servicename string // 'v2_servicename'
	V2Mode        string // 'v2_mode' (仅历史 grpc 组)
	V2Scy         string // 'v2_scy'
}

// v2GroupPreset 协议组配置块 (对齐 PHP GROUP_CONFIGS 条目)
type v2GroupPreset struct {
	Name        string
	Template    string // xray 模板文件名
	NginxPort   int    // 'nginx_port'
	CDN         bool   // 'cdn' (xhttp-cdn 系列)
	XhttpVerify bool   // 'xhttp_verify'
	XhttpSplit  bool   // 'xhttp_split'
	Reality     bool   // 'reality'
	Protocols   []v2ProtoPreset
}

// proto 查找组内子协议配置 (对齐 $groupConfig['protocols'][$shortName])
func (g *v2GroupPreset) proto(shortName string) *v2ProtoPreset {
	for i := range g.Protocols {
		if g.Protocols[i].Name == shortName {
			return &g.Protocols[i]
		}
	}
	return nil
}

// v2GroupsPresets 全部协议组 (定义顺序 = PHP GROUP_CONFIGS 数组顺序)
var v2GroupsPresets = []v2GroupPreset{
	{
		Name: "xhttp", Template: "xhttp.json", NginxPort: 443,
		Protocols: []v2ProtoPreset{
			{Name: "xhttp", Type: "vless", V2Net: "xhttp", LocalPort: 10013, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "", V2FP: "firefox", V2Alpn: "", V2Path: "ssp-xhttp", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// xhttp-nginx-444: 与 xhttp 服务端 xray 配置完全一致, 唯一区别在 nginx
		// (location / 改为 return 444, 主动探测直接切断 TCP)。fp=firefox。
		Name: "xhttp-nginx-444", Template: "xhttp-nginx-444.json", NginxPort: 443,
		Protocols: []v2ProtoPreset{
			{Name: "xhttp", Type: "vless", V2Net: "xhttp", LocalPort: 10013, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "", V2FP: "firefox", V2Alpn: "", V2Path: "ssp-xhttp", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// xhttp-verify: register 时为每节点生成随机 UUID 作为 xhttp-verify token
		// 存入 server 串 (xhttp_verify=<uuid>), nginx 分流同时检测 path 与该 header。
		Name: "xhttp-verify", Template: "xhttp-verify.json", NginxPort: 443, XhttpVerify: true,
		Protocols: []v2ProtoPreset{
			{Name: "xhttp", Type: "vless", V2Net: "xhttp", LocalPort: 10013, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "", V2FP: "firefox", V2Alpn: "", V2Path: "ssp-xhttp", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// xhttp-cdn: Cloudflare CDN 模式 (灰云 host + 优选域名 add + 独立 token/域名池)。
		// fp 恒为空: CF 边缘拦截 uTLS 伪造指纹, 客户端空指纹走标准库 TLS, SNI 隐藏由 ECH 承担。
		Name: "xhttp-cdn", Template: "xhttp-cdn.json", NginxPort: 443, CDN: true,
		Protocols: []v2ProtoPreset{
			{Name: "xhttp", Type: "vless", V2Net: "xhttp", LocalPort: 10013, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "", V2FP: "", V2Alpn: "", V2Path: "ssp-xhttp", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// xhttp-cdn-chrome: xhttp-cdn 的 uTLS chrome 指纹 A/B 实验组, 服务端与 xhttp-cdn 一致。
		Name: "xhttp-cdn-chrome", Template: "xhttp-cdn.json", NginxPort: 443, CDN: true,
		Protocols: []v2ProtoPreset{
			{Name: "xhttp", Type: "vless", V2Net: "xhttp", LocalPort: 10013, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "", V2FP: "chrome", V2Alpn: "", V2Path: "ssp-xhttp", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// xhttp-cdn-hy2: CDN (TCP) + 直连 Hysteria2 (UDP) 混合模式。
		Name: "xhttp-cdn-hy2", Template: "xhttp-cdn-hy2.json", NginxPort: 443, CDN: true,
		Protocols: []v2ProtoPreset{
			{Name: "xhttp", Type: "vless", V2Net: "xhttp", LocalPort: 10013, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "", V2FP: "", V2Alpn: "", V2Path: "ssp-xhttp", V2Servicename: "", V2Scy: "none"},
			{Name: "hy2", Type: "hy2", V2Net: "hysteria2", LocalPort: 443, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "", V2FP: "", V2Alpn: "h3", V2Path: "", V2Servicename: "", V2Scy: ""},
		},
	},
	{
		Name: "vision", Template: "vision.json", NginxPort: 443,
		Protocols: []v2ProtoPreset{
			{Name: "vision", Type: "vless", V2Net: "tcp", LocalPort: 443, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "xtls-rprx-vision", V2FP: "firefox", V2Alpn: "", V2Path: "", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// vision-no-fallback: 与 vision 唯一区别是删掉 vless settings.fallbacks
		// (非 VLESS 流量不再回落 nginx 伪装站)。fp=firefox。
		Name: "vision-no-fallback", Template: "vision-no-fallback.json", NginxPort: 443,
		Protocols: []v2ProtoPreset{
			{Name: "vision", Type: "vless", V2Net: "tcp", LocalPort: 443, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "xtls-rprx-vision", V2FP: "firefox", V2Alpn: "", V2Path: "", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// vision-reality: REALITY 偷自己 (xray 443 → nginx 8443)。
		// per-node X25519 密钥存 server 串: reality_private/reality_pbk/reality_sid。
		Name: "vision-reality", Template: "vision-reality.json", NginxPort: 443, Reality: true,
		Protocols: []v2ProtoPreset{
			{Name: "vision", Type: "vless", V2Net: "tcp", LocalPort: 443, ExternalPort: 443,
				V2TLS: "reality", V2Flow: "xtls-rprx-vision", V2FP: "firefox", V2Alpn: "", V2Path: "", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// vision-reality-pq: vision-reality + nginx 8443 仅允许 X25519MLKEM768 后量子曲线。
		Name: "vision-reality-pq", Template: "vision-reality-pq.json", NginxPort: 443, Reality: true,
		Protocols: []v2ProtoPreset{
			{Name: "vision", Type: "vless", V2Net: "tcp", LocalPort: 443, ExternalPort: 443,
				V2TLS: "reality", V2Flow: "xtls-rprx-vision", V2FP: "firefox", V2Alpn: "", V2Path: "", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// xhttp-reality: xray 直接监听 443 (VLESS + xhttp + REALITY), nginx 8443 仅作 dest/伪装站。
		Name: "xhttp-reality", Template: "xhttp-reality.json", NginxPort: 443, Reality: true,
		Protocols: []v2ProtoPreset{
			{Name: "xhttp", Type: "vless", V2Net: "xhttp", LocalPort: 443, ExternalPort: 443,
				V2TLS: "reality", V2Flow: "", V2FP: "firefox", V2Alpn: "", V2Path: "ssp-xhttp", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// vision-reality-minClientVer: vision-reality + realitySettings.minClientVer=26.3.1 (模板硬编码)。
		Name: "vision-reality-minClientVer", Template: "vision-reality-minClientVer.json", NginxPort: 443, Reality: true,
		Protocols: []v2ProtoPreset{
			{Name: "vision", Type: "vless", V2Net: "tcp", LocalPort: 443, ExternalPort: 443,
				V2TLS: "reality", V2Flow: "xtls-rprx-vision", V2FP: "firefox", V2Alpn: "", V2Path: "", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// xhttp-reality-minClientVer: xhttp-reality + realitySettings.minClientVer=26.3.1 (模板硬编码)。
		Name: "xhttp-reality-minClientVer", Template: "xhttp-reality-minClientVer.json", NginxPort: 443, Reality: true,
		Protocols: []v2ProtoPreset{
			{Name: "xhttp", Type: "vless", V2Net: "xhttp", LocalPort: 443, ExternalPort: 443,
				V2TLS: "reality", V2Flow: "", V2FP: "firefox", V2Alpn: "", V2Path: "ssp-xhttp", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// vision-reality-min-firefox: minClientVer=26.3.27 + fingerprint=firefox (模板硬编码)。
		Name: "vision-reality-min-firefox", Template: "vision-reality-min-firefox.json", NginxPort: 443, Reality: true,
		Protocols: []v2ProtoPreset{
			{Name: "vision", Type: "vless", V2Net: "tcp", LocalPort: 443, ExternalPort: 443,
				V2TLS: "reality", V2Flow: "xtls-rprx-vision", V2FP: "firefox", V2Alpn: "", V2Path: "", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// xhttp-reality-min-firefox: minClientVer=26.3.27 + fingerprint=firefox (模板硬编码)。
		Name: "xhttp-reality-min-firefox", Template: "xhttp-reality-min-firefox.json", NginxPort: 443, Reality: true,
		Protocols: []v2ProtoPreset{
			{Name: "xhttp", Type: "vless", V2Net: "xhttp", LocalPort: 443, ExternalPort: 443,
				V2TLS: "reality", V2Flow: "", V2FP: "firefox", V2Alpn: "", V2Path: "ssp-xhttp", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// xhttp-split: VLESS + xhttp 上下行分离 (downloadSettings, 纯客户端概念, 服务端与 xhttp 一致)。
		Name: "xhttp-split", Template: "xhttp-split.json", NginxPort: 443, XhttpSplit: true,
		Protocols: []v2ProtoPreset{
			{Name: "xhttp", Type: "vless", V2Net: "xhttp", LocalPort: 10013, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "", V2FP: "firefox", V2Alpn: "", V2Path: "ssp-xhttp", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// xhttp-pq: 与 xhttp 的区别全在 nginx (443 仅允许 X25519MLKEM768 后量子曲线)。
		Name: "xhttp-pq", Template: "xhttp-pq.json", NginxPort: 443,
		Protocols: []v2ProtoPreset{
			{Name: "xhttp", Type: "vless", V2Net: "xhttp", LocalPort: 10013, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "", V2FP: "firefox", V2Alpn: "", V2Path: "ssp-xhttp", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// vision-curvePreferences: TLS 密钥交换限定 X25519MLKEM768 (写在 xray tlsSettings)。
		Name: "vision-curvePreferences", Template: "vision-curvePreferences.json", NginxPort: 443,
		Protocols: []v2ProtoPreset{
			{Name: "vision", Type: "vless", V2Net: "tcp", LocalPort: 443, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "xtls-rprx-vision", V2FP: "firefox", V2Alpn: "", V2Path: "", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// vision-curvePreferences-chrome: 服务端与 vision-curvePreferences 逐字一致, 客户端 uTLS 指纹 chrome。
		Name: "vision-curvePreferences-chrome", Template: "vision-curvePreferences-chrome.json", NginxPort: 443,
		Protocols: []v2ProtoPreset{
			{Name: "vision", Type: "vless", V2Net: "tcp", LocalPort: 443, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "xtls-rprx-vision", V2FP: "chrome", V2Alpn: "", V2Path: "", V2Servicename: "", V2Scy: "none"},
		},
	},
	{
		// vision-curvePreferences-hy2: vision-curvePreferences + Hysteria2 (UDP/QUIC)。
		Name: "vision-curvePreferences-hy2", Template: "vision-curvePreferences-hy2.json", NginxPort: 443,
		Protocols: []v2ProtoPreset{
			{Name: "vision", Type: "vless", V2Net: "tcp", LocalPort: 443, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "xtls-rprx-vision", V2FP: "firefox", V2Alpn: "", V2Path: "", V2Servicename: "", V2Scy: "none"},
			{Name: "hy2", Type: "hy2", V2Net: "hysteria2", LocalPort: 443, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "", V2FP: "", V2Alpn: "h3", V2Path: "", V2Servicename: "", V2Scy: ""},
		},
	},
	{
		Name: "xhttp-hy2", Template: "xhttp-hy2.json", NginxPort: 443,
		Protocols: []v2ProtoPreset{
			{Name: "xhttp", Type: "vless", V2Net: "xhttp", LocalPort: 10013, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "", V2FP: "firefox", V2Alpn: "", V2Path: "ssp-xhttp", V2Servicename: "", V2Scy: "none"},
			{Name: "hy2", Type: "hy2", V2Net: "hysteria2", LocalPort: 443, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "", V2FP: "", V2Alpn: "h3", V2Path: "", V2Servicename: "", V2Scy: ""},
		},
	},
	{
		Name: "vision-hy2", Template: "vision-hy2.json", NginxPort: 443,
		Protocols: []v2ProtoPreset{
			{Name: "vision", Type: "vless", V2Net: "tcp", LocalPort: 443, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "xtls-rprx-vision", V2FP: "firefox", V2Alpn: "", V2Path: "", V2Servicename: "", V2Scy: "none"},
			{Name: "hy2", Type: "hy2", V2Net: "hysteria2", LocalPort: 443, ExternalPort: 443,
				V2TLS: "tls", V2Flow: "", V2FP: "", V2Alpn: "h3", V2Path: "", V2Servicename: "", V2Scy: ""},
		},
	},
}

// v2ProtocolOrder 注册用有序协议序列 (对齐 PHP V2Presets::PROTOCOL_ORDER)。
// 首元素 = 主节点协议, 其余 = clone 节点顺序。
var v2ProtocolOrder = map[string][]string{
	"xhttp":                          {"xhttp", "xhttp", "xhttp"},
	"xhttp-nginx-444":                {"xhttp", "xhttp", "xhttp"},
	"xhttp-verify":                   {"xhttp", "xhttp", "xhttp"},
	"xhttp-cdn":                      {"xhttp", "xhttp", "xhttp"},
	"xhttp-cdn-chrome":               {"xhttp", "xhttp", "xhttp"},
	"xhttp-cdn-hy2":                  {"xhttp", "xhttp", "hy2"},
	"vision":                         {"vision", "vision", "vision"},
	"vision-no-fallback":             {"vision", "vision", "vision"},
	"xhttp-hy2":                      {"xhttp", "xhttp", "hy2"},
	"vision-hy2":                     {"vision", "vision", "hy2"},
	"vision-curvePreferences-hy2":    {"vision", "vision", "hy2"},
	"vision-reality":                 {"vision", "vision", "vision"},
	"vision-reality-pq":              {"vision", "vision", "vision"},
	"xhttp-reality":                  {"xhttp", "xhttp", "xhttp"},
	"vision-reality-minClientVer":    {"vision", "vision", "vision"},
	"xhttp-reality-minClientVer":     {"xhttp", "xhttp", "xhttp"},
	"vision-reality-min-firefox":     {"vision", "vision", "vision"},
	"xhttp-reality-min-firefox":      {"xhttp", "xhttp", "xhttp"},
	"xhttp-split":                    {"xhttp", "xhttp", "xhttp"},
	"xhttp-pq":                       {"xhttp", "xhttp", "xhttp"},
	"vision-curvePreferences":        {"vision", "vision", "vision"},
	"vision-curvePreferences-chrome": {"vision", "vision", "vision"},
}

// presetsGroup 取协议组配置 (对齐 PHP V2Presets::getGroupConfig); 未知组返回 nil。
func presetsGroup(v2Name string) *v2GroupPreset {
	for i := range v2GroupsPresets {
		if v2GroupsPresets[i].Name == v2Name {
			return &v2GroupsPresets[i]
		}
	}
	return nil
}

// PresetsGroupConfig 对齐 PHP V2Presets::getGroupConfig; 未知组返回 nil。
// 返回 PHP 关联数组等价形态: template / nginx_port / protocols(及可选
// cdn / xhttp_verify / xhttp_split / reality 标记键, 仅在该组定义时存在)。
func PresetsGroupConfig(v2Name string) map[string]any {
	g := presetsGroup(v2Name)
	if g == nil {
		return nil
	}
	protocols := make(map[string]any, len(g.Protocols))
	for _, p := range g.Protocols {
		pm := map[string]any{
			"type":           p.Type,
			"v2_net":         p.V2Net,
			"local_port":     p.LocalPort,
			"external_port":  p.ExternalPort,
			"v2_tls":         p.V2TLS,
			"v2_flow":        p.V2Flow,
			"v2_fp":          p.V2FP,
			"v2_alpn":        p.V2Alpn,
			"v2_path":        p.V2Path,
			"v2_servicename": p.V2Servicename,
			"v2_scy":         p.V2Scy,
		}
		// 历史字段 (v2_host/v2_mode) 仅在非空时出现, 对齐 PHP 数组键存在性
		if p.V2Host != "" {
			pm["v2_host"] = p.V2Host
		}
		if p.V2Mode != "" {
			pm["v2_mode"] = p.V2Mode
		}
		protocols[p.Name] = pm
	}
	m := map[string]any{
		"template":   g.Template,
		"nginx_port": g.NginxPort,
		"protocols":  protocols,
	}
	if g.CDN {
		m["cdn"] = true
	}
	if g.XhttpVerify {
		m["xhttp_verify"] = true
	}
	if g.XhttpSplit {
		m["xhttp_split"] = true
	}
	if g.Reality {
		m["reality"] = true
	}
	return m
}

// PresetsGroupNames 全部协议组名 (按 PHP GROUP_CONFIGS 定义顺序, 调试/校验用)。
func PresetsGroupNames() []string {
	names := make([]string, 0, len(v2GroupsPresets))
	for _, g := range v2GroupsPresets {
		names = append(names, g.Name)
	}
	return names
}

// presetsParamGet 取 server 串参数 (parse_str 结果的单值读取; 缺失返回 ”)。
// 注意: PHP parse_str 会把键里的点/空格转下划线, Go url.ParseQuery 不做 —
// 现有键名 (v2_name/host/node_port/...) 均不含点/空格, 语义等价。
func presetsParamGet(params url.Values, key string) string {
	if params == nil {
		return ""
	}
	return params.Get(key)
}

// presetsGetOrderedProtocols 对齐 PHP V2Presets::getOrderedProtocols。
// 主节点取 protocols[0], clone 依序取其余。
func presetsGetOrderedProtocols(v2Name string) []string {
	if order, ok := v2ProtocolOrder[v2Name]; ok {
		return order
	}
	// 回退: v2_name 按连字符拆分后每段恰好一一对应组内协议时直接采用
	parts := strings.Split(v2Name, "-")
	group := presetsGroup(v2Name)
	if group != nil && len(parts) == len(group.Protocols) {
		return parts
	}
	// 最终回退: 组内协议定义顺序
	if group != nil {
		out := make([]string, 0, len(group.Protocols))
		for _, p := range group.Protocols {
			out = append(out, p.Name)
		}
		return out
	}
	return nil
}

// presetsParseGroup 对齐 PHP V2Presets::parseGroup (按 '-' 拆分协议组名)。
func presetsParseGroup(groupName string) []string {
	if groupName == "" {
		return nil
	}
	return strings.Split(groupName, "-")
}

// presetsGetGroupProtocols 对齐 PHP V2Presets::getGroupProtocols (组内协议名, 保序)。
func presetsGetGroupProtocols(v2Name string) []string {
	group := presetsGroup(v2Name)
	if group == nil {
		return nil
	}
	out := make([]string, 0, len(group.Protocols))
	for _, p := range group.Protocols {
		out = append(out, p.Name)
	}
	return out
}

// presetsGetProtocolConfig 对齐 PHP V2Presets::getProtocolConfig。
func presetsGetProtocolConfig(v2Name, shortName string) *v2ProtoPreset {
	group := presetsGroup(v2Name)
	if group == nil {
		return nil
	}
	return group.proto(shortName)
}

// presetsGetListenPort 对齐 PHP V2Presets::getListenPort (内部监听端口, 未知返回 0)。
func presetsGetListenPort(v2Name, shortName string) int {
	proto := presetsGetProtocolConfig(v2Name, shortName)
	if proto == nil {
		return 0
	}
	return proto.LocalPort
}

// presetsGetExternalPort 对齐 PHP V2Presets::getExternalPort (对外端口, 未知返回 0)。
func presetsGetExternalPort(v2Name, shortName string) int {
	proto := presetsGetProtocolConfig(v2Name, shortName)
	if proto == nil {
		return 0
	}
	return proto.ExternalPort
}

// presetsBuildServerString 对齐 PHP V2Presets::buildServerString。
// 为组内某子协议构造 server 查询串 (port= 用对外端口; paths 命中时覆盖随机 path)。
func presetsBuildServerString(shortName string, hasVision bool, v2Name, address string, paths map[string]string) string {
	proto := presetsGetProtocolConfig(v2Name, shortName)
	if proto == nil {
		return ""
	}

	parts := []string{
		"v2=" + proto.Type,
		"net=" + proto.V2Net,
		"port=" + strconv.Itoa(proto.ExternalPort),
	}
	if proto.V2TLS != "" {
		parts = append(parts, "tls="+proto.V2TLS)
	}
	if proto.V2Flow != "" {
		parts = append(parts, "flow="+proto.V2Flow)
	}
	// HTTP 分流 path (xhttp / ws): 优先使用随机 path
	if shortName == "xhttp" || shortName == "ws" {
		pathVal := paths[shortName]
		if pathVal == "" {
			pathVal = proto.V2Path
		}
		if pathVal != "" {
			parts = append(parts, "path="+pathVal)
		}
	}
	host := proto.V2Host
	if host == "" {
		host = address
	}
	if host != "" {
		parts = append(parts, "host="+host)
	}
	if proto.V2TLS != "" && host != "" {
		sni := host // PHP: v2_sni 非空优先, 预设内未定义该键 → 恒回退 host
		parts = append(parts, "sni="+sni)
	}
	// gRPC serviceName: 优先使用随机 path
	if shortName == "grpc" {
		svcVal := paths["grpc"]
		if svcVal == "" {
			svcVal = proto.V2Servicename
		}
		if svcVal != "" {
			parts = append(parts, "serviceName="+svcVal)
		}
	} else if proto.V2Servicename != "" {
		parts = append(parts, "serviceName="+proto.V2Servicename)
	}
	if proto.V2Mode != "" {
		parts = append(parts, "mode="+proto.V2Mode)
	}
	if proto.V2Alpn != "" {
		parts = append(parts, "alpn="+proto.V2Alpn)
	}
	if proto.V2Scy != "" {
		parts = append(parts, "scy="+proto.V2Scy)
	}
	if proto.V2FP != "" {
		parts = append(parts, "fp="+proto.V2FP)
	}
	return strings.Join(parts, "&")
}

// presetsGenRandomPath 对齐 PHP Tools::genRandomPath: bin2hex(random_bytes(16)) → 32 位小写 hex。
func presetsGenRandomPath() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败极罕见 (系统熵池故障); 与 PHP random_bytes 一致地快速失败不可行,
		// 这里退化为全零填充保证不 panic (调用方 register 流程会因 path 重复被校验拦下)。
		for i := range b {
			b[i] = 0
		}
	}
	return hex.EncodeToString(b)
}

// presetsGenerateRandomPaths 对齐 PHP V2Presets::generateRandomPaths。
// 仅为使用分流 path / serviceName 的协议 (xhttp/ws/grpc) 生成随机 path。
func presetsGenerateRandomPaths(v2Name string) map[string]string {
	group := presetsGroup(v2Name)
	if group == nil {
		return map[string]string{}
	}
	paths := map[string]string{}
	for _, proto := range group.Protocols {
		switch proto.Name {
		case "xhttp", "ws":
			if proto.V2Path != "" {
				paths[proto.Name] = presetsGenRandomPath()
			}
		case "grpc":
			if proto.V2Servicename != "" {
				paths["grpc"] = presetsGenRandomPath()
			}
		}
	}
	return paths
}

// presetsResolvePaths 对齐 PHP V2Presets::resolvePaths。
// 从节点 server 字符串解析随机 path, 缺失时回退协议组默认值,
// 保证 nginx / xray 下发与订阅 path 一致。
func presetsResolvePaths(v2Name string, serverParams url.Values) map[string]string {
	group := presetsGroup(v2Name)
	if group == nil {
		return map[string]string{}
	}
	paths := map[string]string{}
	for _, proto := range group.Protocols {
		switch proto.Name {
		case "xhttp":
			val := presetsParamGet(serverParams, "xhttp_path")
			if val == "" {
				val = proto.V2Path
			}
			if val != "" {
				paths["xhttp"] = val
			}
		case "ws":
			val := presetsParamGet(serverParams, "ws_path")
			if val == "" {
				val = proto.V2Path
			}
			if val != "" {
				paths["ws"] = val
			}
		case "grpc":
			val := presetsParamGet(serverParams, "grpc_servicename")
			if val == "" {
				val = proto.V2Servicename
			}
			if val != "" {
				paths["grpc"] = val
			}
		}
	}
	return paths
}

// PresetsResolvePaths 对齐 PHP V2Presets::resolvePaths (导出版, handler 层依赖)。
func PresetsResolvePaths(v2Name string, serverParams url.Values) map[string]string {
	return presetsResolvePaths(v2Name, serverParams)
}

// presetsBuildPathsParam 对齐 PHP V2Presets::buildPathsParam。
// 随机 path 编码为 server 串片段 (主节点与 clone 共用同一组随机 path)。
func presetsBuildPathsParam(paths map[string]string) []string {
	var parts []string
	if paths["xhttp"] != "" {
		parts = append(parts, "xhttp_path="+paths["xhttp"])
	}
	if paths["ws"] != "" {
		parts = append(parts, "ws_path="+paths["ws"])
	}
	if paths["grpc"] != "" {
		parts = append(parts, "grpc_servicename="+paths["grpc"])
	}
	return parts
}

// presetsIsCdn 对齐 PHP V2Presets::isCdn (xhttp-cdn 系列)。
func presetsIsCdn(v2Name string) bool {
	group := presetsGroup(v2Name)
	return group != nil && group.CDN
}

// presetsHasXhttpVerify 对齐 PHP V2Presets::hasXhttpVerify。
func presetsHasXhttpVerify(v2Name string) bool {
	group := presetsGroup(v2Name)
	return group != nil && group.XhttpVerify
}

// presetsGenerateUUID4 生成随机 UUID v4 (对齐 PHP ramsey/uuid uuid4 标准输出格式)。
func presetsGenerateUUID4() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = 0
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// presetsGenerateXhttpVerify 对齐 PHP V2Presets::generateXhttpVerify (随机 UUID token)。
func presetsGenerateXhttpVerify() string {
	return presetsGenerateUUID4()
}

// PresetsResolveXhttpVerify 对齐 PHP V2Presets::resolveXhttpVerify (导出版)。
// 仅启用 xhttp_verify 的协议组返回非空 token, 未启用/未存储返回 ""。
func PresetsResolveXhttpVerify(v2Name string, serverParams url.Values) string {
	if !presetsHasXhttpVerify(v2Name) {
		return ""
	}
	return presetsParamGet(serverParams, "xhttp_verify")
}

// presetsBuildXhttpVerifyParam 对齐 PHP V2Presets::buildXhttpVerifyParam。
func presetsBuildXhttpVerifyParam(xhttpVerify string) []string {
	if xhttpVerify == "" {
		return nil
	}
	return []string{"xhttp_verify=" + xhttpVerify}
}

// presetsIsXhttpSplit 对齐 PHP V2Presets::isXhttpSplit (上下行分离模式)。
func presetsIsXhttpSplit(v2Name string) bool {
	group := presetsGroup(v2Name)
	return group != nil && group.XhttpSplit
}

// presetsBuildDownloadHost 对齐 PHP V2Presets::buildDownloadHost。
// 下行域名: {rand8}d{nodeId}.{rootDomain} ('d' 标记 vs 上行 's', 结构性保证两域不等)。
func presetsBuildDownloadHost(nodeID int64, rootDomain string) string {
	dlPrefix := presetsGenerateUUID4()[:8]
	return dlPrefix + "d" + strconv.FormatInt(nodeID, 10) + "." + rootDomain
}

// presetsResolveXhttpSplit 对齐 PHP V2Presets::resolveXhttpSplit。
// 仅 xhttp-split 组返回非空值; 非 split 组返回 nil。
func presetsResolveXhttpSplit(v2Name string, serverParams url.Values) map[string]string {
	if !presetsIsXhttpSplit(v2Name) {
		return nil
	}
	return map[string]string{
		"host": presetsParamGet(serverParams, "xhttp_dl_host"),
		"add":  presetsParamGet(serverParams, "xhttp_dl_add"),
	}
}

// presetsBuildXhttpSplitParam 对齐 PHP V2Presets::buildXhttpSplitParam。
func presetsBuildXhttpSplitParam(dlHost, dlAdd string) []string {
	var parts []string
	if dlHost != "" {
		parts = append(parts, "xhttp_dl_host="+dlHost)
	}
	if dlAdd != "" {
		parts = append(parts, "xhttp_dl_add="+dlAdd)
	}
	return parts
}

// presetsIsReality 对齐 PHP V2Presets::isReality。
func presetsIsReality(v2Name string) bool {
	group := presetsGroup(v2Name)
	return group != nil && group.Reality
}

// presetsGenerateRealityKeys 对齐 PHP V2Presets::generateRealityKeys。
// 标准库 X25519 (crypto/ecdh) 与 PHP libsodium / `xray x25519` 输出完全兼容:
// privateKey 32 字节标量、publicKey 32 字节曲线点, 均 base64url 无填充 (43 字符);
// shortId 为 8 位 hex (4 字节)。
func presetsGenerateRealityKeys() (private, public, shortID string) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", ""
	}
	private = presetsBase64URLEncode(priv.Bytes())
	public = presetsBase64URLEncode(priv.PublicKey().Bytes())
	sid := make([]byte, 4)
	if _, err := rand.Read(sid); err != nil {
		for i := range sid {
			sid[i] = 0
		}
	}
	shortID = hex.EncodeToString(sid)
	return private, public, shortID
}

// presetsResolveReality 对齐 PHP V2Presets::resolveReality。
// 仅 REALITY 协议组返回非 nil (键缺失时值为 ""); 非 REALITY 组返回 nil。
func presetsResolveReality(v2Name string, serverParams url.Values) map[string]string {
	if !presetsIsReality(v2Name) {
		return nil
	}
	return map[string]string{
		"private": presetsParamGet(serverParams, "reality_private"),
		"public":  presetsParamGet(serverParams, "reality_pbk"),
		"shortId": presetsParamGet(serverParams, "reality_sid"),
	}
}

// presetsBuildRealityParam 对齐 PHP V2Presets::buildRealityParam。
func presetsBuildRealityParam(private, public, shortID string) []string {
	if private == "" {
		return nil
	}
	return []string{
		"reality_private=" + private,
		"reality_pbk=" + public,
		"reality_sid=" + shortID,
	}
}

// presetsBase64URLEncode 对齐 PHP V2Presets::base64UrlEncode (base64url 无填充,
// xray reality 密钥标准编码)。
func presetsBase64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// ─────────────────────────────────────────────────────────────────────────────
// ECH (Encrypted Client Hello) — 对齐 PHP V2Presets::buildEchValue /
// DomainPool::echResolvers / pickEchResolver
// ─────────────────────────────────────────────────────────────────────────────

// ECH 查询域名 (对齐 PHP DomainPool::ECH_QUERY_DOMAIN; CF 全网 ECH 同一对密钥,
// 故统一用 crypto.cloudflare.com)。PHP 支持每域名 ech 覆盖键, Go 域名池配置结构
// (config.DomainPoolConf) 未含 ech 字段 → 恒用默认查询域名。
const presetsECHQueryDomain = "crypto.cloudflare.com"

// ECH 解析器候选池 (对齐 PHP DomainPool::ECH_DNS_RESOLVERS 默认值;
// PHP 的 config['ech_dns_resolvers'] 覆盖键 Go 配置未含 → 硬编码默认池)。
var presetsECHDNSResolvers = []string{
	"https://223.5.5.5/dns-query",
	"https://120.53.53.53/dns-query",
	"udp://223.5.5.5",
	"udp://119.29.29.29",
	"https://1.1.1.1/dns-query",
}

// presetsPickEchResolver 对齐 PHP DomainPool::pickEchResolver (按种子取模均匀分配, 幂等)。
func presetsPickEchResolver(seed int64) string {
	return presetsECHDNSResolvers[presetsAbsMod(seed, int64(len(presetsECHDNSResolvers)))]
}

// presetsBuildEchValue 对齐 PHP V2Presets::buildEchValue (DomainPool 转发)。
// nodeId 缺省 (nil) 时按 crc32(根域名) 兜底播种。
func presetsBuildEchValue(domain string, nodeID *int64) string {
	if domain == "" {
		return ""
	}
	seed := int64(crc32.ChecksumIEEE([]byte(domain)))
	if nodeID != nil {
		seed = *nodeID
	}
	return presetsECHQueryDomain + "+" + presetsPickEchResolver(seed)
}

// presetsBuildEchParam 对齐 PHP V2Presets::buildEchParam。
func presetsBuildEchParam(ech string) []string {
	if ech == "" {
		return nil
	}
	return []string{"ech=" + ech}
}

// presetsResolveEch 对齐 PHP V2Presets::resolveEch。
// ECH 值里的 '+' 被 parse_str / ParseQuery 解码成空格, 这里还原。
func presetsResolveEch(serverParams url.Values) string {
	ech := presetsParamGet(serverParams, "ech")
	if ech == "" {
		return ""
	}
	r := strings.NewReplacer(
		" udp://", "+udp://",
		" https://", "+https://",
		" h2c://", "+h2c://",
	)
	return r.Replace(ech)
}

// ─────────────────────────────────────────────────────────────────────────────
// XHTTP padding 混淆 (xhttp-cdn 系列) — 对齐 PHP V2Presets 同名常量/方法
// ─────────────────────────────────────────────────────────────────────────────

// 数值范围单一事实源 (服务端 xhttpSettings 与订阅 extra/xhttp-opts 共用, 双端一致):
//   - XPADDING_BYTES         官方默认区间, 纯噪声长度熵
//   - SC_MAX_EACH_POST_BYTES #5414 官方示例区间 (To=1000000 在 nginx 413 上限之内)
//   - SC_MIN_POSTS_INTERVAL_MS 默认 30ms 随机化到均值 55ms, 突发上行请求率约降 45%
const (
	presetsXHTTPXPaddingBytes        = "100-1000"
	presetsXHTTPSCMaxEachPostBytes   = "500000-1000000"
	presetsXHTTPSCMinPostsIntervalMs = "30-80"
)

// padding 混淆键名候选池 (对齐 PHP DomainPool::XOBFS_KEY_POOL — 真实 CDN 生态
// 常见 cache-buster 词, 避免 x_padding 默认特征)。
var presetsXobfsKeyPool = []string{"_dc", "cb", "v", "ts", "_", "bust"}

// padding 承载 header 候选池 (对齐 PHP DomainPool::XOBFS_HEADER_POOL)。
var presetsXobfsHeaderPool = []string{"X-Cache", "X-Forwarded-Uri", "X-Cache-Key"}

// presetsPickXobfsKey 对齐 PHP DomainPool::pickXobfsKey (按节点 ID 取模, 幂等)。
func presetsPickXobfsKey(seed int64) string {
	return presetsXobfsKeyPool[presetsAbsMod(seed, int64(len(presetsXobfsKeyPool)))]
}

// presetsPickXobfsHeader 对齐 PHP DomainPool::pickXobfsHeader (池大小互素, 组合随 ID 轮转)。
func presetsPickXobfsHeader(seed int64) string {
	return presetsXobfsHeaderPool[presetsAbsMod(seed, int64(len(presetsXobfsHeaderPool)))]
}

// presetsBuildXobfsParam 对齐 PHP V2Presets::buildXobfsParam。
func presetsBuildXobfsParam(pair map[string]string) []string {
	if pair == nil || pair["key"] == "" || pair["header"] == "" {
		return nil
	}
	return []string{"xobfs_key=" + pair["key"], "xobfs_hdr=" + pair["header"]}
}

// presetsResolveXobfs 对齐 PHP V2Presets::resolveXobfs。
// 从 server 串解析 padding 混淆键; 缺失时按节点 ID 种子现算 (存量 CDN 节点
// 渲染期自愈, 与注册期同种子幂等 — 键名必须双端稳定)。
// 仅 CDN 组返回非 nil; hy2 clone (无 cdn=1) 不受影响。
func presetsResolveXobfs(v2Name string, serverParams url.Values, nodeID int64) map[string]string {
	if !presetsIsCdn(v2Name) {
		return nil
	}
	key := presetsParamGet(serverParams, "xobfs_key")
	header := presetsParamGet(serverParams, "xobfs_hdr")
	if key == "" || header == "" {
		key = presetsPickXobfsKey(nodeID)
		header = presetsPickXobfsHeader(nodeID)
	}
	return map[string]string{"key": key, "header": header}
}

// presetsGetGroupTemplate 对齐 PHP V2Presets::getGroupTemplate (xray 模板文件名)。
func presetsGetGroupTemplate(v2Name string) string {
	group := presetsGroup(v2Name)
	if group == nil {
		return ""
	}
	return group.Template
}

// presetsGetSortForType 对齐 PHP V2Presets::getSortForType。
func presetsGetSortForType(typ string) int {
	switch typ {
	case "hy2":
		return 15
	case "vless":
		return 13
	case "vmess":
		return 11
	}
	return 0
}

// presetsAbsMod 对齐 PHP abs((int)$seed) % count($pool)。
func presetsAbsMod(seed, n int64) int64 {
	if seed < 0 {
		seed = -seed
	}
	return seed % n
}

// 未移植 (与 PHP 的差异说明):
//   - V2Presets::getCdnPreferredIps / pickCdnPreferredIp — PHP 中仅转发
//     App\Services\Dns\CdnIpPool (优选 IP CSV 池), 供 Job::RotateCdnIps /
//     注册期使用; Go 侧 CdnIpPool 模块尚未移植, 且 generateXrayConfig 不依赖,
//     待 CdnIpPool 移植时一并补上。
//   - 已停用的 ws/grpc 协议组 (GROUP_CONFIGS 中被注释的 4 组) 不入表。
