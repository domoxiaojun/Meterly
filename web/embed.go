// Package web 内嵌面板静态文件(Chart.js 本地内置,不走 CDN)。
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html app.js style.css vendor modules
var files embed.FS

func FS() fs.FS { return files }
