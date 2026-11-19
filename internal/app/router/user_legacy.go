package router

// 用户端静态托管 + legacy 兼容层 (方案 §5.1)
//
//	gin 限制: r.Static 的 /*filepath 通配符无法与 /auth/login 等精确路由共存,
//	故 /auth 与 /user 的静态页面也统一走 NoRoute 文件托管:
//	  ① 命中 public/<path> 文件 → 直接服务 (.html 协商缓存 no-cache)
//	  ② /auth 旧路径精确表 → 302/301 重定向 (外链契约: 邀请链接/重置邮件/书签)
//	  ③ /user 6 条副作用 GET → 直接服务 bridge.html (D5)
//	  ④ /user 通用兜底 /user/<name> → /user/<name>.html (文件存在时)
//	  ⑤ API 前缀 → JSON 404; 其余 → 404 页面
//	/assets 与 /admin 无 legacy 需求, 保留 r.Static 高效挂载。
//
//	灰度口径: legacyRedirectPermanent=false → 302 不缓存 (deploy 可回滚); 稳定后切 301。

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// legacyRedirectPermanent false=灰度期 302; true=稳定期 301 (旧链接自动进化)
var legacyRedirectPermanent = false

// authLegacyMap /auth 旧路径精确重定向表 (query 自动透传)
var authLegacyMap = map[string]string{
	"/auth/login":          "/auth/login.html",
	"/auth/register":       "/auth/register.html", // ?code=/?ref= 由页面双兼容
	"/auth/logout":         "/auth/logout.html",
	"/auth/password/reset": "/auth/reset.html",
	"/auth/token":          "/auth/token.html",
}

// userLegacyBridge /user 副作用 GET → bridge 页 (直接服务, 无跳转垫跃)
var userLegacyBridge = map[string]bool{
	"/user/logout":          true,
	"/user/gareset":         true,
	"/user/telegram_reset":  true,
	"/user/url_reset":       true,
	"/user/inviteurl_reset": true,
	"/user/backtoadmin":     true,
}

// RegisterUserStatic 挂载用户端静态资源 (auth/user 走 NoRoute 托管, 见 registerCommonPages)
func RegisterUserStatic(r *gin.Engine) {
	mountStatic(r, "assets")
	registerCommonPages(r)
}

// RegisterAdminStatic 挂载管理端前端静态页面 (public/admin)
func RegisterAdminStatic(r *gin.Engine) {
	mountStatic(r, "admin")
}

// mountStatic 将 public/<name> 目录挂载至 /<name>/* 路径 (仅用于无 legacy 需求的目录)
// (staticFS: 磁盘 public/ 优先, 不存在时回退内嵌 —— 见 staticfs.go)
func mountStatic(r *gin.Engine, names ...string) {
	for _, n := range names {
		r.StaticFS("/"+n, ginSubFS(n))
	}
}

// registerCommonPages 根页面 + NoRoute 统一入口 (auth/user 静态 + legacy + 404)
func registerCommonPages(r *gin.Engine) {
	staticPage := func(route, rel string) {
		r.GET(route, func(c *gin.Context) {
			if !serveStaticFSFile(c, rel) {
				c.Status(http.StatusNotFound)
			}
		})
	}
	staticPage("/", "index.html")
	staticPage("/index.html", "index.html")
	staticPage("/404.html", "404.html")
	// 用户中心首页 (方案 B: dashboard.html 重命名为 index.html, /user/ 直出免跳转)
	staticPage("/user/", "user/index.html")

	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path

		// API/节点/订阅前缀 → JSON 404
		if strings.HasPrefix(p, "/api") || strings.HasPrefix(p, "/mu") ||
			strings.HasPrefix(p, "/link") || strings.HasPrefix(p, "/rss") {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "msg": "not found"})
			return
		}

		// ① 静态文件命中 (仅限 public/ 下白名单目录, 防路径逃逸)
		if tryServeStatic(c, p) {
			return
		}

		// ② /auth 旧路径精确表
		if to, ok := authLegacyMap[p]; ok {
			legacyRedirectTo(c, to)
			return
		}
		// 邮件链接: /auth/password/token/{token}
		if strings.HasPrefix(p, "/auth/password/token/") {
			token := strings.TrimPrefix(p, "/auth/password/token/")
			if token != "" && !strings.Contains(token, "/") {
				legacyRedirectTo(c, "/auth/token.html?token="+token)
				return
			}
		}

		// /user 副作用 GET → bridge
		if userLegacyBridge[p] {
			bridgePage(c)
			return
		}

		// 旧用户中心首页别名: dashboard(.html) → /user/ (重命名后兼容旧书签/旧链接)
		if p == "/user/dashboard" || p == "/user/dashboard.html" {
			legacyRedirectTo(c, "/user/")
			return
		}

		// ④ 动态段别名 (邮件保险)
		if seg := matchSegments(p, "/user/ticket/", "/view"); len(seg) == 1 && seg[0] != "" {
			legacyRedirectTo(c, "/user/ticket-view.html?id="+seg[0])
			return
		}
		if seg := matchSegments(p, "/user/relay/", "/edit"); len(seg) == 1 && seg[0] != "" {
			legacyRedirectTo(c, "/user/relay-edit.html?id="+seg[0])
			return
		}

		// ⑤ /user 通用兜底: /user/<name> → /user/<name>.html (同名文件存在时)
		if strings.HasPrefix(p, "/user/") && !strings.Contains(strings.TrimPrefix(p, "/user/"), "/") {
			name := strings.TrimPrefix(p, "/user/")
			if name != "" && !strings.Contains(name, ".") &&
				fileExists(filepath.Join("public", "user", name+".html")) {
				legacyRedirectTo(c, "/user/"+name+".html"+querySuffix(c))
				return
			}
		}
		// /user → /user/ (带斜杠路径, 保证 auth-guard 的 '/user/' 前缀拦截生效)
		if p == "/user" {
			legacyRedirectTo(c, "/user/")
			return
		}

		// 兜底 404 页面
		serveNotFound(c)
	})
}

// tryServeStatic 尝试按白名单目录服务 public/ 下文件
func tryServeStatic(c *gin.Context, p string) bool {
	if p == "/" {
		return false // 根已由 StaticFile 注册, NoRoute 不会收到
	}
	clean := path.Clean(p) // e.g. /user/dashboard.html
	if clean == "/" || strings.Contains(clean, "..") {
		return false
	}
	rel := strings.TrimPrefix(clean, "/")
	first := rel
	if idx := strings.Index(rel, "/"); idx >= 0 {
		first = rel[:idx]
	}
	// 白名单目录: user / auth / assets (admin 由 r.Static 挂载, 不会进 NoRoute)
	if first != "user" && first != "auth" && first != "assets" && first != "images" && first != "tutorial" && first != "docs" && first != "download" && first != "jscdn" && first != "theme" && first != "favicon.ico" && first != "robots.txt" && first != "china.html" && first != "paolu.html" {
		return false
	}

	full := filepath.Join("public", filepath.FromSlash(rel))
	if first == "download" {
		// download 不入内嵌 (1.2GB 安装包), 始终磁盘服务
		if !fileExists(full) {
			return false
		}
		c.Header("Cache-Control", "max-age=86400")
		c.File(full)
		return true
	}
	if _, err := staticStat(rel); err != nil {
		return false
	}
	return serveStaticFSFile(c, rel)
}

// matchSegments 匹配 /prefix/<seg>/<suffix> 形态, 返回中间段
func matchSegments(p, prefix, suffix string) []string {
	if !strings.HasPrefix(p, prefix) || !strings.HasSuffix(p, suffix) {
		return nil
	}
	mid := p[len(prefix) : len(p)-len(suffix)]
	return strings.Split(mid, "/")
}

// legacyRedirectTo 302/301 重定向 (透传 query)
func legacyRedirectTo(c *gin.Context, to string) {
	if q := c.Request.URL.RawQuery; q != "" && !strings.Contains(to, "?") {
		to += "?" + q
	}
	if legacyRedirectPermanent {
		c.Redirect(http.StatusMovedPermanently, to)
	} else {
		c.Redirect(http.StatusFound, to)
	}
}

func querySuffix(c *gin.Context) string {
	if q := c.Request.URL.RawQuery; q != "" {
		return "?" + q
	}
	return ""
}

// bridgePage 服务通用副作用 bridge 页
func bridgePage(c *gin.Context) {
	c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
	if !serveStaticFSFile(c, "user/bridge.html") {
		c.Status(http.StatusNotFound)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func serveNotFound(c *gin.Context) {
	c.Status(http.StatusNotFound)
	c.Header("Content-Type", "text/html; charset=utf-8")
	body, err := staticReadFile("404.html")
	if err != nil {
		_, _ = c.Writer.WriteString("<h1>404 Not Found</h1>")
		return
	}
	_, _ = c.Writer.Write(body)
}
