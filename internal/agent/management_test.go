package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

func TestCDNAttribution(t *testing.T) {
	cases := []struct{ name, remote, header, raw, ipv6, want, mode string }{
		{"direct", "192.0.2.1", "X-Forwarded-For", "1.1.1.1", "", "192.0.2.1", "remote_addr"},
		{"xff", "10.0.0.1", "X-Forwarded-For", "1.1.1.1, 2001:db8::2, 10.0.0.2", "", "2001:db8::2", "trusted_xff"},
		{"missing", "10.0.0.1", "X-Forwarded-For", "", "", "10.0.0.1", "unresolved"},
		{"forged", "192.0.2.1", "CF-Connecting-IP", "8.8.8.8", "", "192.0.2.1", "remote_addr"},
		{"cf ipv6", "173.245.48.2", "CF-Connecting-IP", "240.1.2.3", "2001:db8::3", "2001:db8::3", "cloudflare"},
		{"no address", "", "CF-Connecting-IP", "1.1.1.1", "", "~unknown", "unresolved"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := &LogLine{IP: c.remote, Headers: map[string]string{"http_" + strings.ReplaceAll(strings.ToLower(c.header), "-", "_"): c.raw, "http_cf_connecting_ipv6": c.ipv6}}
			ip, mode := ResolveClientIP(l, SiteSource{}, SiteConfig{TrustedProxies: []string{"10.0.0.0/8", "cloudflare"}, IPHeader: c.header})
			if ip != c.want || mode != c.mode {
				t.Fatalf("%s %s", ip, mode)
			}
		})
	}
	blocks := parseServerBlocks(`http { set_real_ip_from 10.0.0.0/8; set_real_ip_from 2001:db8::/32; real_ip_header X-Forwarded-For; server { server_name a.test; access_log /var/log/nginx/a.log; location / { add_header Test "{#}"; } } server { server_name b.test; access_log /var/log/nginx/b.log; location / { real_ip_header X-Real-IP; } } }`)
	if len(blocks) != 2 || len(blocks[0].TrustedProxies) != 2 || blocks[0].IPHeader != "X-Forwarded-For" || blocks[1].IPHeader != "" {
		t.Fatalf("incorrect real-IP inheritance %+v", blocks)
	}
}
func TestDiscoveryConfigAndMissingSources(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	version := int64(0)
	policy := proto.Policy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(proto.ControlResponse{Config: proto.DesiredConfig{Version: version, Policy: policy}})
	}))
	defer srv.Close()
	cfg := &Config{ServerURL: srv.URL, AllowHTTP: true, Token: "test-only", StateDir: state}
	cfg.applyDefaults()
	a, e := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	a.DetectSys = &DetectSys{Root: root, Procs: []ProcInfo{}, LookPath: func(string) (string, error) { return "", errors.New("no program") }, Exec: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no service") }}
	write(t, root, "proc/net/route", "Iface Destination\nens3 00000000\n", 0644)
	write(t, root, "proc/net/dev", "ens3: 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\nens4: 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n", 0644)
	write(t, root, "etc/nginx/a.conf", "server { server_name a.test; access_log /var/log/nginx/a.log combined; }", 0644)
	write(t, root, "var/log/nginx/a.log", "", 0644)
	if e = a.RefreshConfiguration(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	if len(a.inventory.Sources) != 1 || !in(a.inventory.Interfaces, "ens4") || a.inventory.DefaultInterface != "ens3" {
		t.Fatalf("inventory %+v", a.inventory)
	}
	source := a.inventory.Sources[0].ID
	version = 1
	policy.DisabledSources = []string{source}
	if e = a.RefreshConfiguration(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	if len(a.cfg.Collectors.Site.Sources) != 0 || len(a.inventory.Sources) != 1 || !a.inventory.Sources[0].Disabled {
		t.Fatal("disabled source disappeared or reenabled")
	}
	write(t, root, "etc/nginx/b.conf", "server { server_name b.test; access_log /var/log/nginx/b.log combined; }", 0644)
	write(t, root, "var/log/nginx/b.log", "", 0644)
	if e = a.RefreshConfiguration(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	if len(a.cfg.Collectors.Site.Sources) != 1 || len(a.inventory.Sources) != 2 {
		t.Fatal("new source not discovered")
	}
	os.Remove(filepath.Join(root, "var/log/nginx/b.log"))
	if e = a.RefreshConfiguration(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	if len(a.inventory.Sources) != 2 {
		t.Fatal("missing source discarded")
	}
	version = 2
	policy.IPHeader = "arbitrary-shell"
	if e = a.RefreshConfiguration(context.Background(), false); e == nil || a.configVersion != 1 {
		t.Fatal("invalid config replaced known-good config")
	}
}
func TestUpdaterReadinessAndRollback(t *testing.T) {
	for _, ok := range []bool{true, false} {
		t.Run(map[bool]string{true: "ready", false: "rollback"}[ok], func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "agent")
			os.WriteFile(target, []byte("old"), 0755)
			task := proto.UpgradeTask{ID: 1, Version: "v2.0.0", URL: "https://github.com/domoxiaojun/Meterly/releases/download/v2.0.0/meterly-agent-linux-" + runtime.GOARCH, SHA256: strings.Repeat("a", 64)}
			b, _ := json.Marshal(task)
			os.WriteFile(filepath.Join(dir, "upgrade-request.json"), b, 0600)
			starts := 0
			u := Updater{ReadyTimeout: 5 * time.Millisecond, Download: func(context.Context, string, string, string) error { return os.WriteFile(target, []byte("new"), 0755) }, Exec: func(context.Context, string, ...string) ([]byte, error) {
				starts++
				if ok {
					b, _ := json.Marshal(map[string]any{"version": "v2.0.0", "at": time.Now().Unix()})
					os.WriteFile(filepath.Join(dir, "ready.json"), b, 0600)
				}
				return nil, nil
			}}
			e := u.Apply(context.Background(), dir, target)
			got, _ := os.ReadFile(target)
			var result UpgradeResult
			b, _ = os.ReadFile(filepath.Join(dir, "upgrade-result.json"))
			json.Unmarshal(b, &result)
			if ok && (e != nil || string(got) != "new" || result.Status != "succeeded" || starts != 1) {
				t.Fatalf("%s %+v %v", got, result, e)
			}
			if !ok && (e == nil || string(got) != "old" || result.Status != "rolled_back" || starts != 2) {
				t.Fatalf("%s %+v %v", got, result, e)
			}
		})
	}
}
func TestOversizedLineDoesNotBlockOtherLogs(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.log")
	good := filepath.Join(dir, "good.log")
	os.WriteFile(bad, []byte(strings.Repeat("x", 200000)+"\n"), 0600)
	os.WriteFile(good, []byte("192.0.2.1 - - [11/Nov/2026:08:00:00 +0000] \"GET / HTTP/1.1\" 200 100 \"-\" \"-\"\n"), 0600)
	a := NewSiteAcc(SiteConfig{FromStart: true, AllowDirs: []string{dir}, MaxReadBytes: 128 << 10, Sources: []SiteSource{{Path: bad}, {Path: good, Site: "a.test"}}}, nil)
	defer a.Close()
	var total uint64
	for i := 0; i < 4; i++ {
		r, _, e := a.Collect()
		if e != nil {
			t.Fatal(e)
		}
		for _, c := range r {
			total += c.TX
		}
	}
	if total != 100 {
		t.Fatal("bad log blocked or duplicate good log", total)
	}
}

func TestNginxIncludeInheritsRealIP(t *testing.T) {
	files := map[string]string{"/etc/nginx/nginx.conf": `http { include proxy.conf; include conf.d/*.conf; }`, "/etc/nginx/proxy.conf": `set_real_ip_from 10.0.0.0/8; real_ip_header X-Forwarded-For;`, "/etc/nginx/conf.d/a.conf": `server { server_name a.test; access_log /var/log/nginx/a.log combined; }`}
	_, _, sources := WalkNginxConfs([]string{"/etc/nginx"}, func(p string) ([]byte, error) { return []byte(files[p]), nil }, func(_ string, fn func(string, bool) error) error {
		for _, p := range []string{"/etc/nginx/conf.d/a.conf", "/etc/nginx/nginx.conf", "/etc/nginx/proxy.conf"} {
			if e := fn(p, false); e != nil {
				return e
			}
		}
		return nil
	})
	if len(sources) != 2 {
		t.Fatalf("sources %+v", sources)
	}
	for _, src := range sources {
		if !src.RealIPApplied || src.IPHeader != "X-Forwarded-For" || len(src.TrustedProxies) != 1 {
			t.Fatalf("inheritance lost %+v", src)
		}
	}
}
