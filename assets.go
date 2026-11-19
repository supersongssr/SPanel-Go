// Package spanel 仓库根内嵌资源声明 (方案 .plans/2026-11-18 admin 移植 §10:
// public 静态前端 embed 进二进制, 支持单文件部署; public/download 体积过大不入内嵌,
// 始终从磁盘服务)。
package spanel

import "embed"

// Public 内嵌静态资源 (运行时优先读磁盘 public/, 磁盘不存在时回退到内嵌;
// 见 internal/app/router/staticfs.go)。
//
//go:embed public/admin public/assets public/auth public/images public/tutorial public/user public/404.html public/index.html
var Public embed.FS
