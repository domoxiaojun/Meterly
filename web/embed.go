// Package web 内嵌 Vite 生产产物，包含新版工作台和经典管理入口。
package web

import (
	"embed"
	"io/fs"
)

// 先运行 npm ci && npm run build；缺少产物时编译失败，避免发布旧面板。
//go:embed dist
var files embed.FS

func FS() fs.FS {
	root, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err)
	}
	return root
}
