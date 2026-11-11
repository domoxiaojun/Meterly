package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

func TestFullWebsiteIPAnalytics(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	a := host(t, s, "a")
	b := host(t, s, "b")
	loc, _ := time.LoadLocation("Asia/Singapore")
	start := time.Date(2026, 10, 31, 0, 0, 0, 0, loc).Unix()
	now := time.Unix(start+3*86400, 0)
	var records []proto.Record
	var total int64
	for i := 1; i <= 137; i++ {
		for hr := 0; hr < 24; hr++ {
			n := int64(i*10 + hr)
			total += n
			records = append(records, proto.Record{Seq: int64(len(records) + 1), TS: start + int64(hr*3600) + 60, Kind: proto.KindSite, Dim1: "example.com", Dim2: fmt.Sprintf("198.51.100.%d", i), TX: uint64(n), RX: 7, Site: &proto.SiteMetrics{Status: 200, Requests: 1, RXKnown: true, IPSource: "remote_addr"}})
		}
	}
	meta := proto.Meta{Protocol: 2, InstanceID: "a"}
	if r, e := s.Ingest(ctx, a, loc, meta, records, "", now); e != nil || r.Accepted != len(records) {
		t.Fatalf("ingest: %+v %v", r, e)
	}
	if r, e := s.Ingest(ctx, a, loc, meta, records, "", now); e != nil || r.Duplicates != len(records) {
		t.Fatalf("duplicate: %+v %v", r, e)
	}
	// Another host, different billing timezone, same analysis day and same IP/site.
	ny, _ := time.LoadLocation("America/New_York")
	more := []proto.Record{{Seq: 1, TS: start + 3600 + 60, Kind: proto.KindSite, Dim1: "example.com", Dim2: "::ffff:198.51.100.1", TX: 55, Site: &proto.SiteMetrics{Status: 503, Requests: 2, RXKnown: false, IPSource: "trusted_xff"}}, {Seq: 2, TS: start + 86400 + 60, Kind: proto.KindSite, Dim1: "example.com", Dim2: "2001:db8::1", TX: 999, Site: &proto.SiteMetrics{Status: 200, Requests: 1}}}
	if _, e := s.Ingest(ctx, b, ny, proto.Meta{InstanceID: "b"}, more, "", now); e != nil {
		t.Fatal(e)
	}
	total += 55
	if _, e := s.Maintain(ctx, Retention{}, now); e != nil {
		t.Fatal(e)
	}
	f := AnalysisFilter{From: start, To: start + 86400, HostIDs: []int64{a.ID, b.ID}, Source: "site", Site: "example.com", Group: "ip", Sort: "total", Order: "desc", Page: 1, PageSize: 50, Table: "hour"}
	seen := map[string]bool{}
	var pagesTotal int64
	for page := 1; page <= 3; page++ {
		f.Page = page
		r, e := s.Analyze(ctx, f)
		if e != nil {
			t.Fatal(e)
		}
		if r.Count != 137 || r.Totals.Total != total+137*24*7 || r.Totals.Requests != 137*24+2 || r.Totals.RXKnown {
			t.Fatalf("totals %+v count %d", r.Totals, r.Count)
		}
		for _, row := range r.Rows {
			if seen[row.Key] {
				t.Fatal("duplicate page key")
			}
			seen[row.Key] = true
			pagesTotal += row.Total
		}
	}
	if len(seen) != 137 || pagesTotal != total+137*24*7 {
		t.Fatalf("lost IPs %d %d", len(seen), pagesTotal)
	}
	var csvTotal int64
	var csvCount int
	if e := s.ExportAnalysis(ctx, f, func(row AnalysisRow) error { csvTotal += row.Total; csvCount++; return nil }); e != nil {
		t.Fatal(e)
	}
	if csvCount != 137 || csvTotal != pagesTotal {
		t.Fatal("CSV not complete")
	}
	pts, e := s.AnalysisSeries(ctx, f, "hour", loc)
	if e != nil {
		t.Fatal(e)
	}
	var curveTotal int64
	for _, p := range pts {
		curveTotal += p.Total
	}
	if curveTotal != pagesTotal || len(pts) != 24 {
		t.Fatal("curve does not reconcile")
	}
	daily, e := s.AnalysisSeries(ctx, f, "day", loc)
	if e != nil || len(daily) != 1 || daily[0].IPs != 137 || daily[0].TS != start {
		t.Fatalf("day distinct count %+v %v", daily, e)
	}
	f.Page = 1
	f.IP = "::ffff:198.51.100.1"
	f.Status = 503
	r, e := s.Analyze(ctx, f)
	if e != nil || r.Count != 1 || r.Totals.TX != 55 || r.Totals.Requests != 2 {
		t.Fatalf("joint filter %+v %v", r, e)
	}
	f.IP = "198.51.100.0/25"
	f.Status = 0
	r, e = s.Analyze(ctx, f)
	if e != nil || r.Count != 127 {
		t.Fatalf("CIDR %+v %v", r, e)
	}
	// Ensure query can use the site/time index rather than scanning unrelated history.
	q, args, e := rankSQL(f, true)
	if e != nil {
		t.Fatal(e)
	}
	rows, e := s.R.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q, args...)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	var plans []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if e = rows.Scan(&id, &parent, &unused, &detail); e != nil {
			t.Fatal(e)
		}
		plans = append(plans, detail)
	}
	t.Logf("query plan: %v", plans)
}

func TestRetentionBoundaryAndDailySummary(t *testing.T) {
	s := open(t)
	h := host(t, s, "a")
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	for i, age := range []int64{47 * 3600, 49 * 3600, 364 * 86400} {
		r := proto.Record{Seq: int64(i + 1), TS: now.Unix() - age + 60, Kind: proto.KindSite, Dim1: "a.test", Dim2: "2001:db8::1", TX: 10, Site: &proto.SiteMetrics{Status: 200, Requests: 1}}
		if _, e := s.Ingest(ctx, h, time.UTC, proto.Meta{InstanceID: "i"}, []proto.Record{r}, "", now); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Maintain(ctx, Retention{}, now); e != nil {
		t.Fatal(e)
	}
	var n int
	s.R.QueryRow("SELECT COUNT(*) FROM site_minute").Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
	s.R.QueryRow("SELECT COUNT(*) FROM site_hour").Scan(&n)
	if n != 3 {
		t.Fatal(n)
	}
	s.Maintain(ctx, Retention{}, now.Add(2*24*time.Hour))
	s.R.QueryRow("SELECT COUNT(*) FROM site_hour").Scan(&n)
	if n != 2 {
		t.Fatal(n)
	}
	s.R.QueryRow("SELECT COUNT(*) FROM site_day").Scan(&n)
	if n != 2 {
		t.Fatal("daily summary lost", n)
	}
	var tx int64
	s.R.QueryRow("SELECT SUM(tx) FROM site_day").Scan(&tx)
	if tx != 30 {
		t.Fatal("daily totals lost", tx)
	}
}
