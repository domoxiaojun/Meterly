package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
)

func TestEnrollRedeemAndExistingTokenUntouched(t *testing.T) {
	s, st, _, tok := setup(t, "127.0.0.1:0")
	h := s.Handler()
	now := time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }

	code, _, err := st.CreateEnroll(context.Background(), "h1", store.EnrollOpts{TTL: time.Hour}, now)
	if err != nil {
		t.Fatal(err)
	}
	rr := do(h, "POST", "/api/v2/enroll/redeem", "", map[string]string{"code": code})
	if rr.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d %s", rr.Code, rr.Body.Bytes())
	}
	if _, err := st.HostByToken(context.Background(), tok); err != nil {
		t.Fatal("manual token must survive", err)
	}

	code2, _, err := st.CreateEnroll(context.Background(), "box-new", store.EnrollOpts{TTL: time.Hour}, now)
	if err != nil {
		t.Fatal(err)
	}
	rr = do(h, "POST", "/api/v2/enroll/redeem", "", map[string]string{"code": code2, "hostname": "box-new"})
	if rr.Code != 200 {
		t.Fatalf("redeem %d %s", rr.Code, rr.Body.Bytes())
	}
	var out struct{ Token, Host string }
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.Token == "" || out.Host != "box-new" {
		t.Fatalf("body %s", rr.Body.Bytes())
	}
	if _, err := st.HostByToken(context.Background(), out.Token); err != nil {
		t.Fatal(err)
	}
	rr = do(h, "POST", "/api/v2/enroll/redeem", "", map[string]string{"code": code2})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("reuse %d", rr.Code)
	}
	rr = do(h, "POST", "/api/v2/enroll", "", map[string]any{"host_name": "need-login"})
	if rr.Code != 401 {
		t.Fatalf("unauth create %d", rr.Code)
	}
	rr = doJSON(h, "POST", "/api/v2/enroll", `{"host_name":"box-panel","ttl_hours":1,"max_uses":1}`, "application/json")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"code":"tse_`) {
		t.Fatalf("panel create %d %s", rr.Code, rr.Body.Bytes())
	}
}

func TestSitesNetbirdXrayAPI(t *testing.T) {
	s, st, _, tok := setup(t, "127.0.0.1:0")
	ctx := context.Background()
	h, err := st.HostByToken(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.FixedZone("SGT", 8*3600))
	s.Now = func() time.Time { return now }
	ts := now.Add(-2 * time.Hour).Unix()
	recs := []proto.Record{
		{Seq: 1, TS: ts + 60, PrevTS: ts, Kind: proto.KindSite, Dim1: "funmedia.example", Dim2: "192.0.2.1", TX: 8000},
		{Seq: 2, TS: ts + 60, PrevTS: ts, Kind: proto.KindSiteReq, Dim1: "funmedia.example", Dim2: "2xx", RX: 3},
		{Seq: 3, TS: ts + 60, PrevTS: ts, Kind: proto.KindNetbird, Dim1: "peer-a.example.net", Dim2: "100.64.0.10", RX: 1000, TX: 10},
		{Seq: 4, TS: ts + 60, PrevTS: ts, Kind: proto.KindXray, Dim1: "inbound-20383", RX: 20, TX: 80},
	}
	if _, err := st.Ingest(ctx, h, time.UTC, proto.Meta{Protocol: proto.Version, InstanceID: "i", Collectors: map[string]proto.CollectorHealth{
		"netdev":  {Enabled: true, LastOK: now.Unix()},
		"singbox": {Enabled: true, LastOK: now.Unix()},
		"site":    {Enabled: true, LastOK: now.Unix()},
	}}, recs, "127.0.0.1", now); err != nil {
		t.Fatal(err)
	}
	hh := s.Handler()
	rr := do(hh, "GET", "/api/v2/analytics/rank?group=site&period=current", panelTok, nil)
	if rr.Code != 200 {
		t.Fatalf("sites %d %s", rr.Code, rr.Body.Bytes())
	}
	if !strings.Contains(rr.Body.String(), "funmedia.example") {
		t.Fatalf("sites body %s", rr.Body.Bytes())
	}
	rr = do(hh, "GET", "/api/v2/netbird?period=current", panelTok, nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "peer-a.example.net") {
		t.Fatalf("netbird %d %s", rr.Code, rr.Body.Bytes())
	}
	rr = do(hh, "GET", "/api/v2/xray?period=current", panelTok, nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "inbound-20383") || !strings.Contains(rr.Body.String(), "没有来源 IP") {
		t.Fatalf("xray %d %s", rr.Code, rr.Body.Bytes())
	}
	rr = do(hh, "GET", "/api/v2/collectors", panelTok, nil)
	if rr.Code != 200 {
		t.Fatalf("collectors %d %s", rr.Code, rr.Body.Bytes())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"status":"enabled"`) {
		t.Fatalf("collectors missing enabled: %s", body)
	}
	if strings.Contains(body, `"id":"xray","label":"xray","status":"enabled"`) {
		t.Fatal("xray should be 未启用/disabled, not enabled")
	}
	if !strings.Contains(body, `"id":"xray"`) || !strings.Contains(body, `"status":"disabled"`) {
		t.Fatalf("xray should appear as disabled: %s", body)
	}
	rr = do(hh, "GET", "/api/v2/reconcile?period=current", panelTok, nil)
	if rr.Code != 200 {
		t.Fatalf("recon %d %s", rr.Code, rr.Body.Bytes())
	}
	if !strings.Contains(rr.Body.String(), `"tx":8000`) && !strings.Contains(rr.Body.String(), `"site":{"rx":0,"tx":8000}`) {
		// site should be filled
		if !strings.Contains(rr.Body.String(), `"tx":8000`) {
			t.Fatalf("recon missing site bytes: %s", rr.Body.Bytes())
		}
	}

	// 第二台主机:host=all 不得再把 IN 子句写成 ((?,?)) (SQLite: row value misused)
	tok2, err := st.CreateHost(ctx, "h2", store.HostOpts{})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := st.HostByToken(ctx, tok2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ingest(ctx, h2, time.UTC, proto.Meta{Protocol: proto.Version, InstanceID: "i2"}, []proto.Record{
		{Seq: 1, TS: ts + 60, PrevTS: ts, Kind: proto.KindSite, Dim1: "git.example", Dim2: "192.0.2.2", TX: 1500},
		{Seq: 2, TS: ts + 60, PrevTS: ts, Kind: proto.KindNetbird, Dim1: "peer-b.example.net", Dim2: "100.66.1.2", RX: 50, TX: 5},
	}, "127.0.0.1", now); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v2/analytics/rank?group=site&host=all&period=current", "/api/v2/netbird?host=all&period=current", "/api/v2/xray?host=all&period=current"} {
		rr = do(hh, "GET", path, panelTok, nil)
		if rr.Code != 200 {
			t.Fatalf("%s %d %s", path, rr.Code, rr.Body.Bytes())
		}
	}
	rr = do(hh, "GET", "/api/v2/analytics/rank?group=site&host=all&period=current", panelTok, nil)
	if !strings.Contains(rr.Body.String(), "funmedia.example") || !strings.Contains(rr.Body.String(), "git.example") {
		t.Fatalf("sites all missing both hosts: %s", rr.Body.Bytes())
	}
	rr = do(hh, "GET", "/api/v2/netbird?host=all&period=current", panelTok, nil)
	if !strings.Contains(rr.Body.String(), "peer-a.example.net") || !strings.Contains(rr.Body.String(), "peer-b.example.net") {
		t.Fatalf("netbird all missing both peers: %s", rr.Body.Bytes())
	}
}
