package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/agent"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
)

func TestIndependentConcurrentTickBufferedSnapshot(t *testing.T) {
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
	if n, e := a.Tick(ctx); e != nil || n != 2 {
		t.Fatalf("initial tick %d %v", n, e)
	}
	if n, e := a.Flush(ctx, 1); e != nil || n != 1 {
		t.Fatalf("initial flush %d %v", n, e)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var block int32 = 1
	a.Now = func() time.Time {
		if atomic.CompareAndSwapInt32(&block, 1, 0) {
			close(entered)
			<-release
		}
		return now
	}
	tickDone := make(chan error, 1)
	go func() { _, e := a.Tick(ctx); tickDone <- e }()
	<-entered // Tick holds agent.mu, but has not yet read the remaining two lines.
	flushDone := make(chan error, 1)
	go func() { _, e := a.Flush(ctx, 1); flushDone <- e }()
	// Synchronize on Flush reaching meta(): Pending/Count have snapshotted the
	// one old queued record, while meta waits for the Tick-owned agent mutex.
	deadline := time.Now().Add(3 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		if bytes.Contains(buf[:n], []byte("/internal/agent.(*Agent).meta(")) || bytes.Contains(buf[:n], []byte("/internal/agent.(*Agent).snapshotBatch(")) {
			blocked = true
			break
		}
		runtime.Gosched()
	}
	close(release)
	if e := <-tickDone; e != nil {
		t.Fatal(e)
	}
	if e := <-flushDone; e != nil {
		t.Fatal(e)
	}
	if !blocked {
		t.Fatal("did not synchronize Flush meta snapshot")
	}
	fresh, e := st.HostByToken(ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	var wire struct {
		Buffered   int64
		Collectors map[string]struct{ Backlogged int }
	}
	if e := json.Unmarshal([]byte(fresh.MetaJSON), &wire); e != nil {
		t.Fatal(e)
	}
	actual := a.Buffer().Count()
	w := do(s.Handler(), "GET", fmt.Sprintf("/api/v2/analytics/series?from=%d&to=%d&source=site&gran=minute", now.Add(-time.Hour).Unix(), now.Unix()), panelTok, nil)
	var out struct {
		Meta   analysisMeta
		Points []store.AnalysisPoint
	}
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	var tx int64
	for _, p := range out.Points {
		tx += p.TX
	}
	t.Logf("durable pending=%d meta.buffered=%d backlog=%d complete=%v points=%d tx=%d", actual, wire.Buffered, wire.Collectors["site"].Backlogged, out.Meta.Complete, len(out.Points), tx)
	if actual != 2 {
		t.Fatalf("unexpected pending %d", actual)
	}
	if out.Meta.Complete {
		t.Fatal("new Tick records pending upload but snapshot reported fully complete")
	}
	for i, wantTX := range []int64{300, 400, 400} {
		wantSent := 1
		if i == 2 {
			wantSent = 0
		}
		if n, err := a.Flush(ctx, 1); err != nil || n != wantSent {
			t.Fatalf("drain %d sent=%d err=%v", i, n, err)
		}
		w := do(s.Handler(), "GET", fmt.Sprintf("/api/v2/analytics/series?from=%d&to=%d&source=site&gran=minute", now.Add(-time.Hour).Unix(), now.Unix()), panelTok, nil)
		if w.Code != 200 {
			t.Fatalf("drain HTTP %d: %s", w.Code, w.Body.String())
		}
		var after struct {
			Meta   analysisMeta
			Points []store.AnalysisPoint
		}
		if err := json.Unmarshal(w.Body.Bytes(), &after); err != nil {
			t.Fatal(err)
		}
		var total int64
		for _, p := range after.Points {
			total += p.TX
		}
		if total != wantTX {
			t.Fatalf("drain %d tx=%d want %d", i, total, wantTX)
		}
		wantComplete := i > 0
		if after.Meta.Complete != wantComplete {
			t.Fatalf("drain %d complete=%v want %v", i, after.Meta.Complete, wantComplete)
		}
		if wantComplete && len(after.Points) != 60 {
			t.Fatalf("complete points=%d want60", len(after.Points))
		}
	}
	if n := a.Buffer().Count(); n != 0 {
		t.Fatalf("still queued %d", n)
	}

}
