package router

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	spanel "spanel-go"

	"github.com/gin-gonic/gin"
)

/* 静态资源根: 优先磁盘 public/ (开发/测试模式, 可热改页面), 磁盘不存在时回退内嵌
 * (单文件部署; download 目录不入内嵌, 始终磁盘服务, 缺失时 404)。 */

var staticFS fs.FS

func init() {
	if info, err := os.Stat("public"); err == nil && info.IsDir() {
		staticFS = os.DirFS("public") // 开发模式: 磁盘优先
		return
	}
	sub, err := fs.Sub(spanel.Public, "public")
	if err != nil {
		staticFS = os.DirFS("public") // 兜底: 维持旧行为
		return
	}
	staticFS = sub // 单文件部署: 内嵌
}

// staticStat 静态资源存在性 (fs 路径, 不含 public/ 前缀)
func staticStat(rel string) (fs.FileInfo, error) {
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return nil, fs.ErrNotExist
	}
	return fs.Stat(staticFS, rel)
}

// staticReadFile 读取静态资源字节
func staticReadFile(rel string) ([]byte, error) {
	rel = strings.TrimPrefix(rel, "/")
	return fs.ReadFile(staticFS, rel)
}

// serveStaticFSFile 从 staticFS 输出文件 (html 不缓存, 其余缓存一天); 命中返回 true
func serveStaticFSFile(c *gin.Context, rel string) bool {
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	if rel == "" {
		return false
	}
	if strings.Contains(rel, "..") {
		return false
	}
	data, err := staticReadFile(rel)
	if err != nil {
		return false
	}
	if strings.HasSuffix(rel, ".html") {
		c.Header("Cache-Control", "no-cache")
	} else {
		c.Header("Cache-Control", "max-age=86400")
	}
	mimeType := "application/octet-stream"
	switch {
	case strings.HasSuffix(rel, ".html"):
		mimeType = "text/html; charset=utf-8"
	case strings.HasSuffix(rel, ".css"):
		mimeType = "text/css; charset=utf-8"
	case strings.HasSuffix(rel, ".js"):
		mimeType = "application/javascript; charset=utf-8"
	case strings.HasSuffix(rel, ".json"):
		mimeType = "application/json; charset=utf-8"
	case strings.HasSuffix(rel, ".svg"):
		mimeType = "image/svg+xml"
	case strings.HasSuffix(rel, ".woff2"), strings.HasSuffix(rel, ".woff"), strings.HasSuffix(rel, ".ttf"):
		mimeType = "font/woff2"
	case strings.HasSuffix(rel, ".png"):
		mimeType = "image/png"
	case strings.HasSuffix(rel, ".jpg"), strings.HasSuffix(rel, ".jpeg"):
		mimeType = "image/jpeg"
	case strings.HasSuffix(rel, ".webp"):
		mimeType = "image/webp"
	case strings.HasSuffix(rel, ".ico"):
		mimeType = "image/x-icon"
	case strings.HasSuffix(rel, ".md"):
		mimeType = "text/markdown; charset=utf-8"
	}
	c.Data(http.StatusOK, mimeType, data)
	return true
}

// ginSubFS 某子目录的 http.FileSystem (供 gin StaticFS 挂载; gin 传入子路径, FS 根即该子目录)
func ginSubFS(name string) http.FileSystem {
	sub, err := fs.Sub(staticFS, name)
	if err != nil {
		return http.FS(staticFS)
	}
	return http.FS(sub)
}
