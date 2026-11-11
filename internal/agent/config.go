package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/domoxiaojun/Meterly/internal/compat"
)

// Config agent 配置(JSON,文件权限必须为 600/400)。
type Config struct {
	ServerURL string `json:"server_url"`
	// Token 来源优先级:token_file > 环境变量 METERLY_TOKEN(回退 TRAFFICSTAT_TOKEN) > token 字段。永不打印、不进日志。
	Token     string `json:"token,omitempty"`
	TokenFile string `json:"token_file,omitempty"`
	CAFile    string `json:"ca_file,omitempty"` // 自签中心证书时指定 CA(证书固定)
	// AllowHTTP 仅允许回环 / NetBird 内网明文时使用。
	AllowHTTP bool `json:"allow_http,omitempty"`

	StateDir       string   `json:"state_dir"`
	Interval       Duration `json:"interval"`         // 上报/汇总间隔,默认 60s
	BufferMaxBytes int64    `json:"buffer_max_bytes"` // 本地缓冲上限,默认 256 MiB
	BatchSize      int      `json:"batch_size"`
	HTTPTimeout    Duration `json:"http_timeout"`

	Collectors CollectorsConfig `json:"collectors"`
}

type CollectorsConfig struct {
	NetDev  NetDevConfig  `json:"netdev"`
	Singbox SingboxConfig `json:"singbox"`
	Site    SiteConfig    `json:"site"`
	Netbird NetbirdConfig `json:"netbird"`
	Xray    XrayConfig    `json:"xray"`
}

type NetDevConfig struct {
	Enabled     bool     `json:"enabled"`
	Interfaces  []string `json:"interfaces"`
	ProcPath    string   `json:"proc_path,omitempty"`
	CounterBits int      `json:"counter_bits,omitempty"` // 64(默认)或 32
	// MaxRateMbps 单网卡速率上限,用于识别计数器异常(默认 25000 Mbps)。
	MaxRateMbps float64 `json:"max_rate_mbps,omitempty"`
}

type SingboxConfig struct {
	Enabled bool `json:"enabled"`
	// ConfigPath sing-box 配置文件,运行时读取 clash_api 地址与 secret(secret 仅在内存)。
	ConfigPath string `json:"config_path,omitempty"`
	// URL 可覆盖 clash_api 地址(如 http://127.0.0.1:23390);为空时从 ConfigPath 推导。
	URL string `json:"url,omitempty"`
	// SecretFile 可选:不读 sing-box 配置时单独提供 secret 文件(600)。
	SecretFile   string   `json:"secret_file,omitempty"`
	PollInterval Duration `json:"poll_interval,omitempty"` // 默认 10s(5~10s)
}

// Duration 支持 "60s" 写法。
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// LoadConfig 读取并校验配置。权限过宽(组/其他用户可读写)直接拒绝。
func LoadConfig(path string) (*Config, error) {
	if err := checkPerm(path); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &Config{}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	if err := c.resolveToken(); err != nil {
		return nil, err
	}
	c.applyDefaults()
	return c, c.validate()
}

func (c *Config) resolveToken() error {
	switch {
	case c.TokenFile != "":
		if err := checkPerm(c.TokenFile); err != nil {
			return err
		}
		t, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return errors.New("读取 token_file 失败")
		}
		c.Token = strings.TrimSpace(string(t))
	default:
		if tok := compat.FirstEnv("METERLY_TOKEN", "TRAFFICSTAT_TOKEN"); tok != "" {
			c.Token = tok
		}
	}
	return nil
}

func checkPerm(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s 权限为 %o,必须为 600(含敏感信息,禁止组/其他用户访问)", path, st.Mode().Perm())
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.StateDir == "" {
		c.StateDir = compat.AgentStateDir()
	}
	if c.Interval.Duration == 0 {
		c.Interval.Duration = time.Minute
	}
	if c.BufferMaxBytes == 0 {
		c.BufferMaxBytes = 256 << 20
	}
	if c.BatchSize == 0 {
		c.BatchSize = 2000
	}
	if c.HTTPTimeout.Duration == 0 {
		c.HTTPTimeout.Duration = 20 * time.Second
	}
	n := &c.Collectors.NetDev
	if n.ProcPath == "" {
		n.ProcPath = "/proc/net/dev"
	}
	if n.CounterBits == 0 {
		n.CounterBits = 64
	}
	if n.MaxRateMbps == 0 {
		n.MaxRateMbps = 25000
	}
	s := &c.Collectors.Singbox
	if s.ConfigPath == "" && s.URL == "" {
		s.ConfigPath = "/etc/sing-box/config.json"
	}
	if s.PollInterval.Duration == 0 {
		s.PollInterval.Duration = 10 * time.Second
	}
	c.Collectors.Site.applyDefaults()
	c.Collectors.Netbird.applyDefaults()
	c.Collectors.Xray.applyDefaults()
}

func (c *Config) validate() error {
	u, err := url.Parse(c.ServerURL)
	if c.ServerURL == "" || err != nil || u.Host == "" {
		return errors.New("server_url 未配置或无效")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !c.AllowHTTP) {
		return errors.New("server_url 必须为 https(内网明文需显式 allow_http: true)")
	}
	if u.User != nil {
		return errors.New("server_url 不能包含用户名/密码")
	}
	if c.Token == "" {
		return errors.New("token 未配置(token_file / 环境变量 METERLY_TOKEN 或 TRAFFICSTAT_TOKEN / token)")
	}
	if c.Interval.Duration < time.Second {
		return errors.New("interval 过小")
	}
	if n := c.Collectors.NetDev; n.Enabled && len(n.Interfaces) == 0 {
		return errors.New("netdev.interfaces 为空")
	}
	if b := c.Collectors.NetDev.CounterBits; b != 32 && b != 64 {
		return errors.New("netdev.counter_bits 只能是 32 或 64")
	}
	if p := c.Collectors.Singbox.PollInterval.Duration; p < time.Second || p > c.Interval.Duration {
		return errors.New("singbox.poll_interval 需在 1s 与 interval 之间")
	}
	if c.BatchSize > 5000 {
		c.BatchSize = 5000
	}
	if c.BufferMaxBytes < 1<<20 {
		return errors.New("buffer_max_bytes 至少 1 MiB")
	}
	return nil
}

// Redacted 返回可安全打印的配置摘要(不含 token/secret)。
func (c *Config) Redacted() string {
	tok := "未设置"
	if c.Token != "" {
		tok = "已设置(隐藏)"
	}
	return fmt.Sprintf("server=%s token=%s state_dir=%s interval=%s netdev=%v%v singbox=%v site=%v(%d logs) netbird=%v xray=%v",
		c.ServerURL, tok, c.StateDir, c.Interval.Duration, c.Collectors.NetDev.Enabled, c.Collectors.NetDev.Interfaces,
		c.Collectors.Singbox.Enabled, c.Collectors.Site.Enabled, len(c.Collectors.Site.Sources),
		c.Collectors.Netbird.Enabled, c.Collectors.Xray.Enabled)
}
