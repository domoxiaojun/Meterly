package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

// Regression cases from the project audit, now exercised against the v2 collector.
func TestReviewTickRetriesPendingAfterWriteFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{StateDir: dir, ServerURL: "http://127.0.0.1:1", AllowHTTP: true, Token: "review-placeholder"}
	cfg.Collectors.Xray.Enabled = true
	cfg.applyDefaults()
	a, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	value := 100
	a.SetXrayExec(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"stat":[{"name":"inbound>>>test>>>traffic>>>uplink","value":%d}]}`, value)), nil
	})
	if _, err := a.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 可恢复的文件打开失败：把缓冲目录临时移走，state 所在目录仍可写。
	original := filepath.Join(dir, "buffer")
	moved := filepath.Join(dir, "buffer-moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	value = 200
	if _, err := a.Tick(context.Background()); err == nil {
		t.Fatal("expected injected buffer write failure")
	}
	saved, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Pending) != 1 || saved.Pending[0].RX != 100 {
		t.Fatalf("pending not saved: %+v", saved.Pending)
	}
	if err := os.Rename(moved, original); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	value = 300
	if _, err := a.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, err := a.Buffer().Pending(100)
	if err != nil {
		t.Fatal(err)
	}
	var total uint64
	for _, r := range records {
		total += r.RX
	}
	if total != 200 {
		t.Fatalf("recovered buffer has %d bytes, want 200; records=%+v", total, records)
	}
}

func TestReviewRotationPreservesUnreadTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	a := NewSiteAcc(SiteConfig{Enabled: true, AllowDirs: []string{dir}, Sources: []SiteSource{{Path: path, Site: "demo", Format: "combined"}}}, nil)
	defer a.Close()
	if _, _, err := a.Collect(); err != nil {
		t.Fatal(err)
	}
	line := func(n int) []byte {
		return []byte(fmt.Sprintf("192.0.2.1 - - [11/Nov/2026:08:00:00 +0000] \"GET / HTTP/1.1\" 200 %d \"-\" \"-\"\n", n))
	}
	// 两次轮询之间旧日志新增 100 字节请求，然后 rename 轮转，新文件又记 200 字节请求。
	if err := os.WriteFile(path, line(100), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, line(200), 0600); err != nil {
		t.Fatal(err)
	}
	records, stats, err := a.Collect()
	if err != nil {
		t.Fatal(err)
	}
	var total uint64
	for _, r := range records {
		if r.Kind == proto.KindSite {
			total += r.TX
		}
	}
	if total != 300 {
		t.Fatalf("after rotation got %d bytes / %d requests, want 300 bytes / 2 requests", total, stats.Used)
	}
}

func TestReviewAdjacentQuotedFieldTerminates(t *testing.T) {
	f, err := CompileLogFormat("adjacent", `$remote_addr"$request" $status $body_bytes_sent [$time_local]`)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		f.Parse(`192.0.2.1"GET / HTTP/1.1" 200 100 [11/Nov/2026:08:00:00 +0000]`)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Parse never returned: adjacent quoted field enters an empty loop; real Collect blocks Tick and Flush")
	}
}

func TestCapacityRestartRecoversAllChunks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	var logs strings.Builder
	for i := 0; i < 8000; i++ {
		fmt.Fprintf(&logs, "10.%d.%d.%d - - [11/Nov/2026:08:00:00 +0000] \"GET / HTTP/1.1\" 200 100 \"-\" \"-\"\n", i/65536, (i/256)%256, i%256)
	}
	os.WriteFile(path, []byte(logs.String()), 0600)
	cfg := &Config{StateDir: filepath.Join(dir, "state"), ServerURL: "http://127.0.0.1:1", AllowHTTP: true, Token: "test-only", BufferMaxBytes: 1 << 20, Collectors: CollectorsConfig{Site: SiteConfig{Enabled: true, FromStart: true, AllowDirs: []string{dir}, Sources: []SiteSource{{Path: path, Site: "a.test", Format: "combined"}}}}}
	cfg.applyDefaults()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, e := New(cfg, logger)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Tick(context.Background()); !errors.Is(e, ErrBufferFull) {
		t.Fatalf("expected bounded queue: %v", e)
	}
	a.Close()
	a, e = New(cfg, logger)
	if e != nil {
		t.Fatal("restart must allow upload to drain backlog", e)
	}
	defer a.Close()
	seen := map[int64]bool{}
	var sum uint64
	for i := 0; i < 20; i++ {
		records, e := a.Buffer().Pending(10000)
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range records {
			if seen[r.Seq] {
				t.Fatal("duplicate sequence")
			}
			seen[r.Seq] = true
			sum += r.TX
		}
		if len(records) > 0 {
			if e = a.Buffer().Ack(records[len(records)-1].Seq); e != nil {
				t.Fatal(e)
			}
		}
		if _, e = a.Tick(context.Background()); e != nil && !errors.Is(e, ErrBufferFull) {
			t.Fatal(e)
		}
		if len(a.st.Pending) == 0 && a.Buffer().Count() == 0 {
			break
		}
	}
	if len(seen) != 8000 || sum != 800000 {
		t.Fatalf("lost detail after recovery: %d rows %d bytes", len(seen), sum)
	}
}
