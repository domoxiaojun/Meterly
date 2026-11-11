package proto

// SiteMetrics 保留网站、IP、具体状态码的联合分布；没有请求长度时不能声称上行是零。
type SiteMetrics struct {
	Status   int    `json:"status"`
	Requests uint64 `json:"requests"`
	RXKnown  bool   `json:"rx_known"`
	IPSource string `json:"ip_source"`
}

type Source struct {
	ID        string `json:"id"`
	Site      string `json:"site"`
	Path      string `json:"path"`
	Format    string `json:"format"`
	IPMode    string `json:"ip_mode"`
	Note      string `json:"note,omitempty"`
	Disabled  bool   `json:"disabled"`
	Available bool   `json:"available"`
}

type Inventory struct {
	Interfaces       []string        `json:"interfaces"`
	DefaultInterface string          `json:"default_interface"`
	Sources          []Source        `json:"sources"`
	Detected         map[string]bool `json:"detected"`
	ScannedAt        int64           `json:"scanned_at"`
	OS               string          `json:"os"`
	Arch             string          `json:"arch"`
}

// Policy 仅允许采集选项；不传凭据、命令或可执行文件路径。
type Policy struct {
	Collectors      map[string]bool `json:"collectors,omitempty"`
	Interfaces      *[]string       `json:"interfaces,omitempty"`
	DisabledSources []string        `json:"disabled_sources,omitempty"`
	TrustedProxies  []string        `json:"trusted_proxies,omitempty"`
	IPHeader        string          `json:"ip_header,omitempty"`
}

type DesiredConfig struct {
	Version int64  `json:"version"`
	Policy  Policy `json:"policy"`
}

type UpgradeTask struct {
	ID        int64  `json:"id"`
	Version   string `json:"version"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

type ControlResponse struct {
	Config  DesiredConfig `json:"config"`
	Upgrade *UpgradeTask  `json:"upgrade,omitempty"`
}
