// nginx 协议组模板 (原样内嵌, 对齐 PHP resources/templates/nginx/*.conf)。
//
// 对齐 PHP V2ApiController (nginx conf 下发): 模板路径
// resources/templates/nginx/{v2_name}.conf —— 按 v2_name (协议组名) 定位,
// 与 V2Presets::getGroupTemplate 的 template 字段对应的 .json 同名。
package service

import "embed"

// nginxTemplatesFS 内嵌 26 个 nginx 协议组模板
//
//go:embed templates/nginx/*.conf
var nginxTemplatesFS embed.FS

// NginxTemplate 按协议组名 (v2_name) 取 nginx 模板原文。
// 对齐 PHP: BASE_PATH . '/resources/templates/nginx/' . $v2Name . '.conf'。
// 模板不存在返回 ("", false) (对应 PHP file_exists 检查失败 → 404)。
func NginxTemplate(v2Name string) (string, bool) {
	if v2Name == "" {
		return "", false
	}
	// embed.FS 对含 ".." 的路径直接报错, 无目录穿越风险
	b, err := nginxTemplatesFS.ReadFile("templates/nginx/" + v2Name + ".conf")
	if err != nil {
		return "", false
	}
	return string(b), true
}
