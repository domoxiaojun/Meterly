package store

import (
	"context"
	"fmt"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"testing"
	"time"
)

func TestRegressionOverflowBatchAtomicity(t *testing.T) {
	for _, table := range []string{"fact_minute", "fact_hour", "fact_day", "site_minute", "site_hour", "site_day"} {
		for _, field := range []string{"rx", "requests"} {
			if field == "requests" && table[:4] == "fact" {
				continue
			}
			t.Run(table+"/"+field, func(t *testing.T) {
				s := open(t)
				h := host(t, s, "overflow")
				ctx := context.Background()
				now := time.Now().Truncate(time.Hour)
				r := proto.Record{Seq: 1, TS: now.Unix() - 60, Kind: "site", Dim1: "a.test", Dim2: "192.0.2.1", RX: 10, TX: 20, Site: &proto.SiteMetrics{Requests: 2, Status: 200}}
				meta := proto.Meta{InstanceID: "overflow"}
				if _, e := s.Ingest(ctx, h, time.UTC, meta, []proto.Record{r}, "", now); e != nil {
					t.Fatal(e)
				}
				if _, e := s.W.Exec("UPDATE "+table+" SET "+field+"=?", MetricLimit-30); e != nil {
					t.Fatal(e)
				}
				r.Seq = 2
				r.RX = 50
				r.Site.Requests = 50
				good := r
				good.Seq = 3
				good.Dim1 = "other.test"
				good.TS = now.Add(-2 * time.Hour).Unix()
				good.RX = 1
				good.TX = 1
				result, e := s.Ingest(ctx, h, time.UTC, meta, []proto.Record{good, r}, "", now)
				if e != nil || result.Accepted != 0 || result.Rejected != 2 || result.MaxSeq != 3 {
					t.Fatalf("atomic reject %+v %v", result, e)
				}
				var count int
				s.R.QueryRow("SELECT COUNT(*) FROM site_minute WHERE site='other.test'").Scan(&count)
				if count != 0 {
					t.Fatal("partial site writes escaped")
				}
				var firstTS int64
				if e := s.R.QueryRow("SELECT first_ts FROM collection_coverage WHERE host_id=? AND kind='site'", h.ID).Scan(&firstTS); e != nil || firstTS != r.TS-60 {
					t.Fatal("rejected batch changed collection coverage", firstTS, e)
				}
				var value int64
				if e := s.R.QueryRow("SELECT " + field + " FROM " + table + " LIMIT 1").Scan(&value); e != nil || value != MetricLimit-30 {
					t.Fatal("existing bucket changed", value, e)
				}
				result, e = s.Ingest(ctx, h, time.UTC, meta, []proto.Record{good, r}, "", now)
				if e != nil || result.Duplicates != 2 {
					t.Fatal("rejected sequence retried", result, e)
				}
				good.Seq = 4
				result, e = s.Ingest(ctx, h, time.UTC, meta, []proto.Record{good}, "", now)
				if e != nil || result.Accepted != 1 {
					t.Fatal("subsequent healthy batch blocked", result, e)
				}
				if e := s.R.QueryRow("SELECT first_ts FROM collection_coverage WHERE host_id=? AND kind='site'", h.ID).Scan(&firstTS); e != nil || firstTS != good.TS-60 {
					t.Fatal("healthy batch did not extend collection coverage", firstTS, e)
				}
			})
		}
	}
}

func TestRegressionAggregateReadOverflow(t *testing.T) {
	s := open(t)
	h := host(t, s, "sum")
	ctx := context.Background()
	now := time.Now().Truncate(time.Hour)
	var records []proto.Record
	for i := 0; i < 5; i++ {
		records = append(records, proto.Record{Seq: int64(i + 1), TS: now.Unix() - 60, Kind: "site", Dim1: fmt.Sprintf("%d.test", i), Dim2: fmt.Sprintf("192.0.2.%d", i+1), RX: 1, Site: &proto.SiteMetrics{Requests: 1, Status: 500, RXKnown: true}})
	}
	if _, e := s.Ingest(ctx, h, time.UTC, proto.Meta{}, records, "", now); e != nil {
		t.Fatal(e)
	}
	// Each bucket is valid; their combined sum is larger than int64.
	if _, e := s.W.Exec("UPDATE site_hour SET rx=?,tx=0,requests=?", MetricLimit-1, MetricLimit-1); e != nil {
		t.Fatal(e)
	}
	f := AnalysisFilter{From: now.Add(-time.Hour).Unix(), To: now.Unix(), HostIDs: []int64{h.ID}, Source: "site", Table: "hour", Group: "host", Page: 1, PageSize: 50}
	result, e := s.Analyze(ctx, f)
	if e != nil || !result.Totals.Overflow || result.Totals.Total != MetricLimit || result.Totals.Requests != MetricLimit {
		t.Fatal(result, e)
	}
	points, e := s.AnalysisSeries(ctx, f, "hour", time.UTC)
	if e != nil || len(points) != 1 || !points[0].Overflow {
		t.Fatal(points, e)
	}
	if e = s.ExportAnalysis(ctx, f, func(r AnalysisRow) error {
		if !r.Overflow {
			t.Error("export lost overflow state")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	// Old SQLite REAL/negative counters are treated as unavailable, not accepted as precise integers.
	for _, value := range []any{float64(1e20), int64(-1)} {
		s.W.Exec("UPDATE site_hour SET rx=?", value)
		result, e = s.Analyze(ctx, f)
		if e != nil || !result.Totals.Overflow {
			t.Fatal(result, e)
		}
	}
	var exact int64
	if e = s.R.QueryRow("SELECT metric_sum(v) FROM (SELECT 9007199254740993 AS v UNION ALL SELECT 1)").Scan(&exact); e != nil || exact != 9007199254740994 {
		t.Fatal("lost integer precision", exact, e)
	}
	if AddMetric(MetricLimit-1, 2) != MetricLimit {
		t.Fatal("Go total overflow")
	}
}

func TestRegressionRecoveredAndBackloggedHealth(t *testing.T) {
	s := open(t)
	h := host(t, s, "health")
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)
	failed := now.Add(-time.Hour).Unix()
	meta := proto.Meta{InstanceID: "health", SentAt: failed, Collectors: map[string]proto.CollectorHealth{"site": {Enabled: true, Fails: 1, LastErr: "failure", ErrAt: failed}}}
	for i := 0; i < 3; i++ {
		if i > 0 {
			meta.Collectors["site"] = proto.CollectorHealth{Enabled: true, LastOK: now.Unix(), LastErr: "failure", ErrAt: failed}
		}
		if _, e := s.Ingest(ctx, h, time.UTC, meta, nil, "", now.Add(time.Duration(i)*time.Minute)); e != nil {
			t.Fatal(e)
		}
	}
	var count int
	s.R.QueryRow("SELECT COUNT(*) FROM data_issues").Scan(&count)
	if count != 1 {
		t.Fatal("historical issue duplicated", count)
	}
	issues, e := s.DataIssues(ctx, AnalysisFilter{HostIDs: []int64{h.ID}, From: now.Add(-time.Minute).Unix(), To: now.Add(time.Hour).Unix()})
	if e != nil || len(issues) > 0 {
		t.Fatal("recovery invented current gap", issues, e)
	}
	meta.Collectors["site"] = proto.CollectorHealth{Enabled: true, Fails: 0, LastErr: "legacy without err_at", LastOK: now.Unix()}
	s.Ingest(ctx, h, time.UTC, meta, nil, "", now)
	s.R.QueryRow("SELECT COUNT(*) FROM data_issues").Scan(&count)
	if count != 1 {
		t.Fatal("legacy healthy agent invented gap")
	}
}

func TestRegressionUpgradeExpiryPreservesTerminal(t *testing.T) {
	s := open(t)
	h := host(t, s, "upgrade")
	ctx := context.Background()
	now := time.Now()
	id, e := s.CreateUpgrade(ctx, h.ID, proto.UpgradeTask{Version: "v2.0.1", CreatedAt: now.Add(-25 * time.Hour).Unix()})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.UpgradeStatus(ctx, h.ID, id, "running", "replayed progress"); e != nil {
		t.Fatal(e)
	}
	next, e := s.NextUpgrade(ctx, h.ID, now)
	if e != nil || next != nil {
		t.Fatal(next, e)
	}
	if e = s.UpgradeStatus(ctx, h.ID, id, "succeeded", "late report"); e != nil {
		t.Fatal("late terminal not acknowledged", e)
	}
	var status string
	s.R.QueryRow("SELECT status FROM upgrade_tasks WHERE id=?", id).Scan(&status)
	if status != "failed" {
		t.Fatal("terminal overwritten", status)
	}
	if _, e = s.CreateUpgrade(ctx, h.ID, proto.UpgradeTask{Version: "v2.0.2", CreatedAt: now.Unix()}); e != nil {
		t.Fatal("slot still occupied", e)
	}
}
