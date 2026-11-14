package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/version"
)

// Published Cloudflare ranges, verified 2026-11-11 against /ips-v4 and /ips-v6.
var cloudflareRanges = strings.Fields(`173.245.48.0/20 103.21.244.0/22 103.22.200.0/22 103.31.4.0/22 141.101.64.0/18 108.162.192.0/18 190.93.240.0/20 188.114.96.0/20 197.234.240.0/22 198.41.128.0/17 162.158.0.0/15 104.16.0.0/13 104.24.0.0/14 172.64.0.0/13 131.0.72.0/22 2400:cb00::/32 2606:4700::/32 2803:f800::/32 2405:b500::/32 2405:8100::/32 2a06:98c0::/29 2c0f:f248::/32`)

func expandProxies(v []string) []string {
	out := []string{}
	for _, p := range v {
		if p == "cloudflare" {
			out = append(out, cloudflareRanges...)
		} else {
			out = append(out, p)
		}
	}
	return out
}
func applyPolicy(c *Config, p proto.Policy) {
	for k, v := range p.Collectors {
		switch k {
		case "netdev":
			c.Collectors.NetDev.Enabled = v
		case "singbox":
			c.Collectors.Singbox.Enabled = v
		case "site":
			c.Collectors.Site.Enabled = v
		case "netbird":
			c.Collectors.Netbird.Enabled = v
		case "xray":
			c.Collectors.Xray.Enabled = v
		}
	}
	if p.Interfaces != nil {
		c.Collectors.NetDev.Interfaces = append([]string{}, (*p.Interfaces)...)
	}
	if p.TrustedProxies != nil {
		c.Collectors.Site.TrustedProxies = expandProxies(p.TrustedProxies)
	}
	if p.IPHeader != "" {
		c.Collectors.Site.IPHeader = p.IPHeader
		for i := range c.Collectors.Site.Sources {
			c.Collectors.Site.Sources[i].IPHeader = p.IPHeader
			c.Collectors.Site.Sources[i].TrustedProxies = c.Collectors.Site.TrustedProxies
			c.Collectors.Site.Sources[i].RealIPApplied = false
		}
	}
	if len(p.DisabledSources) > 0 {
		out := []SiteSource{}
		for _, src := range c.Collectors.Site.Sources {
			disabled := false
			for _, id := range p.DisabledSources {
				if id == SourceID(src.Path) {
					disabled = true
				}
			}
			if !disabled {
				out = append(out, src)
			}
		}
		c.Collectors.Site.Sources = out
	}
}
func (a *Agent) control(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		rd = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.cfg.ServerURL, "/")+path, rd)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, e := a.client.Do(req)
	if e != nil {
		return fmt.Errorf("管理连接: %s", netErrKind(e))
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("管理接口 HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	return nil
}
func (a *Agent) RefreshConfiguration(ctx context.Context, force bool) error {
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var control proto.ControlResponse
	remoteErr := a.control(cctx, http.MethodGet, "/api/v2/agent/control", nil, &control)
	if remoteErr == nil {
		if e := control.Config.Policy.Validate(); e != nil {
			a.mu.Lock()
			a.configError = e.Error()
			a.mu.Unlock()
			return e
		}
	} else if force {
		b, e := os.ReadFile(filepath.Join(a.cfg.StateDir, "desired.json"))
		if e == nil {
			_ = json.Unmarshal(b, &control.Config)
		}
	}
	if remoteErr == nil {
		defer a.processUpgrade(ctx, control.Upgrade)
	}
	if e := control.Config.Policy.Validate(); e != nil {
		return e
	}
	a.mu.Lock()
	needs := force || a.Now().After(a.nextDiscovery) || remoteErr == nil && control.Config.Version != a.configVersion
	if !needs {
		a.mu.Unlock()
		return remoteErr
	}
	if remoteErr == nil || force {
		a.desired = control.Config
	}
	a.mu.Unlock()
	sys := a.DetectSys
	if sys == nil {
		sys = &DetectSys{}
	}
	d, e := Detect(sys, nil, nil)
	if e != nil {
		return e
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var next Config
	baseJSON, _ := json.Marshal(a.baseConfig)
	_ = json.Unmarshal(baseJSON, &next)
	if next.Discovery == nil || *next.Discovery {
		base := next.Collectors
		proposal := ProposalConfig(d, next.ServerURL, next.TokenFile, next.CAFile, next.StateDir, next.AllowHTTP)
		next.Collectors = proposal.Collectors
		// Local collector configuration is explicit; installer-generated configs use overrides instead.
		if base.NetDev.Enabled {
			next.Collectors.NetDev = base.NetDev
		}
		if base.Singbox.Enabled {
			next.Collectors.Singbox = base.Singbox
		}
		if base.Site.Enabled {
			next.Collectors.Site = base.Site
		}
		if base.Netbird.Enabled {
			next.Collectors.Netbird = base.Netbird
		}
		if base.Xray.Enabled {
			next.Collectors.Xray = base.Xray
		}
	}
	// Keep disappeared source descriptors and offsets across restarts. Missing files remain diagnosable.
	knownPath := filepath.Join(a.cfg.StateDir, "sources.json")
	var known []SiteSource
	if b, e := os.ReadFile(knownPath); e == nil {
		_ = json.Unmarshal(b, &known)
	}
	found := map[string]bool{}
	for _, src := range next.Collectors.Site.Sources {
		found[src.Path] = true
	}
	for _, src := range known {
		if !found[src.Path] {
			next.Collectors.Site.Sources = append(next.Collectors.Site.Sources, src)
		}
	}
	if len(next.Collectors.Site.Sources) > 0 {
		next.Collectors.Site.Enabled = true
	}
	allSources := append([]SiteSource(nil), next.Collectors.Site.Sources...)
	for i := range allSources {
		src := &allSources[i]
		if src.FormatDef == "" {
			src.FormatDef = next.Collectors.Site.Formats[src.Format]
		}
	}
	applyPolicy(&next, a.desired.Policy)
	// Replay only fields explicitly present locally after remote policy; defaults must not mask policy.
	if len(a.baseConfig.localCollectors) > 0 {
		b, _ := json.Marshal(next.Collectors)
		var all map[string]map[string]json.RawMessage
		_ = json.Unmarshal(b, &all)
		for name, raw := range a.baseConfig.localCollectors {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			for k, v := range fields {
				all[name][k] = v
			}
		}
		b, _ = json.Marshal(all)
		_ = json.Unmarshal(b, &next.Collectors)
	}
	applyPolicy(&next, a.baseConfig.Overrides)
	next.applyDefaults()
	if e = next.validate(); e != nil {
		a.configError = e.Error()
		return e
	}
	raw, _ := json.Marshal(a.desired)
	if e = writeFileAtomic(filepath.Join(a.cfg.StateDir, "desired.json"), raw); e != nil {
		return e
	}
	sourceJSON, _ := json.Marshal(allSources)
	if e = writeFileAtomic(knownPath, sourceJSON); e != nil {
		return e
	}
	// Do not discard accumulated values while a failed Tick still owns pending records.
	if len(a.st.Pending) > 0 {
		a.configError = "等待待写记录持久化后应用配置"
		return errors.New(a.configError)
	}
	if next.Collectors.NetDev.Enabled {
		a.netdev = &NetDev{Path: next.Collectors.NetDev.ProcPath, Ifaces: next.Collectors.NetDev.Interfaces}
	} else {
		a.netdev = nil
	}
	if next.Collectors.Singbox.Enabled {
		if a.sb == nil {
			a.sb = NewSingboxAcc(a.st.Singbox)
		}
		if a.clash == nil || !reflect.DeepEqual(next.Collectors.Singbox, a.cfg.Collectors.Singbox) {
			a.clash = NewClashClient(next.Collectors.Singbox)
		}
	} else {
		a.sb = nil
		a.clash = nil
	}
	if next.Collectors.Site.Enabled {
		fresh := NewSiteAcc(next.Collectors.Site, a.st.SiteOffsets)
		if a.site != nil {
			fresh.files = a.site.files
		}
		a.site = fresh
	} else {
		if a.site != nil {
			a.site.Close()
		}
		a.site = nil
	}
	if next.Collectors.Netbird.Enabled {
		if a.netbird == nil || !reflect.DeepEqual(next.Collectors.Netbird, a.cfg.Collectors.Netbird) {
			a.netbird = NewNetbirdAcc(next.Collectors.Netbird, a.st.Netbird)
		}
	} else {
		a.netbird = nil
	}
	if next.Collectors.Xray.Enabled {
		if a.xray == nil || !reflect.DeepEqual(next.Collectors.Xray, a.cfg.Collectors.Xray) {
			a.xray = NewXrayAcc(next.Collectors.Xray, a.st.Xray)
		}
	} else {
		a.xray = nil
	}
	a.cfg.Collectors = next.Collectors
	for k, v := range map[string]bool{"netdev": a.netdev != nil, "singbox": a.sb != nil, "site": a.site != nil, "netbird": a.netbird != nil, "xray": a.xray != nil} {
		h := a.health[k]
		h.Enabled = v
		a.health[k] = h
	}
	inv := &proto.Inventory{Interfaces: []string{}, Sources: []proto.Source{}, Detected: map[string]bool{}, ScannedAt: a.Now().Unix(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	for _, it := range d.Interfaces {
		inv.Interfaces = append(inv.Interfaces, it.Name)
		if it.Reason == "默认路由" && inv.DefaultInterface == "" {
			inv.DefaultInterface = it.Name
		}
	}
	for k, col := range d.Collectors {
		inv.Detected[k] = col.Detected
	}
	enabledSources := map[string]bool{}
	for _, src := range next.Collectors.Site.Sources {
		enabledSources[src.Path] = next.Collectors.Site.Enabled
	}
	for _, src := range allSources {
		mode := next.Collectors.Site.IPHeader
		if src.RealIPApplied {
			mode = "nginx_realip"
		}
		if mode == "" {
			mode = "remote_addr"
		}
		note := ""
		if mode == "remote_addr" {
			note = "若有 CDN，请配置可信代理并检查真实客户端 IP 字段"
		}
		_, statErr := sys.stat(src.Path)
		available := statErr == nil
		if !available {
			note = "日志暂时不可访问，已保留历史与断点"
		}
		if !enabledSources[src.Path] {
			note = "已停用；自动发现不会重新开启"
		}
		site := NormalizeSite(src.Site)
		if site == "" || site == "_default" {
			site = SourceID(src.Path)
		}
		inv.Sources = append(inv.Sources, proto.Source{Disabled: !enabledSources[src.Path], Available: available, ID: SourceID(src.Path), Site: site, Path: src.Path, Format: src.Format, IPMode: mode, Note: note})
	}
	if b, e := sys.readFile("/proc/net/dev"); e == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if before, _, ok := strings.Cut(line, ":"); ok {
				name := strings.TrimSpace(before)
				if !skipIface(name) && !in(inv.Interfaces, name) {
					inv.Interfaces = append(inv.Interfaces, name)
				}
			}
		}
	}
	sort.Strings(inv.Interfaces)
	sort.Slice(inv.Sources, func(i, j int) bool { return inv.Sources[i].Path < inv.Sources[j].Path })
	a.inventory = inv
	a.configVersion = a.desired.Version
	a.configError = ""
	a.nextDiscovery = a.Now().Add(5 * time.Minute)
	ready, _ := json.Marshal(map[string]any{"version": version.Version, "pid": os.Getpid(), "at": a.Now().Unix()})
	_ = writeFileAtomic(filepath.Join(a.cfg.StateDir, "ready.json"), ready)
	return remoteErr
}
func (a *Agent) processUpgrade(ctx context.Context, task *proto.UpgradeTask) {
	// Report a durable terminal result before accepting another task (including a stale copy of the same task).
	path := filepath.Join(a.cfg.StateDir, "upgrade-result.json")
	if b, e := os.ReadFile(path); e == nil {
		var result UpgradeResult
		if json.Unmarshal(b, &result) == nil {
			if result.Status == "running" && a.updaterStopped(ctx, path) {
				result.Status = "failed"
				result.Message = "更新器已中断；安装结果未确认，回滚备份保留，请检查版本后重新发起升级"
				raw, _ := json.Marshal(result)
				if writeFileAtomic(path, raw) != nil {
					return
				}
			}
			if e = a.control(ctx, "POST", "/api/v2/agent/upgrade/status", result, nil); e == nil && result.Status != "running" {
				// Remove the request first: a crash must not requeue an acknowledged task.
				if e = os.Remove(filepath.Join(a.cfg.StateDir, "upgrade-request.json")); e != nil && !os.IsNotExist(e) {
					return
				}
				_ = os.Remove(path)
			}
			return
		}
	}
	// A crash can occur after queuing the request but before the updater writes running.
	req := filepath.Join(a.cfg.StateDir, "upgrade-request.json")
	if b, e := os.ReadFile(req); e == nil {
		var old proto.UpgradeTask
		if json.Unmarshal(b, &old) == nil && a.updaterStopped(ctx, req) {
			raw, _ := json.Marshal(UpgradeResult{ID: old.ID, Status: "failed", Message: "更新器未完成排队任务，请重新发起升级"})
			_ = writeFileAtomic(path, raw)
		}
		return
	}
	if task != nil {
		if e := a.queueUpgrade(*task); e != nil {
			a.mu.Lock()
			a.configError = e.Error()
			a.mu.Unlock()
		}
	}
}

func (a *Agent) upgradeExec(ctx context.Context, args ...string) ([]byte, error) {
	fn := a.UpgradeExec
	if fn == nil {
		fn = defaultExec
	}
	return fn(ctx, "systemctl", args...)
}

func (a *Agent) updaterStopped(ctx context.Context, path string) bool {
	fi, e := os.Stat(path)
	if e != nil || a.Now().Sub(fi.ModTime()) < 30*time.Second {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	b, e := a.upgradeExec(ctx, "show", "--all", "--property=ActiveState", "--property=Job", "meterly-agent-updater.service")
	var state, job string
	jobSeen := false
	for _, line := range strings.Split(string(b), "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "ActiveState":
			state = value
		case "Job":
			job = value
			jobSeen = true
		}
	}
	// An inactive unit may still have a queued start job. Do not race that job.
	return e == nil && jobSeen && (job == "" || job == "0") && (state == "inactive" || state == "failed")
}

func (a *Agent) queueUpgrade(u proto.UpgradeTask) error {
	if runtime.GOOS != "linux" {
		return errors.New("受控升级仅支持 Linux systemd")
	}
	if e := ValidateUpgrade(u); e != nil {
		return e
	}
	if u.CreatedAt > 0 && a.Now().Unix()-u.CreatedAt >= proto.UpgradeLifetime {
		return errors.New("升级任务已过期")
	}
	req := filepath.Join(a.cfg.StateDir, "upgrade-request.json")
	if b, e := os.ReadFile(req); e == nil {
		var old proto.UpgradeTask
		if json.Unmarshal(b, &old) == nil && old.ID == u.ID {
			return nil
		}
	}
	raw, _ := json.Marshal(u)
	if e := writeFileAtomic(req, raw); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, e := a.upgradeExec(ctx, "start", "--no-block", "meterly-agent-updater.service"); e != nil {
		_ = os.Remove(req)
		return errors.New("无法启动更新器，请检查安装的 systemd unit")
	}
	return nil
}
