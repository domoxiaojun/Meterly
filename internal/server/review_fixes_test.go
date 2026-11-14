package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
	"testing"
	"time"
)

func TestRegressionDashboardComparisonAcrossMinuteRetention(t *testing.T) {
	s, st, _, tok := setup(t, "127.0.0.1:8787")
	defer s.Close()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, s.cfg.loc)
	s.Now = func() time.Time { return now }
	ctx := context.Background()
	h, _ := st.HostByToken(ctx, tok)
	records := []proto.Record{}
	for i, x := range []struct {
		day, hour int
		bytes     uint64
	}{{9, 8, 1}, {10, 8, 100}, {10, 18, 200}, {11, 8, 300}} {
		records = append(records, proto.Record{Seq: int64(i + 1), TS: time.Date(2026, 11, x.day, x.hour, 1, 0, 0, s.cfg.loc).Unix(), Kind: "site", Dim1: "a.test", Dim2: "192.0.2.1", TX: x.bytes, Site: &proto.SiteMetrics{Status: 200, Requests: 1}})
	}
	meta := proto.Meta{Protocol: 2, InstanceID: "audit", Collectors: map[string]proto.CollectorHealth{"site": {Enabled: true, LastOK: now.Unix()}}}
	if _, e := st.Ingest(ctx, h, s.cfg.loc, meta, records, "", now); e != nil {
		t.Fatal(e)
	}
	if _, e := st.Maintain(ctx, s.cfg.Retention, now); e != nil {
		t.Fatal(e)
	}
	w := do(s.Handler(), "GET", "/api/v2/overview?period=yesterday", panelTok, nil)
	var d struct {
		Site     store.Metrics
		Previous store.Metrics
		Growth   *float64 `json:"growth_pct"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &d) != nil {
		t.Fatal(w.Code, w.Body)
	}
	var growth any
	if d.Growth != nil {
		growth = *d.Growth
	}
	t.Logf("actual: current=%d previous=%d growth=%v; expected previous=300 and growth=0%%", d.Site.Total, d.Previous.Total, growth)
	if d.Previous.Total != 300 || d.Growth == nil || *d.Growth != 0 {
		t.Fatal("comparison reused minute table despite expired previous-period minute rows")
	}
}

func TestRegressionSuccessfulHeartbeatDoesNotInventGap(t *testing.T) {
	s, st, _, tok := setup(t, "127.0.0.1:8787")
	defer s.Close()
	ctx := context.Background()
	h, _ := st.HostByToken(ctx, tok)
	now := time.Now().Truncate(time.Minute)
	meta := proto.Meta{Protocol: 2, InstanceID: "audit", Collectors: map[string]proto.CollectorHealth{"site": {Enabled: true, Fails: 0, LastOK: now.Unix(), LastErr: "old error", ErrAt: now.Add(-time.Hour).Unix()}}}
	if _, e := st.Ingest(ctx, h, s.cfg.loc, meta, nil, "", now); e != nil {
		t.Fatal(e)
	}
	issues, e := st.DataIssues(ctx, store.AnalysisFilter{HostIDs: []int64{h.ID}, From: now.Add(-time.Minute).Unix(), To: now.Unix()})
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("healthy heartbeat produced current issues: %v", issues)
	if len(issues) > 0 {
		t.Fatal("historical error was stamped into current time window")
	}
}

func TestRegressionAggregateOverflowCannotPoisonPanel(t *testing.T) {
	s, _, _, tok := setup(t, "127.0.0.1:8787")
	defer s.Close()
	now := time.Now().Truncate(time.Minute)
	s.Now = func() time.Time { return now }
	records := make([]proto.Record, 4097)
	for i := range records {
		records[i] = proto.Record{Seq: int64(i + 1), TS: now.Unix() - 60, Kind: "site", Dim1: "overflow.test", Dim2: "192.0.2.1", RX: (1 << 50) - 1, TX: (1 << 50) - 1, Site: &proto.SiteMetrics{Status: 200, Requests: 1, RXKnown: true}}
	}
	batch := proto.Batch{Meta: proto.Meta{Protocol: 2, InstanceID: "audit-overflow"}, Records: records}
	w := do(s.Handler(), "POST", "/api/v2/ingest", tok, batch)
	if w.Code != 200 {
		t.Logf("rejected with %d", w.Code)
		return
	}
	q := fmt.Sprintf("/api/v2/analytics/rank?from=%d&to=%d&group=ip", now.Unix()-3600, now.Unix())
	w = do(s.Handler(), "GET", q, panelTok, nil)
	var d struct{ Totals store.Metrics }
	json.Unmarshal(w.Body.Bytes(), &d)
	t.Logf("first valid-size batch: HTTP %d total=%d", w.Code, d.Totals.Total)
	for i := range batch.Records {
		batch.Records[i].Seq += 4097
	}
	w = do(s.Handler(), "POST", "/api/v2/ingest", tok, batch)
	t.Logf("second valid-size batch: ingest HTTP %d", w.Code)
	w = do(s.Handler(), "GET", q, panelTok, nil)
	t.Logf("after second batch: analytics HTTP %d body=%s", w.Code, w.Body.String())
	if d.Totals.Total < 0 || w.Code != 200 {
		t.Fatal("agent-supplied allowed counters overflow aggregate and break panel analytics")
	}
}

func TestRegressionOldUpgradeDoesNotBlockAllFutureTasks(t *testing.T) {
	s, st, _, tok := setup(t, "127.0.0.1:8787")
	defer s.Close()
	ctx := context.Background()
	h, _ := st.HostByToken(ctx, tok)
	id, e := st.CreateUpgrade(ctx, h.ID, proto.UpgradeTask{Version: "v2.0.1", CreatedAt: time.Now().Add(-7 * 24 * time.Hour).Unix()})
	if e != nil {
		t.Fatal(e)
	}
	st.UpgradeStatus(ctx, h.ID, id, "running", "updater interrupted")
	next, e := st.NextUpgrade(ctx, h.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = st.CreateUpgrade(ctx, h.ID, proto.UpgradeTask{Version: "v2.0.2", CreatedAt: time.Now().Unix()})
	t.Logf("7-day-old task still returned=%v, replacement blocked=%v", next != nil, e != nil)
	if next != nil && e != nil {
		t.Fatal("no timeout/cancel/recovery path releases one_active_upgrade after interruption")
	}
}

func TestRegressionOfflineThresholdIsRespectedByDashboard(t *testing.T) {
	s, st, _, tok := setup(t, "127.0.0.1:8787")
	defer s.Close()
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)
	s.Now = func() time.Time { return now }
	s.cfg.OfflineAfter = 30
	h, _ := st.HostByToken(ctx, tok)
	st.Ingest(ctx, h, s.cfg.loc, proto.Meta{Protocol: 2, InstanceID: "audit-offline"}, nil, "", now.Add(-10*time.Minute))
	w := do(s.Handler(), "GET", "/api/v2/overview", panelTok, nil)
	var d struct{ Online int }
	json.Unmarshal(w.Body.Bytes(), &d)
	w = do(s.Handler(), "GET", "/api/v2/billing/overview", panelTok, nil)
	var b struct{ Hosts []struct{ Online bool } }
	json.Unmarshal(w.Body.Bytes(), &b)
	t.Logf("offline_after_minutes=30, last heartbeat 10 minutes ago: dashboard_online=%d billing_online=%v", d.Online, b.Hosts[0].Online)
	if d.Online != 1 {
		t.Fatal("dashboard hardcodes 5 minutes and ignores configured threshold")
	}
}

func TestRegressionPartialHourEdges(t *testing.T) {
	s, st, _, tok := setup(t, "127.0.0.1:8787")
	defer s.Close()
	ctx := context.Background()
	h, _ := st.HostByToken(ctx, tok)
	now := time.Date(2026, 11, 12, 8, 30, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	records := []proto.Record{}
	// Outside the selected 07:30–08:30 edges must not leak into the curve.
	for i, x := range []struct {
		minute int
		rx     uint64
	}{{-75, 999}, {-45, 100}, {-15, 200}, {15, 999}} {
		records = append(records, proto.Record{Seq: int64(i + 1), TS: now.Add(time.Duration(x.minute) * time.Minute).Unix(), Kind: "iface", Dim1: "ens3", RX: x.rx})
	}
	if _, e := st.Ingest(ctx, h, s.cfg.loc, proto.Meta{InstanceID: "edges"}, records, "", now.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	w := do(s.Handler(), "GET", "/api/v2/series?period=last1h&kind=iface&dim1=ens3&gran=hour", panelTok, nil)
	var d struct{ Points []store.Point }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &d) != nil {
		t.Fatal(w.Code, w.Body)
	}
	if len(d.Points) != 2 || d.Points[0].RX != 100 || d.Points[1].RX != 200 || d.Points[0].TS%3600 != 0 {
		t.Fatalf("partial hours %+v", d.Points)
	}
}
func TestRegressionOfflineBoundaryAndHostAPI(t *testing.T) {
	s, st, _, tok := setup(t, "127.0.0.1:8787")
	defer s.Close()
	ctx := context.Background()
	h, _ := st.HostByToken(ctx, tok)
	now := time.Now().Truncate(time.Minute)
	s.Now = func() time.Time { return now }
	s.cfg.OfflineAfter = 30
	for _, age := range []time.Duration{10 * time.Minute, 30 * time.Minute} {
		if _, e := st.Ingest(ctx, h, s.cfg.loc, proto.Meta{}, nil, "", now.Add(-age)); e != nil {
			t.Fatal(e)
		}
		w := do(s.Handler(), "GET", "/api/v2/hosts", panelTok, nil)
		var d struct {
			Hosts []struct {
				Online bool
				Alerts []struct{ Kind string }
			}
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &d) != nil {
			t.Fatal(w.Code, w.Body)
		}
		offline := false
		for _, a := range d.Hosts[0].Alerts {
			offline = offline || a.Kind == "offline"
		}
		if d.Hosts[0].Online != (age < 30*time.Minute) || offline != (age >= 30*time.Minute) {
			t.Fatal(w.Body)
		}
	}
}
