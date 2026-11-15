package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

func TestSiteBacklogCoverageAndTransientIssues(t *testing.T) {
	s := open(t)
	h := host(t, s, "backlog")
	healthy := host(t, s, "healthy")
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)
	meta := proto.Meta{Protocol: 2, InstanceID: "backlog", SentAt: now.Unix(), Inventory: &proto.Inventory{Sources: []proto.Source{{Site: "backlog.test", Available: true}}}}
	if err := json.Unmarshal([]byte(`{"collectors":{"site":{"enabled":true,"last_ok":`+strconv.FormatInt(now.Unix(), 10)+`,"backlogged":2}}}`), &meta); err != nil {
		t.Fatal(err)
	}
	records := []proto.Record{{Seq: 1, TS: now.Add(-time.Hour).Unix(), Kind: "site", Dim1: "backlog.test", Dim2: "192.0.2.1", TX: 100, Site: &proto.SiteMetrics{Status: 200, Requests: 1}}}
	if _, err := s.Ingest(ctx, h, time.UTC, meta, records, "", now); err != nil {
		t.Fatal(err)
	}
	_, last, err := s.Coverage(ctx, []int64{h.ID}, "site")
	if err != nil || last != records[0].TS {
		t.Fatalf("backlog falsely advanced collection coverage last=%d err=%v", last, err)
	}
	_, last, err = s.SourceCoverage(ctx, []int64{h.ID}, "backlog.test")
	if err != nil || last != records[0].TS {
		t.Fatalf("backlog falsely advanced source coverage last=%d err=%v", last, err)
	}
	base := AnalysisFilter{HostIDs: []int64{h.ID}, Source: "site", From: now.Add(-time.Hour).Unix(), To: now.Unix()}
	check := func(f AnalysisFilter, want int) {
		t.Helper()
		issues, err := s.DataIssues(ctx, f)
		if err != nil || len(issues) != want {
			t.Fatalf("filter %+v issues=%v err=%v want count=%d", f, issues, err, want)
		}
	}
	check(base, 1)
	for _, source := range []string{"singbox", "iface"} {
		f := base
		f.Source = source
		check(f, 0)
	}
	f := base
	f.HostIDs = []int64{healthy.ID}
	check(f, 0)
	f.HostIDs = []int64{healthy.ID, h.ID}
	check(f, 1)
	if _, err = s.W.ExecContext(ctx, "INSERT INTO data_issues(host_id,from_ts,to_ts,reason) VALUES(?,?,?,?)", h.ID, now.Add(-30*time.Minute).Unix(), now.Add(-29*time.Minute).Unix(), "real historical loss"); err != nil {
		t.Fatal(err)
	}
	check(base, 2)
	// Old agents omitting the new field remain accepted and have zero backlog.
	var caughtUp proto.CollectorHealth
	if err = json.Unmarshal([]byte(`{"enabled":true,"last_ok":`+strconv.FormatInt(now.Unix(), 10)+`}`), &caughtUp); err != nil {
		t.Fatal(err)
	}
	meta.Collectors["site"] = caughtUp
	if _, err = s.Ingest(ctx, h, time.UTC, meta, nil, "", now); err != nil {
		t.Fatal(err)
	}
	issues, err := s.DataIssues(ctx, base)
	if err != nil || len(issues) != 1 || !strings.Contains(issues[0], "real historical loss") {
		t.Fatalf("catch-up failed to clear only transient issue: %v %v", issues, err)
	}
	_, last, err = s.Coverage(ctx, []int64{h.ID}, "site")
	if err != nil || last != now.Unix() {
		t.Fatalf("healthy collection coverage not restored: %d %v", last, err)
	}
	_, last, err = s.SourceCoverage(ctx, []int64{h.ID}, "backlog.test")
	if err != nil || last != now.Unix() {
		t.Fatalf("healthy source coverage not restored: %d %v", last, err)
	}
	// Disabled collector with stale backlog must not create a live backlog warning.
	if err = json.Unmarshal([]byte(`{"enabled":false,"last_ok":`+strconv.FormatInt(now.Unix(), 10)+`,"backlogged":2}`), &caughtUp); err != nil {
		t.Fatal(err)
	}
	meta.Collectors["site"] = caughtUp
	if _, err = s.Ingest(ctx, h, time.UTC, meta, nil, "", now); err != nil {
		t.Fatal(err)
	}
	check(base, 1)
}
