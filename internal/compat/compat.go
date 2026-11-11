// Package compat 提供 Meterly 对旧版 trafficstat 路径与环境变量的回退。
package compat

import (
	"os"
	"strings"
)

const (
	AgentConfigNew = "/etc/meterly/agent.json"
	AgentConfigOld = "/etc/trafficstat/agent.json"
	AgentTokenNew  = "/etc/meterly/agent.token"
	AgentTokenOld  = "/etc/trafficstat/agent.token"
	AgentStateNew  = "/var/lib/meterly"
	AgentStateOld  = "/var/lib/trafficstat"

	ServerConfigNew = "/etc/meterly/server.json"
	ServerConfigOld = "/etc/trafficstat/server.json"
	ServerDBNew     = "/var/lib/meterly-server/meterly.db"
	ServerDBLegacy  = "/var/lib/meterly-server/trafficstat.db"
	ServerDBOld     = "/var/lib/trafficstat-server/trafficstat.db"
)

// FirstEnv 按顺序返回第一个非空环境变量(已 TrimSpace)。
func FirstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// FirstExisting 返回第一个已存在的路径;都不存在时返回 fallback。
func FirstExisting(fallback string, paths ...string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return fallback
}

// AgentConfigPath 缺省配置路径:新路径优先,否则回退旧版 trafficstat。
func AgentConfigPath() string {
	return FirstExisting(AgentConfigNew, AgentConfigNew, AgentConfigOld)
}

// AgentTokenPath 缺省 token 路径。
func AgentTokenPath() string {
	return FirstExisting(AgentTokenNew, AgentTokenNew, AgentTokenOld)
}

// AgentStateDir 缺省状态目录。
func AgentStateDir() string {
	return FirstExisting(AgentStateNew, AgentStateNew, AgentStateOld)
}

// ServerConfigPath 缺省中心配置路径。都不存在时返回空,调用方使用内置默认值。
func ServerConfigPath() string {
	return FirstExisting("", ServerConfigNew, ServerConfigOld)
}

// ServerDBPath 缺省数据库路径。文件名 trafficstat.db 在旧部署中保留,见 docs/migrate.md。
func ServerDBPath() string {
	return FirstExisting(ServerDBNew, ServerDBNew, ServerDBLegacy, ServerDBOld)
}
