package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/agent"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/server"
	"github.com/domoxiaojun/Meterly/internal/store"
)

func TestM3AgentToPanel(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now0 := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

	cfg := &server.Config{Listen: "127.0.0.1:0", TZ: "Asia/Singapore", ResetDay: 9, DBPath: filepath.Join(dir, "s.db")}
	t.Setenv("METERLY_PANEL_TOKEN", "panel-token-for-tests-only")
	if err := cfgFinish(cfg); err != nil {
		t.Fatal(err)
	}
	srv := server.New(cfg, st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.Now = func() time.Time { return now0.Add(2 * time.Minute) }
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 注册码入会
	code, _, err := st.CreateEnroll(ctx, "box-m3", store.EnrollOpts{TTL: time.Hour}, now0)
	if err != nil {
		t.Fatal(err)
	}
	er, err := agent.RedeemEnroll(ctx, ts.URL, "", true, code, "box-m3", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tokFile := filepath.Join(dir, "agent.token")
	if err := os.WriteFile(tokFile, []byte(er.Token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	logp := filepath.Join(dir, "access.log")
	var lb strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&lb, "192.0.2.%d - - [11/Nov/2026:12:00:%02d +0000] \"GET /video HTTP/1.1\" 200 %d \"-\" \"Emby\" \"-\"\n", 1+i%4, i, 10000+i*100)
	}
	if err := os.WriteFile(logp, []byte(lb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	acfg := &agent.Config{
		ServerURL: ts.URL, TokenFile: tokFile, AllowHTTP: true, StateDir: filepath.Join(dir, "state"),
		Interval: agent.Duration{Duration: time.Minute}, BatchSize: 500, HTTPTimeout: agent.Duration{Duration: 5 * time.Second},
		Collectors: agent.CollectorsConfig{
			Site: agent.SiteConfig{Enabled: true, FromStart: true, AllowDirs: []string{dir},
				Sources: []agent.SiteSource{{Path: logp, Site: "media.example.com", Format: "main"}}},
			Netbird: agent.NetbirdConfig{Enabled: true},
			Xray:    agent.XrayConfig{Enabled: true, Bin: "xray", Server: "127.0.0.1:62789"},
		},
	}
	// 不走 LoadConfig(它要校验 token 文件权限,我们已 0600);直接填 Token
	acfg.Token = er.Token
	acfg.StateDir = filepath.Join(dir, "state")

	a, err := agent.New(acfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// 注入假命令
	clk := now0
	a.Now = func() time.Time { return clk }
	nbN, xrN := 0, 0
	a.SetNetbirdExec(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		rx := 1000 + nbN*500
		nbN++
		return []byte(fmt.Sprintf(`{"fqdn":"self.example.net","peers":{"details":[{"fqdn":"peer-a.example.net","netbirdIp":"100.64.0.10","transferReceived":%d,"transferSent":10}]}}`, rx)), nil
	})
	a.SetXrayExec(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		v := 1000 + xrN*100
		xrN++
		return []byte(fmt.Sprintf(`{"stat":[{"name":"inbound>>>in-1>>>traffic>>>uplink","value":"%d"},{"name":"inbound>>>in-1>>>traffic>>>downlink","value":"%d"}]}`, v, v*3)), nil
	})

	// 两拍:第一拍建基线(netbird/xray)+读日志;第二拍出增量
	if _, err := a.Tick(ctx); err != nil {
		t.Fatal("tick1", err)
	}
	if _, err := a.Flush(ctx, 10); err != nil {
		t.Fatal("flush1", err)
	}
	clk = clk.Add(time.Minute)
	if _, err := a.Tick(ctx); err != nil {
		t.Fatal("tick2", err)
	}
	if _, err := a.Flush(ctx, 10); err != nil {
		t.Fatal("flush2", err)
	}

	req := func(path string) string {
		r, _ := http.NewRequest("GET", ts.URL+path, nil)
		r.Header.Set("Authorization", "Bearer panel-token-for-tests-only")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s %d %s", path, resp.StatusCode, b)
		}
		return string(b)
	}
	sites := req("/api/v2/analytics/rank?group=site&period=custom&start=2026-11-11&end=2026-11-11")
	if !strings.Contains(sites, "media.example.com") {
		t.Fatalf("sites: %s", sites)
	}
	nb := req("/api/v2/netbird?period=custom&start=2026-11-11&end=2026-11-11")
	if !strings.Contains(nb, "peer-a.example.net") {
		t.Fatalf("netbird: %s", nb)
	}
	xr := req("/api/v2/xray?period=custom&start=2026-11-11&end=2026-11-11")
	if !strings.Contains(xr, "in-1") {
		t.Fatalf("xray: %s", xr)
	}
	col := req("/api/v2/collectors")
	if !strings.Contains(col, `"status":"enabled"`) || !strings.Contains(col, "site") {
		t.Fatalf("collectors: %s", col)
	}
	var cs struct {
		Hosts []struct {
			Name       string `json:"name"`
			Collectors []struct{ ID, Status string }
		}
	}
	if err := json.Unmarshal([]byte(col), &cs); err != nil {
		t.Fatal(err)
	}
	_ = proto.KindSite
}

func cfgFinish(c *server.Config) error {
	// LoadConfig 需要文件;直接调用未导出 finish。用 JSON 绕一下。
	p := filepath.Join(os.TempDir(), "ts-e2e-cfg.json")
	b, _ := json.Marshal(map[string]any{"listen": c.Listen, "db_path": c.DBPath, "tz": c.TZ, "reset_day": c.ResetDay})
	os.WriteFile(p, b, 0o600)
	got, err := server.LoadConfig(p)
	if err != nil {
		return err
	}
	*c = *got
	return nil
}
