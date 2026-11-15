package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

func TestSiteBackpressureHealthClearsWhenCollectionResumes(t *testing.T) {
	for _, priorFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "preserve-real-failure"}[priorFailure], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "access.log")
			if err := os.WriteFile(path, []byte("192.0.2.1 - - [11/Nov/2026:03:00:00 +0000] \"GET / HTTP/1.1\" 200 100 \"-\" \"-\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := &Config{ServerURL: "http://127.0.0.1:1", AllowHTTP: true, Token: "test-only", StateDir: t.TempDir(), Collectors: CollectorsConfig{Site: SiteConfig{Enabled: true, FromStart: true, AllowDirs: []string{dir}, Sources: []SiteSource{{Path: path, Site: "backlog.test", Format: "combined"}}}}}
			cfg.applyDefaults()
			a, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			now := time.Date(2026, 11, 11, 4, 0, 0, 0, time.UTC)
			a.Now = func() time.Time { return now }
			if priorFailure {
				a.health["site"] = proto.CollectorHealth{Enabled: true, Fails: 1, LastErr: "real parse loss", ErrAt: now.Add(-time.Hour).Unix()}
			}
			before := a.health["site"]
			if err = a.buf.AppendChunks([]proto.Record{{Seq: a.st.NextSeq, TS: now.Unix(), Kind: "iface", Dim1: "test0", TX: 1}}); err != nil {
				t.Fatal(err)
			}
			a.st.NextSeq++
			normalLimit := cfg.BufferMaxBytes
			// Force pressure by lowering only the admission threshold; the buffer keeps
			// its real capacity. Restore it below to test resuming normal collection.
			cfg.BufferMaxBytes = a.buf.Size()
			if n, err := a.Tick(context.Background()); err != nil || n != 0 {
				t.Fatalf("paused tick records=%d err=%v", n, err)
			}
			var health struct{ Backlogged int }
			data, _ := json.Marshal(a.health["site"])
			if err = json.Unmarshal(data, &health); err != nil {
				t.Fatal(err)
			}
			if health.Backlogged <= 0 {
				t.Fatal("paused collector did not report recoverable unread backlog")
			}
			paused := a.health["site"]
			if paused.Fails != before.Fails || paused.LastErr != before.LastErr || paused.ErrAt != before.ErrAt {
				t.Fatalf("backpressure replaced/invented permanent error: before=%+v after=%+v", before, paused)
			}
			cfg.BufferMaxBytes = normalLimit
			now = now.Add(time.Minute)
			if n, err := a.Tick(context.Background()); err != nil || n != 1 {
				t.Fatalf("resumed tick records=%d err=%v", n, err)
			}
			data, _ = json.Marshal(a.health["site"])
			health.Backlogged = 0
			if err = json.Unmarshal(data, &health); err != nil {
				t.Fatal(err)
			}
			if health.Backlogged != 0 {
				t.Fatal("caught-up collector retained backlog")
			}
			resumed := a.health["site"]
			if resumed.Fails != 0 || resumed.LastErr != before.LastErr || resumed.ErrAt != before.ErrAt {
				t.Fatalf("catch-up erased genuine historical error or failed to reset active failures: %+v", resumed)
			}
		})
	}
}
