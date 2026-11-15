package store

import (
	"context"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"testing"
	"time"
)

func TestBufferedSiteCoverageAndTransientIssues(t *testing.T) {
	s := open(t)
	h := host(t, s, "queued")
	healthy := host(t, s, "healthy")
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)
	meta := proto.Meta{Protocol: 2, InstanceID: "queued", SentAt: now.Unix(), Buffered: 2,
		Collectors: map[string]proto.CollectorHealth{"site": {Enabled: true, LastOK: now.Unix()}},
		Inventory:  &proto.Inventory{Sources: []proto.Source{{Site: "queued.test", Available: true}}}}
	ts := now.Add(-time.Hour).Unix()
	records := []proto.Record{{Seq: 1, TS: ts, Kind: "site", Dim1: "queued.test", Dim2: "192.0.2.1", TX: 100, Site: &proto.SiteMetrics{Status: 200, Requests: 1}}}
	if _, err := s.Ingest(ctx, h, time.UTC, meta, records, "", now); err != nil {
		t.Fatal(err)
	}
	_, last, err := s.Coverage(ctx, []int64{h.ID}, "site")
	if err != nil || last != ts {
		t.Fatalf("queued records falsely advanced coverage last=%d err=%v", last, err)
	}
	_, last, err = s.SourceCoverage(ctx, []int64{h.ID}, "queued.test")
	if err != nil || last != ts {
		t.Fatalf("queued records falsely advanced source coverage last=%d err=%v", last, err)
	}
	f := AnalysisFilter{HostIDs: []int64{h.ID}, Source: "site", From: ts, To: now.Unix()}
	check := func(filter AnalysisFilter, want int) {
		t.Helper()
		issues, err := s.DataIssues(ctx, filter)
		if err != nil || len(issues) != want {
			t.Fatalf("filter=%+v issues=%v err=%v", filter, issues, err)
		}
	}
	check(f, 1)
	other := f
	other.HostIDs = []int64{healthy.ID}
	check(other, 0)
	other.HostIDs = []int64{h.ID, healthy.ID}
	check(other, 1)
	for _, source := range []string{"iface", "singbox"} {
		other = f
		other.Source = source
		check(other, 0)
	}
	meta.Buffered = 0
	if _, err := s.Ingest(ctx, h, time.UTC, meta, nil, "", now); err != nil {
		t.Fatal(err)
	}
	check(f, 0)
	_, last, err = s.Coverage(ctx, []int64{h.ID}, "site")
	if err != nil || last != now.Unix() {
		t.Fatalf("drained coverage last=%d err=%v", last, err)
	}
	_, last, err = s.SourceCoverage(ctx, []int64{h.ID}, "queued.test")
	if err != nil || last != now.Unix() {
		t.Fatalf("drained source coverage last=%d err=%v", last, err)
	}
	meta.Buffered = 2
	meta.Collectors["site"] = proto.CollectorHealth{Enabled: false, LastOK: now.Unix()}
	if _, err := s.Ingest(ctx, h, time.UTC, meta, nil, "", now); err != nil {
		t.Fatal(err)
	}
	check(f, 0)
	var n int
	if err := s.R.QueryRow("SELECT COUNT(*) FROM data_issues WHERE host_id=?", h.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("recoverable upload queue persisted permanent loss: count=%d err=%v", n, err)
	}
}
