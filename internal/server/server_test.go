package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
)

const panelTok = "panel-token-for-tests-only"

func setup(t *testing.T, listen string) (*Server, *store.Store, *bytes.Buffer, string) {
	t.Helper()
	t.Setenv("METERLY_PANEL_TOKEN", panelTok)
	cfg := &Config{Listen: listen}
	if err := cfg.finish(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tok, _ := st.CreateHost(context.Background(), "h1", store.HostOpts{QuotaBytes: 1 << 30})
	logs := &bytes.Buffer{}
	return New(cfg, st, nil, slog.New(slog.NewTextHandler(logs, nil))), st, logs, tok
}

func do(h http.Handler, method, path, auth string, body any) *httptest.ResponseRecorder {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestAuthSeparation(t *testing.T) {
	s, _, logs, hostTok := setup(t, "127.0.0.1:8787")
	h := s.Handler()
	batch := proto.Batch{Meta: proto.Meta{Protocol: proto.Version, InstanceID: "x"}, Records: []proto.Record{{Seq: 1, TS: time.Now().Unix(), Kind: "iface", Dim1: "eth0", RX: 1, TX: 1}}}
	if w := do(h, "POST", "/api/v2/ingest", hostTok, batch); w.Code != 200 {
		t.Fatalf("host token ingest: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/v2/ingest", panelTok, batch); w.Code != 401 {
		t.Fatalf("面板口令不能用于 ingest: %d", w.Code)
	}
	if w := do(h, "GET", "/api/v2/overview", hostTok, nil); w.Code != 401 {
		t.Fatalf("host token 不能访问面板: %d", w.Code)
	}
	if w := do(h, "GET", "/api/v2/overview", "", nil); w.Code != 401 {
		t.Fatalf("未登录应 401: %d", w.Code)
	}
	w := do(h, "GET", "/api/v2/overview", panelTok, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"host_count":1`) {
		t.Fatalf("面板口令访问: %d %s", w.Code, w.Body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Fatal("缺少 CSP")
	}
	// 登录 -> 会话 Cookie
	w = do(h, "POST", "/api/login", "", map[string]string{"token": panelTok})
	c := w.Result().Cookies()
	if w.Code != 200 || len(c) == 0 || !c[0].HttpOnly || strings.Contains(c[0].Value, panelTok) {
		t.Fatalf("登录 Cookie 异常: %d %+v", w.Code, c)
	}
	req := httptest.NewRequest("GET", "/api/v2/me", nil)
	req.AddCookie(c[0])
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)
	if rw.Code != 200 {
		t.Fatalf("Cookie 访问失败: %d", rw.Code)
	}
	// 篡改 Cookie
	req = httptest.NewRequest("GET", "/api/v2/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "9999999999.deadbeef"})
	rw = httptest.NewRecorder()
	h.ServeHTTP(rw, req)
	if rw.Code != 401 {
		t.Fatal("伪造 Cookie 应被拒")
	}
	do(h, "POST", "/api/login", "", map[string]string{"token": "wrong-token-xxxxxxxx"})
	if strings.Contains(logs.String(), hostTok) || strings.Contains(logs.String(), panelTok) || strings.Contains(logs.String(), "wrong-token") {
		t.Fatal("日志中出现了 token")
	}
}

func TestRevokedAndRateLimit(t *testing.T) {
	s, st, _, hostTok := setup(t, "127.0.0.1:8787")
	h := s.Handler()
	st.RevokeHost(context.Background(), "h1")
	if w := do(h, "POST", "/api/v2/ingest", hostTok, proto.Batch{}); w.Code != 401 {
		t.Fatalf("吊销后应 401: %d", w.Code)
	}
	// 错误 token 连续尝试触发限速
	limited := false
	for i := 0; i < 40; i++ {
		if w := do(h, "POST", "/api/v2/ingest", "bad", proto.Batch{}); w.Code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("鉴权失败应被限速")
	}
}

func TestOversizedBatch(t *testing.T) {
	s, _, _, hostTok := setup(t, "127.0.0.1:8787")
	recs := make([]proto.Record, proto.MaxRecordsPerBatch+1)
	if w := do(s.Handler(), "POST", "/api/v2/ingest", hostTok, proto.Batch{Records: recs}); w.Code != 413 {
		t.Fatalf("超大批次应 413: %d", w.Code)
	}
}

func TestNonLoopbackRequiresPanelToken(t *testing.T) {
	t.Setenv("METERLY_PANEL_TOKEN", "")
	if err := (&Config{Listen: "0.0.0.0:8787"}).finish(); err == nil {
		t.Fatal("非回环监听且无面板口令应拒绝启动")
	}
	if err := (&Config{Listen: "127.0.0.1:8787"}).finish(); err != nil {
		t.Fatal(err)
	}
}

func TestOverviewBillingAndPeriod(t *testing.T) {
	s, st, _, hostTok := setup(t, "127.0.0.1:8787")
	st.UpdateHost(context.Background(), "h1", store.HostOpts{BillingMode: "tx"})
	now := time.Date(2026, 11, 11, 4, 0, 0, 0, time.UTC) // 12:00 SGT
	s.Now = func() time.Time { return now }
	// 周期边界两侧各一条:11-08 23:59 SGT(上期)与 11-09 00:01 SGT(本期)
	b1 := time.Date(2026, 11, 8, 15, 59, 30, 0, time.UTC).Unix()
	b2 := time.Date(2026, 11, 8, 16, 1, 0, 0, time.UTC).Unix()
	batch := proto.Batch{Meta: proto.Meta{Protocol: proto.Version, InstanceID: "x"}, Records: []proto.Record{
		{Seq: 1, TS: b1, Kind: "iface", Dim1: "eth0", RX: 100, TX: 1000},
		{Seq: 2, TS: b2, Kind: "iface", Dim1: "eth0", RX: 7, TX: 70},
	}}
	if w := do(s.Handler(), "POST", "/api/v2/ingest", hostTok, batch); w.Code != 200 {
		t.Fatal(w.Body)
	}
	var ov struct {
		Hosts []struct {
			Billed int64 `json:"billed"`
			Period struct{ Start, End string }
		}
	}
	w := do(s.Handler(), "GET", "/api/v2/billing/overview?period=current", panelTok, nil)
	json.Unmarshal(w.Body.Bytes(), &ov)
	if ov.Hosts[0].Billed != 70 || ov.Hosts[0].Period.Start != "2026-11-09" || ov.Hosts[0].Period.End != "2026-12-08" {
		t.Fatalf("本期(仅出站)应为 70: %s", w.Body)
	}
	w = do(s.Handler(), "GET", "/api/v2/billing/overview?period=previous", panelTok, nil)
	json.Unmarshal(w.Body.Bytes(), &ov)
	if ov.Hosts[0].Billed != 1000 {
		t.Fatalf("上期应为 1000: %s", w.Body)
	}
}
