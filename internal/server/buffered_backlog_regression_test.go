package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/agent"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
)

func TestReviewCatchupBufferedRecords(t *testing.T) {
	s, st, _, token := setup(t, "127.0.0.1:8787")
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 4, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	h, err := st.HostByToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	// Existing health coverage may already reach now. An active backlog must
	// suppress zero-filling even when this older broad coverage cannot be undone.
	seed := proto.Meta{Protocol: 2, InstanceID: "coverage-seed", Inventory: &proto.Inventory{Sources: []proto.Source{{Site: "backlog.test", Available: true}}}, Collectors: map[string]proto.CollectorHealth{"site": {Enabled: true, LastOK: now.Unix()}}}
	_, err = st.Ingest(ctx, h, time.UTC, seed, []proto.Record{{Seq: 1, TS: now.Add(-2 * time.Hour).Unix(), Kind: "site", Dim1: "backlog.test", Dim2: "192.0.2.1", TX: 50, Site: &proto.SiteMetrics{Status: 200, Requests: 1}}}, "", now)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 4; i++ {
		lines = append(lines, fmt.Sprintf("192.0.2.1 - - [%s] \"GET / HTTP/1.1\" 200 100 \"-\" \"-\"\n", now.Add(time.Duration(-30+i)*time.Minute).Format("02/Jan/2006:15:04:05 -0700")))
	}
	path := filepath.Join(dir, "access.log")
	if err = os.WriteFile(path, []byte(strings.Join(lines, "")), 0600); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()
	cfg := agent.Config{ServerURL: httpServer.URL, AllowHTTP: true, Token: token, StateDir: filepath.Join(dir, "state"), BatchSize: 1, Collectors: agent.CollectorsConfig{Site: agent.SiteConfig{Enabled: true, FromStart: true, MaxReadBytes: int64(2 * len(lines[0])), AllowDirs: []string{dir}, Sources: []agent.SiteSource{{Path: path, Site: "backlog.test", Format: "combined"}}}}}
	configPath := filepath.Join(dir, "agent.json")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METERLY_TOKEN", "")
	t.Setenv("TRAFFICSTAT_TOKEN", "")
	loaded, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	a, err := agent.New(loaded, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Now = func() time.Time { return now }
	for tick := 1; tick <= 2; tick++ {
		n, err := a.Tick(ctx)
		if err != nil || n != 2 {
			t.Fatalf("tick %d records=%d err=%v", tick, n, err)
		}
		if n, err = a.Flush(ctx, 1); err != nil || n != 1 {
			t.Fatalf("flush %d sent=%d err=%v", tick, n, err)
		}
		fresh, err := st.HostByToken(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Buffered   int64
			Collectors map[string]struct{ Backlogged int }
		}
		if err = json.Unmarshal([]byte(fresh.MetaJSON), &wire); err != nil {
			t.Fatal(err)
		}
		wantComplete := false // upload has not caught up; records remain in durable buffer

		for _, site := range []string{"", "&site=backlog.test"} {
			w := do(s.Handler(), "GET", fmt.Sprintf("/api/v2/analytics/series?from=%d&to=%d&source=site&gran=minute%s", now.Add(-time.Hour).Unix(), now.Unix(), site), panelTok, nil)
			if w.Code != 200 {
				t.Fatalf("tick %d HTTP%d %s", tick, w.Code, w.Body.String())
			}
			var body struct {
				Meta   analysisMeta
				Points []store.AnalysisPoint
			}
			if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Meta.Complete != wantComplete {
				t.Fatalf("tick %d complete=%v want=%v buffered=%d backlog=%d points=%d", tick, body.Meta.Complete, wantComplete, wire.Buffered, wire.Collectors["site"].Backlogged, len(body.Points))
			}
		}
	}
	// Draining the remaining durable queue must restore completeness only on
	// the final acknowledged batch, without losing or duplicating traffic.
	for remaining := 1; remaining >= 0; remaining-- {
		if n, err := a.Flush(ctx, 1); err != nil || n != 1 {
			t.Fatalf("drain remaining=%d sent=%d err=%v", remaining, n, err)
		}
		for _, site := range []string{"", "&site=backlog.test"} {
			w := do(s.Handler(), "GET", fmt.Sprintf("/api/v2/analytics/series?from=%d&to=%d&source=site&gran=minute%s", now.Add(-time.Hour).Unix(), now.Unix(), site), panelTok, nil)
			if w.Code != 200 {
				t.Fatalf("drain HTTP%d: %s", w.Code, w.Body.String())
			}
			var body struct {
				Meta   analysisMeta
				Points []store.AnalysisPoint
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Meta.Complete != (remaining == 0) {
				t.Fatalf("drain remaining=%d complete=%v", remaining, body.Meta.Complete)
			}
			var tx int64
			zeros := 0
			for _, pt := range body.Points {
				tx += pt.TX
				if pt.Total == 0 {
					zeros++
				}
			}
			if tx != int64(4-remaining)*100 {
				t.Fatalf("drain remaining=%d TX=%d", remaining, tx)
			}
			if remaining > 0 && (zeros != 0 || len(body.Points) != 3) {
				t.Fatalf("pending data was zero-filled: %s", w.Body.String())
			}
			if remaining == 0 && (zeros != 56 || len(body.Points) != 60) {
				t.Fatalf("fully uploaded stream did not recover: %s", w.Body.String())
			}
		}
	}
	if n, err := a.Flush(ctx, 1); err != nil || n != 0 {
		t.Fatalf("empty flush sent=%d err=%v", n, err)
	}
	w := do(s.Handler(), "GET", fmt.Sprintf("/api/v2/analytics/series?from=%d&to=%d&source=site&gran=minute", now.Add(-time.Hour).Unix(), now.Unix()), panelTok, nil)
	var final struct {
		Meta   analysisMeta
		Points []store.AnalysisPoint
	}
	if err := json.Unmarshal(w.Body.Bytes(), &final); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, pt := range final.Points {
		total += pt.TX
	}
	if !final.Meta.Complete || total != 400 {
		t.Fatalf("post-drain heartbeat changed totals or completeness: %s", w.Body.String())
	}
	var permanent int
	if err = st.R.QueryRow("SELECT COUNT(*) FROM data_issues WHERE host_id=?", h.ID).Scan(&permanent); err != nil {
		t.Fatal(err)
	}
	if permanent != 0 {
		t.Fatalf("recoverable backlog persisted as %d permanent data issues", permanent)
	}
}
