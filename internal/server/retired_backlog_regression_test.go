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

func TestReviewRetiredLogBacklog(t *testing.T) {
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
	for i := 0; i < 3; i++ {
		lines = append(lines, fmt.Sprintf("192.0.2.1 - - [%s] \"GET / HTTP/1.1\" 200 100 \"-\" \"-\"\n", now.Add(time.Duration(-30+i)*time.Minute).Format("02/Jan/2006:15:04:05 -0700")))
	}
	path := filepath.Join(dir, "access.log")
	if err = os.WriteFile(path, []byte(strings.Join(lines, "")), 0600); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()
	cfg := agent.Config{ServerURL: httpServer.URL, AllowHTTP: true, Token: token, StateDir: filepath.Join(dir, "state"), Collectors: agent.CollectorsConfig{Site: agent.SiteConfig{Enabled: true, FromStart: true, MaxReadBytes: int64(len(lines[0])), AllowDirs: []string{dir}, Sources: []agent.SiteSource{{Path: path, Site: "backlog.test", Format: "combined"}}}}}
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
	for tick := 1; tick <= 3; tick++ {
		if tick == 2 {
			if err := os.Rename(path, path+".1"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		n, err := a.Tick(ctx)
		if err != nil || n != 1 {
			t.Fatalf("tick %d records=%d err=%v", tick, n, err)
		}
		if n, err = a.Flush(ctx, 10); err != nil || n != 1 {
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
		if wire.Buffered != 0 {
			t.Fatal("upload buffer should be drained; this regression is unread log backlog")
		}
		wantComplete := tick == 3
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
				t.Fatalf("tick %d complete=%v want=%v backlog=%d points=%d", tick, body.Meta.Complete, wantComplete, wire.Collectors["site"].Backlogged, len(body.Points))
			}
			var total int64
			zeros := 0
			for _, point := range body.Points {
				total += point.TX
				if point.Total == 0 {
					zeros++
				}
			}
			if total != int64(tick*100) {
				t.Fatalf("tick %d TX=%d (lost/duplicated traffic)", tick, total)
			}
			if !wantComplete && (zeros != 0 || len(body.Points) != tick || body.Meta.State != "partial") {
				t.Fatalf("tick %d incomplete stream was zero filled or state wrong: %s", tick, w.Body.String())
			}
			if wantComplete && (len(body.Points) != 60 || zeros != 57) {
				t.Fatalf("healthy zero fill did not recover: %s", w.Body.String())
			}
		}
		if (wire.Collectors["site"].Backlogged == 0) != wantComplete {
			t.Fatalf("tick %d backlog signal=%d", tick, wire.Collectors["site"].Backlogged)
		}
	}
	var permanent int
	if err = st.R.QueryRow("SELECT COUNT(*) FROM data_issues WHERE host_id=?", h.ID).Scan(&permanent); err != nil {
		t.Fatal(err)
	}
	if permanent != 0 {
		t.Fatalf("recoverable backlog persisted as %d permanent data issues", permanent)
	}
}
