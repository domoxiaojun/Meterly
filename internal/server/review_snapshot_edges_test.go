package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/domoxiaojun/Meterly/internal/agent"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type reviewSnapshotHarness struct {
	a                  *agent.Agent
	s                  *Server
	st                 *store.Store
	token, path, state string
	now                time.Time
}

func newReviewSnapshotHarness(t *testing.T, total, readLines int, capacity int64, intercept func(http.ResponseWriter, *http.Request, http.Handler)) *reviewSnapshotHarness {
	t.Helper()
	s, st, _, token := setup(t, "127.0.0.1:8787")
	t.Cleanup(s.Close)
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 4, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	h, e := st.HostByToken(ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	seed := proto.Meta{Protocol: 2, InstanceID: "seed", Collectors: map[string]proto.CollectorHealth{"site": {Enabled: true, LastOK: now.Unix()}}, Inventory: &proto.Inventory{Sources: []proto.Source{{Site: "backlog.test", Available: true}}}}
	if _, e := st.Ingest(ctx, h, time.UTC, seed, []proto.Record{{Seq: 1, TS: now.Add(-2 * time.Hour).Unix(), Kind: "site", Dim1: "backlog.test", Dim2: "192.0.2.1", TX: 50, Site: &proto.SiteMetrics{Status: 200, Requests: 1}}}, "", now); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	var logs strings.Builder
	for i := 0; i < total; i++ {
		fmt.Fprintf(&logs, "10.%d.%d.%d - - [%s] \"GET / HTTP/1.1\" 200 100 \"-\" \"-\"\n", i/65536, (i/256)%256, i%256, now.Add(time.Duration(-30+i%4)*time.Minute).Format("02/Jan/2006:15:04:05 -0700"))
	}
	text := logs.String()
	if e := os.WriteFile(path, []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
	handler := s.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if intercept != nil {
			intercept(w, r, handler)
		} else {
			handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	// Small tests use a fixed number of complete equal-length lines; large tests
	// read the entire log in one collection pass to exercise State.Pending.
	maxRead := int64(len(text) + 1)
	if readLines > 0 && total > 0 {
		maxRead = int64(readLines * (strings.IndexByte(text, '\n') + 1))
	}
	cfg := agent.Config{ServerURL: srv.URL, AllowHTTP: true, Token: token, StateDir: filepath.Join(dir, "state"), BatchSize: 1, BufferMaxBytes: capacity, Collectors: agent.CollectorsConfig{Site: agent.SiteConfig{Enabled: true, FromStart: true, MaxReadBytes: maxRead, AllowDirs: []string{dir}, Sources: []agent.SiteSource{{Path: path, Site: "backlog.test", Format: "combined"}}}}}
	if total > 1000 {
		cfg.BatchSize = 1000
	}
	p := filepath.Join(dir, "agent.json")
	b, _ := json.Marshal(cfg)
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("METERLY_TOKEN", "")
	t.Setenv("TRAFFICSTAT_TOKEN", "")
	loaded, e := agent.LoadConfig(p)
	if e != nil {
		t.Fatal(e)
	}
	a, e := agent.New(loaded, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	a.Now = func() time.Time { return now }
	return &reviewSnapshotHarness{a: a, s: s, st: st, token: token, path: path, state: cfg.StateDir, now: now}
}
func (h *reviewSnapshotHarness) check(t *testing.T, tx int64, complete bool) {
	t.Helper()
	for _, site := range []string{"", "&site=backlog.test"} {
		w := do(h.s.Handler(), "GET", fmt.Sprintf("/api/v2/analytics/series?from=%d&to=%d&source=site&gran=minute%s", h.now.Add(-time.Hour).Unix(), h.now.Unix(), site), panelTok, nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var out struct {
			Meta   analysisMeta
			Points []store.AnalysisPoint
		}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		var sum int64
		zeros := 0
		for _, p := range out.Points {
			sum += p.TX
			if p.Total == 0 {
				zeros++
			}
		}
		if sum != tx || out.Meta.Complete != complete {
			t.Fatalf("tx=%d want=%d complete=%v want=%v points=%d reasons=%v", sum, tx, out.Meta.Complete, complete, len(out.Points), out.Meta.Reasons)
		}
		if !complete && zeros > 0 {
			t.Fatal("premature zero-fill")
		}
		if complete && len(out.Points) != 60 {
			t.Fatalf("complete points=%d", len(out.Points))
		}
	}
}
func reviewAwait(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case e := <-ch:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked/deadlocked operation")
	}
}
func reviewFlushResult(a *agent.Agent, ch chan<- error) {
	_, e := a.Flush(context.Background(), 1)
	ch <- e
}
func reviewSecondFlush(a *agent.Agent, ch chan<- error) {
	_, e := a.Flush(context.Background(), 1)
	ch <- e
}
func reviewWaitBlocked(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b := make([]byte, 1<<20)
		n := runtime.Stack(b, true)
		for _, stack := range strings.Split(string(b[:n]), "\n\n") {
			if strings.Contains(stack, marker) && (strings.Contains(stack, "[sync.Mutex.Lock]") || strings.Contains(stack, "[chan receive]") || strings.Contains(stack, "[select]")) {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatal("did not reach synchronization barrier", marker)
}
func TestReviewEmptyHeartbeatTickSnapshot(t *testing.T) {
	h := newReviewSnapshotHarness(t, 4, 0, 0, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var block int32 = 1
	h.a.Now = func() time.Time {
		if atomic.CompareAndSwapInt32(&block, 1, 0) {
			close(entered)
			<-release
		}
		return h.now
	}
	tick := make(chan error, 1)
	go func() { _, e := h.a.Tick(context.Background()); tick <- e }()
	<-entered
	flush := make(chan error, 1)
	go reviewFlushResult(h.a, flush)
	reviewWaitBlocked(t, "reviewFlushResult(")
	once.Do(func() { close(release) })
	reviewAwait(t, tick)
	reviewAwait(t, flush)
	if n := h.a.Buffer().Count(); n != 3 {
		t.Fatalf("empty-heartbeat race stranded pending=%d want3", n)
	}
	h.check(t, 100, false)
	for i := 2; i <= 4; i++ {
		if _, e := h.a.Flush(context.Background(), 1); e != nil {
			t.Fatal(e)
		}
		h.check(t, int64(i*100), i == 4)
	}
}
func TestReviewConcurrentFlushSerialization(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var count atomic.Int32
	var once sync.Once
	h := newReviewSnapshotHarness(t, 4, 0, 0, func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if r.URL.Path == "/api/v2/ingest" {
			if count.Add(1) == 1 {
				close(entered)
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		next.ServeHTTP(w, r)
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	if _, e := h.a.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	f1 := make(chan error, 1)
	go reviewFlushResult(h.a, f1)
	<-entered
	f2 := make(chan error, 1)
	go reviewSecondFlush(h.a, f2)
	reviewWaitBlocked(t, "reviewSecondFlush(")
	once.Do(func() { close(release) })
	reviewAwait(t, f1)
	reviewAwait(t, f2)
	if n := h.a.Buffer().Count(); n != 2 {
		t.Fatalf("two Flush calls acknowledged overlapping batches: pending=%d want2", n)
	}
	h.check(t, 200, false)
	if _, e := h.a.Flush(context.Background(), 10); e != nil {
		t.Fatal(e)
	}
	h.check(t, 400, true)
}
func TestReviewSlowHTTPAllowsTickAndRetryNoDuplication(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var request atomic.Int32
	var once sync.Once
	h := newReviewSnapshotHarness(t, 4, 2, 0, func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if r.URL.Path == "/api/v2/ingest" && request.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			rr := httptest.NewRecorder()
			next.ServeHTTP(rr, r)
			w.WriteHeader(503)
			return
		}
		next.ServeHTTP(w, r)
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	if _, e := h.a.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	flush := make(chan error, 1)
	go reviewFlushResult(h.a, flush)
	<-entered
	tick := make(chan error, 1)
	go func() { _, e := h.a.Tick(context.Background()); tick <- e }()
	reviewAwait(t, tick) // Must finish while HTTP remains blocked.
	once.Do(func() { close(release) })
	select {
	case e := <-flush:
		if e == nil {
			t.Fatal("expected injected ambiguous HTTP failure")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("flush deadlock")
	}
	if n := h.a.Buffer().Count(); n != 4 {
		t.Fatalf("unconfirmed records removed: %d", n)
	}
	if _, e := h.a.Flush(context.Background(), 1); e != nil {
		t.Fatal(e)
	}
	h.check(t, 100, false)
	for i := 2; i <= 4; i++ {
		if _, e := h.a.Flush(context.Background(), 1); e != nil {
			t.Fatal(e)
		}
		h.check(t, int64(i*100), i == 4)
	}
	for i := 0; i < 3; i++ {
		if n, e := h.a.Flush(context.Background(), 1); e != nil || n != 0 {
			t.Fatalf("heartbeat %d %v", n, e)
		}
		h.check(t, 400, true)
	}
}
func TestReviewPersistedStatePendingCompleteness(t *testing.T) {
	h := newReviewSnapshotHarness(t, 8000, 0, 1<<20, nil)
	if _, e := h.a.Tick(context.Background()); !errors.Is(e, agent.ErrBufferFull) {
		t.Fatalf("expected capacity pressure, got %v", e)
	}
	state, e := agent.LoadState(h.state)
	if e != nil {
		t.Fatal(e)
	}
	last := h.a.Buffer().LastSeq()
	unwritten := 0
	for _, r := range state.Pending {
		if r.Seq > last {
			unwritten++
		}
	}
	if unwritten == 0 {
		t.Fatal("test did not leave durable State.Pending suffix")
	}
	// Drain only the spool; State.Pending cannot be retried until the next Tick.
	for h.a.Buffer().Count() > 0 {
		if _, e := h.a.Flush(context.Background(), 1000); e != nil {
			t.Fatal(e)
		}
	}
	hst, e := h.st.HostByToken(context.Background(), h.token)
	if e != nil {
		t.Fatal(e)
	}
	var meta proto.Meta
	if e := json.Unmarshal([]byte(hst.MetaJSON), &meta); e != nil {
		t.Fatal(e)
	}
	t.Logf("durable unspooled suffix=%d spool=%d reported buffered=%d backlogged=%d", unwritten, h.a.Buffer().Count(), meta.Buffered, meta.Collectors["site"].Backlogged)

	w := do(h.s.Handler(), "GET", fmt.Sprintf("/api/v2/analytics/series?from=%d&to=%d&source=site&gran=minute", h.now.Add(-time.Hour).Unix(), h.now.Unix()), panelTok, nil)
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
	t.Logf("with durable suffix pending: complete=%v points=%d TX=%d expected-final=800000", out.Meta.Complete, len(out.Points), tx)
	if tx != int64(8000-unwritten)*100 {
		t.Fatal("unexpected ingested total")
	}
	if out.Meta.Complete {
		t.Error("durable State.Pending suffix omitted from completeness snapshot")
	}
	for round := 0; round < 10; round++ {
		_, e := h.a.Tick(context.Background())
		if e != nil && !errors.Is(e, agent.ErrBufferFull) {
			t.Fatal(e)
		}
		if _, e := h.a.Flush(context.Background(), 1000); e != nil {
			t.Fatal(e)
		}
		state, e := agent.LoadState(h.state)
		if e != nil {
			t.Fatal(e)
		}
		if len(state.Pending) == 0 && h.a.Buffer().Count() == 0 {
			break
		}
	}
	h.check(t, 800000, true)
	t.Log("recovery drained all 8000 records, TX=800000, no duplication")
	var permanent int
	if e := h.st.R.QueryRow("SELECT COUNT(*) FROM data_issues").Scan(&permanent); e != nil || permanent != 0 {
		t.Fatalf("permanent=%d err=%v", permanent, e)
	}

}

var _ = bytes.NewReader

// Restart at both failure boundaries: a full spool and a drained spool with
// durable State.Pending still holding an unwritten suffix.
func TestReviewPersistedPendingRestartCompleteness(t *testing.T) {
	for _, drainBeforeRestart := range []bool{false, true} {
		t.Run(fmt.Sprintf("drain_before_restart_%v", drainBeforeRestart), func(t *testing.T) {
			h := newReviewSnapshotHarness(t, 8000, 0, 1<<20, nil)
			if _, err := h.a.Tick(context.Background()); !errors.Is(err, agent.ErrBufferFull) {
				t.Fatalf("want capacity failure, got %v", err)
			}
			if drainBeforeRestart {
				if _, err := h.a.Flush(context.Background(), 1000); err != nil {
					t.Fatal(err)
				}
				h.check(t, 523900, false)
			}
			if err := h.a.Close(); err != nil {
				t.Fatal(err)
			}
			cfg, err := agent.LoadConfig(filepath.Join(filepath.Dir(h.state), "agent.json"))
			if err != nil {
				t.Fatal(err)
			}
			resumed, err := agent.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { resumed.Close() })
			h.a = resumed
			resumed.Now = func() time.Time { return h.now }
			// New may recover a suffix into the spool. Before any Tick, inspect the
			// first post: queued remainder + strictly unspooled suffix, not all Pending.
			state, err := agent.LoadState(h.state)
			if err != nil {
				t.Fatal(err)
			}
			watermark := h.a.Buffer().LastSeq()
			var suffix int64
			for _, r := range state.Pending {
				if r.Seq > watermark {
					suffix++
				}
			}
			before := h.a.Buffer().Count()
			batch := before
			if batch > int64(cfg.BatchSize) {
				batch = int64(cfg.BatchSize)
			}
			if _, err := h.a.Flush(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
			host, err := h.st.HostByToken(context.Background(), h.token)
			if err != nil {
				t.Fatal(err)
			}
			var meta proto.Meta
			if err := json.Unmarshal([]byte(host.MetaJSON), &meta); err != nil {
				t.Fatal(err)
			}
			if want := before - batch + suffix; meta.Buffered != want {
				t.Fatalf("buffered=%d want=%d queued=%d suffix=%d", meta.Buffered, want, before, suffix)
			}
			for round := 0; round < 10; round++ {
				_, err := h.a.Tick(context.Background())
				if err != nil && !errors.Is(err, agent.ErrBufferFull) {
					t.Fatal(err)
				}
				if _, err := h.a.Flush(context.Background(), 1000); err != nil {
					t.Fatal(err)
				}
				state, err := agent.LoadState(h.state)
				if err != nil {
					t.Fatal(err)
				}
				if len(state.Pending) == 0 && h.a.Buffer().Count() == 0 {
					break
				}
			}
			h.check(t, 800000, true)
			for i := 0; i < 2; i++ {
				if _, err := h.a.Flush(context.Background(), 1); err != nil {
					t.Fatal(err)
				}
				h.check(t, 800000, true)
			}
			var permanent int
			if err := h.st.R.QueryRow("SELECT COUNT(*) FROM data_issues").Scan(&permanent); err != nil || permanent != 0 {
				t.Fatalf("permanent=%d err=%v", permanent, err)
			}
		})
	}
}
