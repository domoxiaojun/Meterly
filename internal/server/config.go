package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/domoxiaojun/Meterly/internal/compat"
	"github.com/domoxiaojun/Meterly/internal/store"
)

// Config 中心配置(JSON)。面板口令来源:panel_token_file > 环境变量 METERLY_PANEL_TOKEN(回退 TRAFFICSTAT_PANEL_TOKEN)。
type Config struct {
	Listen         string          `json:"listen"`  // 默认 127.0.0.1:8787
	DBPath         string          `json:"db_path"` // 默认 /var/lib/meterly-server/meterly.db(若不存在则回退 trafficstat.db)
	PanelTokenFile string          `json:"panel_token_file,omitempty"`
	TZ             string          `json:"tz"`                        // 面板展示时区,默认 Asia/Singapore
	ResetDay       int             `json:"reset_day"`                 // 多机汇总视图默认周期重置日,默认 9
	TrustedProxies []string        `json:"trusted_proxies,omitempty"` // 反代地址或 CIDR(取 X-Forwarded-For)
	TLSCert        string          `json:"tls_cert,omitempty"`
	TLSKey         string          `json:"tls_key,omitempty"`
	OfflineAfter   int             `json:"offline_after_minutes"` // 判定离线,默认 5
	Retention      store.Retention `json:"retention"`
	Geo            GeoConfig       `json:"geo"`

	panelToken string
	loc        *time.Location
	proxies    []netip.Prefix
}

// GeoConfig 离线归属地库(DB-IP Lite)。
type GeoConfig struct {
	Dir        string `json:"dir,omitempty"`         // 默认 db_path 同目录下 geo/
	AutoUpdate *bool  `json:"auto_update,omitempty"` // 首次下载 + 月度更新,默认 true
	BaseURL    string `json:"base_url,omitempty"`    // 默认 https://download.db-ip.com/free
	CheckHours int    `json:"check_hours,omitempty"` // 检查间隔,默认 24(只在跨月后真正下载)
}

// AutoUpdateOn 是否自动下载/更新。
func (g GeoConfig) AutoUpdateOn() bool { return g.AutoUpdate == nil || *g.AutoUpdate }

// ConfigPath 配置文件路径:命令行 > 环境变量 METERLY_CONFIG(回退 TRAFFICSTAT_CONFIG) > 已存在的默认文件。
func ConfigPath(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := compat.FirstEnv("METERLY_CONFIG", "TRAFFICSTAT_CONFIG"); v != "" {
		return v
	}
	return compat.ServerConfigPath()
}

func LoadConfig(path string) (*Config, error) {
	c := &Config{}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(c); err != nil {
			return nil, fmt.Errorf("解析配置失败: %w", err)
		}
	}
	return c, c.finish()
}

func (c *Config) finish() error {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8787"
	}
	if c.DBPath == "" {
		c.DBPath = compat.ServerDBPath()
	}
	if c.TZ == "" {
		c.TZ = "Asia/Singapore"
	}
	if c.ResetDay == 0 {
		c.ResetDay = 9
	}
	if c.OfflineAfter == 0 {
		c.OfflineAfter = 5
	}
	c.Retention.Defaults()
	if c.Geo.Dir == "" {
		c.Geo.Dir = filepath.Join(filepath.Dir(c.DBPath), "geo")
	}
	if c.Geo.CheckHours <= 0 {
		c.Geo.CheckHours = 24
	}
	for _, p := range c.TrustedProxies {
		if pf, err := netip.ParsePrefix(p); err == nil {
			c.proxies = append(c.proxies, pf.Masked())
		} else if a, err := netip.ParseAddr(p); err == nil {
			c.proxies = append(c.proxies, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		} else {
			return fmt.Errorf("trusted_proxies 无效: %s", p)
		}
	}
	loc, err := time.LoadLocation(c.TZ)
	if err != nil {
		return fmt.Errorf("tz 无效: %s", c.TZ)
	}
	c.loc = loc
	if c.PanelTokenFile != "" {
		st, err := os.Stat(c.PanelTokenFile)
		if err != nil {
			return errors.New("读取 panel_token_file 失败")
		}
		if st.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("panel_token_file 权限为 %o,必须为 600", st.Mode().Perm())
		}
		b, err := os.ReadFile(c.PanelTokenFile)
		if err != nil {
			return errors.New("读取 panel_token_file 失败")
		}
		c.panelToken = strings.TrimSpace(string(b))
	} else {
		c.panelToken = compat.FirstEnv("METERLY_PANEL_TOKEN", "TRAFFICSTAT_PANEL_TOKEN")
	}
	if c.panelToken != "" && len(c.panelToken) < 12 {
		return errors.New("面板口令至少 12 个字符")
	}
	if c.panelToken == "" && !c.Loopback() {
		return errors.New("监听非回环地址时必须配置面板口令(panel_token_file 或 METERLY_PANEL_TOKEN / TRAFFICSTAT_PANEL_TOKEN)")
	}
	return nil
}

// Loopback 是否只监听回环地址。
func (c *Config) Loopback() bool {
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Config) Location() *time.Location { return c.loc }

// PanelAuth 是否配置了面板口令。
func (c *Config) PanelAuth() bool { return c.panelToken != "" }
