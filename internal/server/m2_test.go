package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/cycle"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
)

// setupM2 带测试归属地库的 server;时间固定在 2026-11-11 12:30 SGT。
func setupM2(t *testing.T) (*Server, *store.Store, *store.Host, time.Time) {
	t.Helper()
	t.Setenv("METERLY_PANEL_TOKEN", panelTok)
	gdir := t.TempDir()
	for _, f := range []string{"dbip-city-lite.mmdb", "dbip-asn-lite.mmdb"} {
		b, err := os.ReadFile(filepath.Join("..", "geo", "testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(gdir, f), b, 0o600)
	}
	cfg := &Config{Listen: "127.0.0.1:8787", Geo: GeoConfig{Dir: gdir}}
	if err := cfg.finish(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	tok, _ := st.CreateHost(ctx, "host-a", store.HostOpts{BillingMode: "both"})
	h, _ := st.HostByToken(ctx, tok)
	now := time.Date(2026, 11, 11, 12, 30, 0, 0, cycle.SGT)
	s := New(cfg, st, nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	s.Now = func() time.Time { return now }
	// 数据:今天 10:00~12:29 每分钟 eth0 rx=600 tx=400;sing-box 两个 IP + 未归因
	var recs []proto.Record
	seq := int64(0)
	start := time.Date(2026, 11, 11, 10, 0, 0, 0, cycle.SGT).Unix()
	for ts := start + 60; ts <= now.Unix()-60; ts += 60 {
		seq++
		recs = append(recs, proto.Record{Seq: seq, TS: ts, PrevTS: ts - 60, Kind: "iface", Dim1: "eth0", RX: 600, TX: 400})
		seq++
		recs = append(recs, proto.Record{Seq: seq, TS: ts, Kind: "singbox", Dim1: "198.51.100.7", Dim2: "tuic/tuic-in", RX: 100, TX: 100})
		seq++
		recs = append(recs, proto.Record{Seq: seq, TS: ts, Kind: "singbox", Dim1: "192.0.2.5", Dim2: "anytls/anytls-in", RX: 25, TX: 25})
		seq++
		recs = append(recs, proto.Record{Seq: seq, TS: ts, Kind: "singbox", Dim1: "10.0.0.8", Dim2: "direct/relay-in", RX: 10, TX: 0})
		seq++
		recs = append(recs, proto.Record{Seq: seq, TS: ts, Kind: "singbox", Dim1: proto.DimUnattributed, RX: 5, TX: 5})
	}
	for i := 0; i < len(recs); i += 4000 {
		if _, err := st.Ingest(ctx, h, cycle.SGT, proto.Meta{InstanceID: "i"}, recs[i:min(i+4000, len(recs))], "", now); err != nil {
			t.Fatal(err)
		}
	}
	return s, st, h, now
}

func getJSON(t *testing.T, h http.Handler, path string, v any) int {
	t.Helper()
	w := do(h, "GET", path, panelTok, nil)
	if w.Code == 200 && v != nil {
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code
}

func TestTopIPsWithGeoAndSummary(t *testing.T) {
	s, _, _, _ := setupM2(t)
	h := s.Handler()
	var top struct {
		Rows []ipRowGeo `json:"rows"`
	}
	if c := getJSON(t, h, "/api/v1/singbox/top?host=host-a", &top); c != 200 {
		t.Fatal(c)
	}
	geoOf := map[string]string{}
	for _, r := range top.Rows {
		geoOf[r.IP] = r.Geo.CC + "|" + r.Geo.Scope + "|" + r.Geo.Org
	}
	if geoOf["198.51.100.7"] != "CN||Example Mobile CN" || geoOf["192.0.2.5"] != "JP||Example Telecom JP" || geoOf["10.0.0.8"] != "|内网|" {
		t.Fatalf("归属地 %v", geoOf)
	}
	for _, by := range []string{"country", "asn"} {
		var g struct {
			Rows  []geoRow   `json:"rows"`
			Total store.Pair `json:"total"`
		}
		if c := getJSON(t, h, "/api/v1/singbox/geo?host=host-a&by="+by, &g); c != 200 {
			t.Fatal(by, c)
		}
		var sum int64
		for _, r := range g.Rows {
			sum += r.Total
		}
		if sum != g.Total.Total() || len(g.Rows) != 4 {
			t.Fatalf("%s 汇总之和 %d ≠ 总量 %d, rows=%+v", by, sum, g.Total.Total(), g.Rows)
		}
		if g.Rows[0].Key != map[string]string{"country": "CN", "asn": "AS64501"}[by] || g.Rows[len(g.Rows)-1].Special != "unattributed" {
			t.Fatalf("%s 排序 %+v", by, g.Rows)
		}
	}
	if c := getJSON(t, h, "/api/v1/singbox/geo?by=city", nil); c != 400 {
		t.Fatal("by 校验", c)
	}
}

func TestIPDetailAndLast1h(t *testing.T) {
	s, _, _, now := setupM2(t)
	h := s.Handler()
	var d struct {
		Gran     string                `json:"gran"`
		Points   []store.Point         `json:"points"`
		Inbounds []store.InboundShare  `json:"inbounds"`
		Total    store.Pair            `json:"total"`
		Geo      struct{ City string } `json:"geo"`
	}
	if c := getJSON(t, h, "/api/v1/singbox/ip?host=host-a&ip=198.51.100.7", &d); c != 200 {
		t.Fatal(c)
	}
	// 本期(11-09 起)按小时,10/11/12 点有数据
	nonzero := 0
	for _, p := range d.Points {
		if p.Total() > 0 {
			nonzero++
		}
	}
	if d.Gran != "hour" || nonzero != 3 || d.Geo.City != "Shenzhen" || len(d.Inbounds) != 1 || d.Total.RX != 149*100 {
		t.Fatalf("明细 %+v", d)
	}
	// 最近 1 小时:分钟粒度,60 个点
	if c := getJSON(t, h, "/api/v1/singbox/ip?host=host-a&ip=198.51.100.7&period=last1h", &d); c != 200 {
		t.Fatal(c)
	}
	if d.Gran != "minute" || len(d.Points) != 60 || d.Points[len(d.Points)-1].TS != now.Unix()/60*60 {
		t.Fatalf("last1h %s %d", d.Gran, len(d.Points))
	}
	for _, bad := range []string{"~other", "1.2.3", "::ffff:198.51.100.7"} {
		if c := getJSON(t, h, "/api/v1/singbox/ip?ip="+bad, nil); c != 400 {
			t.Fatalf("%s: %d", bad, c)
		}
	}
	// 入站小时曲线(dim2 过滤)
	var sr struct {
		Points []store.Point `json:"points"`
	}
	getJSON(t, h, "/api/v1/series?host=host-a&kind=singbox&dim2=anytls/anytls-in&gran=hour", &sr)
	var sum int64
	for _, p := range sr.Points {
		sum += p.Total()
	}
	if sum != 149*50 {
		t.Fatalf("入站曲线合计 %d", sum)
	}
	// 网卡分钟曲线 + last1h 排行用分钟表
	getJSON(t, h, "/api/v1/series?host=host-a&period=last1h&gran=minute", &sr)
	if len(sr.Points) != 60 || sr.Points[30].RX != 600 {
		t.Fatalf("分钟曲线 %d %+v", len(sr.Points), sr.Points[30])
	}
	if c := getJSON(t, h, "/api/v1/series?host=host-a&gran=minute", nil); c != 400 {
		t.Fatal("本期 + 分钟粒度应拒绝")
	}
	var top struct {
		Total store.Pair `json:"total"`
	}
	getJSON(t, h, "/api/v1/singbox/top?host=host-a&period=last1h", &top)
	if top.Total.Total() != 58*(200+50+10+10) {
		t.Fatalf("last1h 排行合计 %d", top.Total.Total())
	}
}

func TestReconcile(t *testing.T) {
	s, _, _, _ := setupM2(t)
	h := s.Handler()
	var rc struct {
		Rows []reconRow `json:"rows"`
	}
	if c := getJSON(t, h, "/api/v1/reconcile?host=host-a", &rc); c != 200 || len(rc.Rows) != 1 {
		t.Fatal(c)
	}
	r := rc.Rows[0]
	n := int64(149)
	if r.IfaceTotal != n*1000 || r.Singbox.Total() != n*270 || r.SingboxEst != n*540 || r.Unattributed != n*460 || r.Site != nil || r.Netbird != nil {
		t.Fatalf("对账 %+v", r)
	}
	getJSON(t, h, "/api/v1/reconcile?host=host-a&factor=1", &rc)
	if rc.Rows[0].Unattributed != n*730 {
		t.Fatalf("factor=1 %+v", rc.Rows[0])
	}
	if c := getJSON(t, h, "/api/v1/reconcile?factor=9", nil); c != 400 {
		t.Fatal("factor 校验")
	}
}

func doJSON(h http.Handler, method, path string, body string, ctype string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+panelTok)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestProviderReadingsAPI(t *testing.T) {
	s, _, _, _ := setupM2(t)
	h := s.Handler()
	// 非 JSON 拒绝(CSRF 防护)
	if w := doJSON(h, "POST", "/api/v1/provider", `host=host-a`, "application/x-www-form-urlencoded"); w.Code != 415 {
		t.Fatalf("表单提交应 415: %d", w.Code)
	}
	if w := doJSON(h, "POST", "/api/v1/provider", `{"host":"host-a","cycle_start":"2026-11-09","value":"abc"}`, "application/json"); w.Code != 400 {
		t.Fatalf("非法用量: %d", w.Code)
	}
	// 我们记录本期 149×1000 = 149000 字节;服务商显示 150 KB(=150000)
	if w := doJSON(h, "POST", "/api/v1/provider", `{"host":"host-a","cycle_start":"2026-11-09","value":"150 KB","note":"后台"}`, "application/json"); w.Code != 200 {
		t.Fatalf("录入: %d %s", w.Code, w.Body)
	}
	var pl struct {
		Rows []providerRow `json:"rows"`
	}
	getJSON(t, h, "/api/v1/provider?host=host-a", &pl)
	r := pl.Rows[0]
	if r.CycleStart != "2026-11-09" || r.Ours != 149000 || r.Reading == nil || r.Reading.Bytes != 150000 || r.Diff != -1000 || r.Pct != -0.67 || len(r.History) != 1 {
		t.Fatalf("比对 %+v", r)
	}
	if w := doJSON(h, "DELETE", "/api/v1/provider?host=host-a&cycle_start=2026-11-09", "", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	getJSON(t, h, "/api/v1/provider?host=host-a", &pl)
	if pl.Rows[0].Reading != nil {
		t.Fatal("删除后应为空")
	}
	// 未登录不可写
	req := httptest.NewRequest("POST", "/api/v1/provider", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("未登录应 401: %d", w.Code)
	}
}

func TestVnstatCheckAPI(t *testing.T) {
	s, _, _, _ := setupM2(t)
	h := s.Handler()
	// 10、11 点两个完整小时:我们 60×1000 = 60000;vnstat 10 点一致、11 点多 600(1%);12 点未结束忽略;9 点 agent 未上线
	hr := func(hh int) int64 { return time.Date(2026, 11, 11, hh, 0, 0, 0, cycle.SGT).Unix() }
	vn := map[string]any{"interfaces": []any{map[string]any{"name": "eth0", "traffic": map[string]any{
		"hour": []any{
			map[string]any{"timestamp": hr(9), "rx": 5, "tx": 5},
			map[string]any{"timestamp": hr(10), "rx": 36000, "tx": 24000},
			map[string]any{"timestamp": hr(11), "rx": 36600, "tx": 24000},
			map[string]any{"timestamp": hr(12), "rx": 1, "tx": 1},
		}, "day": []any{}}}}}
	b, _ := json.Marshal(vn)
	w := doJSON(h, "POST", "/api/v1/vnstat/check?host=host-a", string(b), "application/json")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var res struct {
		Results []store.VnstatCheck `json:"results"`
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	c := res.Results[0]
	// 10 点:我们 10:01~10:59 共 59 分钟?—— 数据从 10:01 的样本开始(覆盖 10:00~10:01)
	if c.Total.Hours != 2 || c.OnlyVnstat != 1 {
		t.Fatalf("%+v", c)
	}
	if w := doJSON(h, "POST", "/api/v1/vnstat/check?host=all", string(b), "application/json"); w.Code != 404 {
		t.Fatalf("必须指定主机: %d", w.Code)
	}
	if w := doJSON(h, "POST", "/api/v1/vnstat/check?host=host-a", "not json", "application/json"); w.Code != 400 {
		t.Fatalf("非法 JSON: %d", w.Code)
	}
}

func TestHealthzAndTrustedProxyCIDR(t *testing.T) {
	s, st, _, _ := setupM2(t)
	h := s.Handler()
	if w := do(h, "GET", "/healthz", "", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	cfg := &Config{Listen: "127.0.0.1:1", TrustedProxies: []string{"172.30.87.0/24", "10.0.0.1"}}
	if err := cfg.finish(); err != nil {
		t.Fatal(err)
	}
	s2 := New(cfg, st, nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "172.30.87.10:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	if ip := s2.clientIP(req); ip != "203.0.113.5" {
		t.Fatalf("CIDR 内反代应取 XFF: %s", ip)
	}
	req.RemoteAddr = "172.31.0.1:5555"
	if ip := s2.clientIP(req); ip != "172.31.0.1" {
		t.Fatalf("不在信任列表不取 XFF: %s", ip)
	}
	if err := (&Config{Listen: "127.0.0.1:1", TrustedProxies: []string{"nope"}}).finish(); err == nil {
		t.Fatal("非法 trusted_proxies 应报错")
	}
	st.Close()
	if w := do(h, "GET", "/healthz", "", nil); w.Code != 503 {
		t.Fatalf("数据库不可用应 503: %d", w.Code)
	}
}
