// Package version 保存构建时注入的版本信息(Makefile 通过 -ldflags -X 写入)。
package version

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func String() string { return Version + " (" + Commit + ", " + Date + ")" }
