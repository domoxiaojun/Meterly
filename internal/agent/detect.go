package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DetectResult 自动探测结果(不含任何 secret)。
type DetectResult struct {
	Hostname   string                       `json:"hostname"`
	Interfaces []DetectedIface              `json:"interfaces"`
	Collectors map[string]DetectedCollector `json:"collectors"`
	AllowDirs  []string                     `json:"allow_dirs,omitempty"`
	Notes      []string                     `json:"notes,omitempty"`
}

type DetectedIface struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type DetectedCollector struct {
	Detected bool           `json:"detected"`
	Enabled  bool           `json:"enabled"` // 建议开启(已探测到且未被 --skip)
	Reason   string         `json:"reason"`
	Config   map[string]any `json:"config,omitempty"`
}

// DetectSys 可注入的探测环境(测试用夹具)。
type DetectSys struct {
	Root     string // 非空时所有路径相对此根(模拟整机树)
	Hostname string
	LookPath func(string) (string, error)
	Exec     ExecFunc
	Now      time.Time
	// Procs 非空时不再扫描 /proc
	Procs []ProcInfo
}

type ProcInfo struct {
	Name, Cmdline, Exe string
}

func (s *DetectSys) path(p string) string {
	if s == nil || s.Root == "" || !filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.Root, p)
}

func (s *DetectSys) readFile(p string) ([]byte, error) {
	return os.ReadFile(s.path(p))
}

func (s *DetectSys) stat(p string) (os.FileInfo, error) {
	return os.Stat(s.path(p))
}

func (s *DetectSys) lookPath(name string) string {
	if s != nil && s.LookPath != nil {
		p, err := s.LookPath(name)
		if err != nil {
			return ""
		}
		return p
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

func (s *DetectSys) hostname() string {
	if s != nil && s.Hostname != "" {
		return s.Hostname
	}
	h, _ := os.Hostname()
	return h
}

// Detect 扫描本机(或夹具树),给出采集器建议。only/skip 是采集器名列表。
func Detect(sys *DetectSys, only, skip []string) (*DetectResult, error) {
	if sys == nil {
		sys = &DetectSys{}
	}
	want := func(name string) bool {
		if in(skip, name) {
			return false
		}
		if len(only) == 0 {
			return true
		}
		return in(only, name)
	}
	r := &DetectResult{
		Hostname:   sys.hostname(),
		Collectors: map[string]DetectedCollector{},
		AllowDirs:  []string{"/var/log/nginx", "/var/log/openresty", "/opt/1panel/www", "/opt/1panel/apps/openresty"},
	}

	ifaces, notes := detectIfaces(sys)
	r.Interfaces = ifaces
	r.Notes = append(r.Notes, notes...)

	// netdev
	if want("netdev") {
		names := make([]string, 0, len(ifaces))
		for _, i := range ifaces {
			names = append(names, i.Name)
		}
		if len(names) == 0 {
			r.Collectors["netdev"] = DetectedCollector{Detected: false, Reason: "未找到默认路由网卡"}
		} else {
			r.Collectors["netdev"] = DetectedCollector{Detected: true, Enabled: true, Reason: "实际发现的网卡（默认路由优先）",
				Config: map[string]any{"enabled": true, "interfaces": names}}
		}
	} else {
		r.Collectors["netdev"] = DetectedCollector{Reason: skipReason("netdev", only, skip)}
	}

	r.Collectors["singbox"] = detectSingbox(sys, want("singbox"), only, skip)
	r.Collectors["site"] = detectSite(sys, want("site"), only, skip)
	r.Collectors["netbird"] = detectNetbird(sys, want("netbird"), ifaces, only, skip)
	r.Collectors["xray"] = detectXray(sys, want("xray"), only, skip)
	return r, nil
}

func skipReason(name string, only, skip []string) string {
	if in(skip, name) {
		return "已按 --skip 排除"
	}
	if len(only) > 0 && !in(only, name) {
		return "未包含在 --only 中"
	}
	return "未启用"
}

func in(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func detectIfaces(sys *DetectSys) ([]DetectedIface, []string) {
	var out []DetectedIface
	seen := map[string]bool{}
	add := func(name, reason string) {
		if name == "" || seen[name] || skipIface(name) {
			return
		}
		seen[name] = true
		out = append(out, DetectedIface{Name: name, Reason: reason})
	}
	// /proc/net/route: Destination 00000000 是默认路由
	if b, err := sys.readFile("/proc/net/route"); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) >= 2 && fs[1] == "00000000" && fs[0] != "Iface" {
				add(fs[0], "默认路由")
			}
		}
	}
	// /proc/net/dev 里的 wt0
	if b, err := sys.readFile("/proc/net/dev"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if i := strings.IndexByte(line, ':'); i > 0 {
				name := strings.TrimSpace(line[:i])
				if name == "wt0" {
					add("wt0", "NetBird 网卡")
				} else {
					add(name, "发现的网卡")
				}
			}
		}
	}
	var notes []string
	if len(out) == 0 {
		notes = append(notes, "未在 /proc/net/route 找到默认路由网卡,请在配置里手动填写 interfaces")
	}
	return out, notes
}

func skipIface(name string) bool {
	if name == "" || name == "lo" || name == "lo0" {
		return true
	}
	prefs := []string{"docker", "veth", "br-", "virbr", "cni", "flannel", "cali", "kube-", "tun", "dummy", "sit", "fwbr", "fwln", "fwpr", "vnet", "lxc", "zt"}
	for _, p := range prefs {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func fileExists(sys *DetectSys, p string) bool {
	st, err := sys.stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(sys *DetectSys, p string) bool {
	st, err := sys.stat(p)
	return err == nil && st.IsDir()
}

func unitExists(sys *DetectSys, name string) bool {
	for _, p := range []string{
		"/etc/systemd/system/" + name,
		"/lib/systemd/system/" + name,
		"/usr/lib/systemd/system/" + name,
	} {
		if fileExists(sys, p) {
			return true
		}
	}
	return false
}

func detectSingbox(sys *DetectSys, want bool, only, skip []string) DetectedCollector {
	if !want {
		return DetectedCollector{Reason: skipReason("singbox", only, skip)}
	}
	cfgPath := "/etc/sing-box/config.json"
	hasCfg := fileExists(sys, cfgPath)
	hasUnit := unitExists(sys, "sing-box.service")
	hasBin := sys.lookPath("sing-box") != ""
	if !hasCfg && !hasUnit && !hasBin {
		return DetectedCollector{Detected: false, Reason: "未找到 sing-box 配置、单元或可执行文件"}
	}
	c := DetectedCollector{Detected: true, Enabled: true, Config: map[string]any{
		"enabled": true, "config_path": cfgPath, "poll_interval": "10s",
	}}
	var reasons []string
	if hasCfg {
		reasons = append(reasons, "配置 "+cfgPath)
		if b, err := sys.readFile(cfgPath); err == nil {
			url, secret := clashAPIFromConfig(b)
			if url != "" {
				c.Config["url"] = url
				reasons = append(reasons, "clash_api "+url)
			} else {
				reasons = append(reasons, "配置中未找到 clash_api,将在运行时再读")
			}
			if secret {
				reasons = append(reasons, "已找到 secret(不输出)")
			}
		}
	}
	if hasUnit {
		reasons = append(reasons, "systemd 单元")
	}
	if hasBin {
		reasons = append(reasons, "可执行文件")
	}
	c.Reason = strings.Join(reasons, "; ")
	return c
}

func clashAPIFromConfig(b []byte) (url string, hasSecret bool) {
	var root map[string]any
	if json.Unmarshal(b, &root) != nil {
		return "", false
	}
	exp, _ := root["experimental"].(map[string]any)
	if exp == nil {
		return "", false
	}
	api, _ := exp["clash_api"].(map[string]any)
	if api == nil {
		return "", false
	}
	if s, _ := api["secret"].(string); s != "" {
		hasSecret = true
	}
	ec, _ := api["external_controller"].(string)
	if ec == "" {
		return "", hasSecret
	}
	host, port, ok := splitHostPortLoose(ec)
	if !ok {
		return "", hasSecret
	}
	if host == "0.0.0.0" || host == "::" || host == "[::]" || host == "" {
		host = "127.0.0.1"
	}
	return "http://" + host + ":" + port, hasSecret
}

func splitHostPortLoose(s string) (host, port string, ok bool) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return strings.Trim(s[:i], "[]"), s[i+1:], true
	}
	return "", "", false
}

func detectSite(sys *DetectSys, want bool, only, skip []string) DetectedCollector {
	if !want {
		return DetectedCollector{Reason: skipReason("site", only, skip)}
	}
	roots := []string{"/etc/nginx", "/usr/local/nginx/conf", "/opt/1panel/www", "/opt/1panel/apps/openresty"}
	var exist []string
	for _, r := range roots {
		if dirExists(sys, r) {
			exist = append(exist, r)
		}
	}
	hasNginx := sys.lookPath("nginx") != "" || unitExists(sys, "nginx.service") || dirExists(sys, "/etc/nginx")
	has1p := dirExists(sys, "/opt/1panel")
	if len(exist) == 0 && !hasNginx && !has1p {
		return DetectedCollector{Detected: false, Reason: "未找到 nginx / OpenResty / 1Panel"}
	}
	read := func(p string) ([]byte, error) { return sys.readFile(p) }
	walk := func(root string, fn func(string, bool) error) error {
		real := sys.path(root)
		if _, err := os.Stat(real); err != nil {
			return err
		}
		return filepath.Walk(real, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			// 把夹具路径映射回逻辑路径再交给解析(配置里写的是 /www/... 这种)
			logic := p
			if sys.Root != "" {
				if rel, err := filepath.Rel(sys.Root, p); err == nil {
					logic = "/" + rel
				}
			}
			return fn(logic, info.IsDir())
		})
	}
	_, formats, sources := WalkNginxConfs(exist, read, walk)
	// 只保留真实存在的日志(含路径映射)
	var okSrc []SiteSource
	seen := map[string]bool{}
	for _, s := range sources {
		for _, cand := range MapContainerLogPath(s.Path) {
			if !fileExists(sys, cand) {
				continue
			}
			if seen[cand] {
				continue
			}
			seen[cand] = true
			ss := s
			ss.Path = cand
			okSrc = append(okSrc, ss)
		}
	}
	// 额外扫描已知日志位置
	globs := []string{
		"/var/log/nginx/*.log",
		"/opt/1panel/www/sites/*/log/access.log",
		"/opt/1panel/apps/openresty/openresty/log/access.log",
	}
	for _, pat := range globs {
		matches, _ := filepath.Glob(sys.path(pat))
		for _, m := range matches {
			logic := m
			if sys.Root != "" {
				if rel, err := filepath.Rel(sys.Root, m); err == nil {
					logic = "/" + rel
				}
			}
			if isSkippableLog(logic) || seen[logic] {
				continue
			}
			if !fileExists(sys, logic) {
				continue
			}
			seen[logic] = true
			site := inferSiteFromPath(logic)
			if site == "" {
				site = "_default"
			}
			okSrc = append(okSrc, SiteSource{Path: logic, Site: site, Format: "main"})
		}
	}
	if len(okSrc) == 0 && !hasNginx && !has1p {
		return DetectedCollector{Detected: false, Reason: "找到配置目录但没有可读的 access_log"}
	}
	cfg := map[string]any{"enabled": len(okSrc) > 0, "sources": okSrc}
	if len(formats) > 0 {
		cfg["formats"] = formats
	}
	var reasons []string
	if hasNginx {
		reasons = append(reasons, "nginx")
	}
	if has1p {
		reasons = append(reasons, "1Panel/OpenResty")
	}
	reasons = append(reasons, fmtN("日志 %d 个", len(okSrc)))
	if len(okSrc) == 0 {
		return DetectedCollector{Detected: true, Enabled: false, Reason: strings.Join(reasons, "; ") + "(未找到现存日志文件,不建议开启)", Config: cfg}
	}
	return DetectedCollector{Detected: true, Enabled: true, Reason: strings.Join(reasons, "; "), Config: cfg}
}

func fmtN(f string, n int) string { return strings.Replace(f, "%d", itoa(int64(n)), 1) }

func detectNetbird(sys *DetectSys, want bool, ifaces []DetectedIface, only, skip []string) DetectedCollector {
	if !want {
		return DetectedCollector{Reason: skipReason("netbird", only, skip)}
	}
	hasBin := sys.lookPath("netbird") != ""
	hasUnit := unitExists(sys, "netbird.service")
	hasWT := false
	for _, i := range ifaces {
		if i.Name == "wt0" {
			hasWT = true
		}
	}
	if !hasWT {
		// 再看一眼 /proc/net/dev
		if b, err := sys.readFile("/proc/net/dev"); err == nil && strings.Contains(string(b), "wt0:") {
			hasWT = true
		}
	}
	if !hasBin && !hasUnit && !hasWT {
		return DetectedCollector{Detected: false, Reason: "未找到 netbird 或 wt0"}
	}
	var reasons []string
	if hasBin {
		reasons = append(reasons, "netbird 可执行文件")
	}
	if hasUnit {
		reasons = append(reasons, "systemd 单元")
	}
	if hasWT {
		reasons = append(reasons, "网卡 wt0")
	}
	return DetectedCollector{Detected: true, Enabled: true, Reason: strings.Join(reasons, "; "),
		Config: map[string]any{"enabled": true, "command": "netbird", "wg_interface": "wt0", "timeout": "8s"}}
}

func detectXray(sys *DetectSys, want bool, only, skip []string) DetectedCollector {
	if !want {
		return DetectedCollector{Reason: skipReason("xray", only, skip)}
	}
	bins := []string{
		sys.lookPath("xray"),
		"/usr/local/bin/xray",
		"/usr/bin/xray",
		"/root/x-ui/bin/xray-linux-amd64",
		"/usr/local/x-ui/bin/xray-linux-amd64",
	}
	var bin string
	for _, b := range bins {
		if b != "" && fileExists(sys, b) || (b != "" && sys.Root == "" && sys.lookPath(filepath.Base(b)) == b) {
			if b != "" && (fileExists(sys, b) || (sys.Root == "" && filepath.IsAbs(b) == false)) {
				// lookPath result may not exist in fixture
			}
			if b != "" && fileExists(sys, b) {
				bin = b
				break
			}
		}
	}
	if bin == "" {
		// lookPath 在真实系统上找到但不在常见路径
		if p := sys.lookPath("xray"); p != "" && (sys.Root == "" || fileExists(sys, p)) {
			bin = p
		}
	}
	hasUnit := unitExists(sys, "xray.service") || unitExists(sys, "x-ui.service")
	// 配置: x-ui 与官方 xray
	cfgCands := []string{
		"/usr/local/etc/xray/config.json",
		"/etc/xray/config.json",
		"/root/x-ui/bin/config.json",
		"/usr/local/x-ui/bin/config.json",
	}
	var cfgPath, apiAddr string
	for _, p := range cfgCands {
		if !fileExists(sys, p) {
			continue
		}
		cfgPath = p
		if b, err := sys.readFile(p); err == nil {
			apiAddr = xrayAPIAddr(b)
		}
		break
	}
	if bin == "" && !hasUnit && cfgPath == "" {
		return DetectedCollector{Detected: false, Reason: "未找到 xray / x-ui"}
	}
	if apiAddr == "" {
		apiAddr = "127.0.0.1:62789"
	}
	if bin == "" {
		bin = "xray"
	}
	var reasons []string
	if bin != "xray" {
		reasons = append(reasons, "二进制 "+bin)
	} else if sys.lookPath("xray") != "" {
		reasons = append(reasons, "PATH 中的 xray")
	}
	if cfgPath != "" {
		reasons = append(reasons, "配置 "+cfgPath)
	}
	if hasUnit {
		reasons = append(reasons, "systemd 单元")
	}
	reasons = append(reasons, "stats API "+apiAddr+"(无来源 IP)")
	enabled := bin != "" || hasUnit
	return DetectedCollector{Detected: true, Enabled: enabled, Reason: strings.Join(reasons, "; "),
		Config: map[string]any{"enabled": enabled, "bin": bin, "server": apiAddr, "timeout": "5s"}}
}

func xrayAPIAddr(b []byte) string {
	var root map[string]any
	if json.Unmarshal(b, &root) != nil {
		return ""
	}
	// inbound tag=api 的 listen+port
	arr, _ := root["inbounds"].([]any)
	for _, it := range arr {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		tag, _ := m["tag"].(string)
		proto, _ := m["protocol"].(string)
		if tag != "api" && proto != "dokodemo-door" {
			// dokodemo-door 不一定是 api;看 port 62789 或 tag api
			port, _ := asInt(m["port"])
			if tag != "api" && port != 62789 {
				continue
			}
			if tag != "api" {
				continue
			}
		}
		if tag != "api" {
			continue
		}
		listen, _ := m["listen"].(string)
		port, _ := asInt(m["port"])
		if listen == "" || listen == "0.0.0.0" {
			listen = "127.0.0.1"
		}
		if port == 0 {
			port = 62789
		}
		return listen + ":" + itoa(int64(port))
	}
	return ""
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case string:
		var i int
		_, err := fmtSscanf(n, "%d", &i)
		return i, err == nil
	}
	return 0, false
}

func fmtSscanf(s, f string, args ...any) (int, error) {
	return fmtScan(s, f, args...)
}

// 避免在 detect 里多引一个包名冲突,薄封装。
func fmtScan(s, f string, args ...any) (int, error) {
	return jsonScanInt(s, args)
}

func jsonScanInt(s string, args []any) (int, error) {
	if len(args) != 1 {
		return 0, errors.New("bad")
	}
	p, ok := args[0].(*int)
	if !ok {
		return 0, errors.New("bad")
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	*p = n
	return 1, nil
}

// ProposalConfig 根据探测结果生成可写入的 agent Config(不含 token)。
func ProposalConfig(d *DetectResult, serverURL, tokenFile, caFile, stateDir string, allowHTTP bool) *Config {
	c := &Config{
		ServerURL:  serverURL,
		TokenFile:  tokenFile,
		CAFile:     caFile,
		AllowHTTP:  allowHTTP,
		StateDir:   stateDir,
		Interval:   Duration{Duration: time.Minute},
		Collectors: CollectorsConfig{},
	}
	if c.StateDir == "" {
		c.StateDir = "/var/lib/meterly-v2"
	}
	if col, ok := d.Collectors["netdev"]; ok && col.Enabled {
		c.Collectors.NetDev.Enabled = true
		if cfg, _ := col.Config["interfaces"].([]string); len(cfg) > 0 {
			c.Collectors.NetDev.Interfaces = cfg
		} else if raw, ok := col.Config["interfaces"]; ok {
			c.Collectors.NetDev.Interfaces = anyStrings(raw)
		}
	}
	if col, ok := d.Collectors["singbox"]; ok && col.Enabled {
		c.Collectors.Singbox.Enabled = true
		if p, _ := col.Config["config_path"].(string); p != "" {
			c.Collectors.Singbox.ConfigPath = p
		}
		if u, _ := col.Config["url"].(string); u != "" {
			c.Collectors.Singbox.URL = u
		}
		c.Collectors.Singbox.PollInterval = Duration{Duration: 10 * time.Second}
	}
	if col, ok := d.Collectors["site"]; ok && col.Enabled {
		c.Collectors.Site.Enabled = true
		c.Collectors.Site.Sources = sourcesFrom(col.Config["sources"])
		c.Collectors.Site.Formats = stringMap(col.Config["formats"])
		c.Collectors.Site.AllowDirs = d.AllowDirs
	}
	if col, ok := d.Collectors["netbird"]; ok && col.Enabled {
		c.Collectors.Netbird.Enabled = true
		if cmd, _ := col.Config["command"].(string); cmd != "" {
			c.Collectors.Netbird.Command = cmd
		}
		if iface, _ := col.Config["wg_interface"].(string); iface != "" {
			c.Collectors.Netbird.WGInterface = iface
		}
		c.Collectors.Netbird.Timeout = Duration{Duration: 8 * time.Second}
	}
	if col, ok := d.Collectors["xray"]; ok && col.Enabled {
		c.Collectors.Xray.Enabled = true
		if b, _ := col.Config["bin"].(string); b != "" {
			c.Collectors.Xray.Bin = b
		}
		if s, _ := col.Config["server"].(string); s != "" {
			c.Collectors.Xray.Server = s
		}
		c.Collectors.Xray.Timeout = Duration{Duration: 5 * time.Second}
	}
	c.applyDefaults()
	return c
}

func anyStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		var out []string
		for _, it := range x {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func stringMap(v any) map[string]string {
	switch x := v.(type) {
	case map[string]string:
		return x
	case map[string]any:
		out := map[string]string{}
		for k, iv := range x {
			if s, ok := iv.(string); ok {
				out[k] = s
			}
		}
		return out
	}
	return nil
}

func sourcesFrom(v any) []SiteSource {
	if v == nil {
		return nil
	}
	if s, ok := v.([]SiteSource); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []SiteSource
	_ = json.Unmarshal(b, &out)
	return out
}

// WriteConfigFile 原子写入 0600 的 JSON 配置(不含 token 明文)。
func WriteConfigFile(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// 不把内存里的 Token 写进文件
	cp := *c
	cp.Token = ""
	b, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writeFileAtomic(path, b)
}

// 让未使用的 fs 导入在某些构建标签下不报错。
var _ fs.FS
