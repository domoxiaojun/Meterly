package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
)

func TestAnalysisDatesAndCSV(t *testing.T) {
	s, st, _, tok := setup(t, "127.0.0.1:8787")
	ctx := context.Background()
	host, _ := st.HostByToken(ctx, tok)
	now := time.Date(2026, 11, 11, 12, 30, 37, 0, s.cfg.loc)
	s.Now = func() time.Time { return now }
	cases := []struct {
		q        string
		from, to time.Time
	}{
		{"period=day&start=2026-10-31", time.Date(2026, 10, 31, 0, 0, 0, 0, s.cfg.loc), time.Date(2026, 11, 1, 0, 0, 0, 0, s.cfg.loc)},
		{"period=custom&start=2026-10-31&end=2026-11-02", time.Date(2026, 10, 31, 0, 0, 0, 0, s.cfg.loc), time.Date(2026, 11, 3, 0, 0, 0, 0, s.cfg.loc)},
		{"period=last1h", now.Truncate(time.Minute).Add(-time.Hour), now.Truncate(time.Minute)},
		{"period=yesterday", time.Date(2026, 11, 10, 0, 0, 0, 0, s.cfg.loc), time.Date(2026, 11, 11, 0, 0, 0, 0, s.cfg.loc)},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/?"+c.q, nil)
		a, b, e := s.analysisPeriod(r, now)
		if e != nil || a != c.from.Unix() || b != c.to.Unix() {
			t.Fatalf("%s: %d,%d %v", c.q, a, b, e)
		}
	}
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, s.cfg.loc).Unix()
	records := []proto.Record{{Seq: 1, TS: start + 60, Kind: "site", Dim1: "a.test", Dim2: "1.2.3.4", TX: 123, Site: &proto.SiteMetrics{Status: 200, Requests: 1}}, {Seq: 2, TS: start + 3660, Kind: "site", Dim1: "a.test", Dim2: "1.2.3.4", TX: 77, Site: &proto.SiteMetrics{Status: 503, Requests: 1}}}
	if _, e := st.Ingest(ctx, host, time.UTC, proto.Meta{InstanceID: "test", Protocol: 2}, records, "", now); e != nil {
		t.Fatal(e)
	}
	q := "?period=day&start=2026-11-10&site=a.test&ip=1.2.3.4&status=503"
	w := do(s.Handler(), "GET", "/api/v2/analytics/rank"+q, panelTok, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var d struct {
		Totals store.Metrics
		Count  int64
		Meta   analysisMeta
	}
	json.Unmarshal(w.Body.Bytes(), &d)
	if d.Count != 1 || d.Totals.TX != 77 || d.Totals.Requests != 1 || d.Totals.RXKnown {
		t.Fatalf("%+v", d)
	}
	w = do(s.Handler(), "GET", "/api/v2/analytics/export"+q, panelTok, nil)
	rows, e := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if e != nil || len(rows) != 2 || rows[1][2] != "77" {
		t.Fatalf("csv %v %v", rows, e)
	}
	w = do(s.Handler(), "GET", "/api/v2/analytics/rank?period=day&start=2025-01-01", panelTok, nil)
	json.Unmarshal(w.Body.Bytes(), &d)
	if d.Meta.State != "expired" {
		t.Fatal(w.Body)
	}
	w = do(s.Handler(), "GET", "/api/v2/overview?period=today", panelTok, nil)
	var ov struct {
		PreviousFrom int64 `json:"previous_from"`
		PreviousTo   int64 `json:"previous_to"`
		Meta         analysisMeta
	}
	json.Unmarshal(w.Body.Bytes(), &ov)
	if ov.PreviousTo-ov.PreviousFrom != ov.Meta.To-ov.Meta.From || ov.Meta.From-ov.PreviousFrom != 86400 {
		t.Fatal("comparison not equal elapsed", w.Body)
	}
}
func TestControlCASAndUpgradeAcknowledgement(t *testing.T) {
	s, st, _, tok := setup(t, "127.0.0.1:8787")
	h, _ := st.HostByToken(context.Background(), tok)
	c := proto.DesiredConfig{Version: 0, Policy: proto.Policy{Collectors: map[string]bool{"site": false}}}
	w := do(s.Handler(), "PUT", "/api/v2/hosts/h1/config", panelTok, c)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w = do(s.Handler(), "PUT", "/api/v2/hosts/h1/config", panelTok, c); w.Code != 409 {
		t.Fatal("expected version conflict", w.Body)
	}
	w = do(s.Handler(), "GET", "/api/v2/agent/control", tok, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":1`) {
		t.Fatal(w.Body)
	}
	id, e := st.CreateUpgrade(context.Background(), h.ID, proto.UpgradeTask{Version: "v2.0.0", CreatedAt: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = st.UpgradeStatus(context.Background(), h.ID, id, "succeeded", "ready"); e != nil {
			t.Fatal("repeat ack", e)
		}
	}
}

func TestUnknownWebsiteIsNotExactZero(t *testing.T) {
	s, st, _, tok := setup(t, "127.0.0.1:8787")
	ctx := context.Background()
	h, _ := st.HostByToken(ctx, tok)
	now := s.Now().Unix()
	_, e := st.Ingest(ctx, h, time.UTC, proto.Meta{InstanceID: "i", Collectors: map[string]proto.CollectorHealth{"site": {Enabled: true, LastOK: now}}}, []proto.Record{{Seq: 1, TS: now - 3600, Kind: "site", Dim1: "known.test", Dim2: "1.2.3.4", TX: 1, Site: &proto.SiteMetrics{Requests: 1, Status: 200}}}, "", s.Now())
	if e != nil {
		t.Fatal(e)
	}
	w := do(s.Handler(), "GET", "/api/v2/analytics/rank?site=not-discovered.test&period=last1h", panelTok, nil)
	var d struct{ Meta analysisMeta }
	if e = json.Unmarshal(w.Body.Bytes(), &d); e != nil || d.Meta.State != "not_collected" {
		t.Fatalf("uncollected website presented as zero: %s", w.Body)
	}
}
