package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

const refreshTestLine = "192.0.2.1 - - [11/Nov/2026:08:00:00 +0000] \"GET / HTTP/1.1\" 200 100 \"-\" \"-\"\n"

type refreshFixture struct {
	a       *Agent
	sources []SiteSource
	mu      sync.Mutex
	policy  proto.Policy
}

func newRefreshFixture(t *testing.T, count int) *refreshFixture {
	t.Helper()
	f := &refreshFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(proto.ControlResponse{Config: proto.DesiredConfig{Policy: f.policy}})
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	for i := 0; i < count; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%d.log", i))
		if err := os.WriteFile(path, []byte(strings.Repeat(refreshTestLine, 100)), 0600); err != nil {
			t.Fatal(err)
		}
		f.sources = append(f.sources, SiteSource{Path: path, Site: fmt.Sprintf("site%d.test", i), Format: "combined"})
	}
	discovery := false
	cfg := &Config{ServerURL: srv.URL, AllowHTTP: true, Token: "test-only", StateDir: t.TempDir(), Discovery: &discovery,
		Collectors: CollectorsConfig{Site: SiteConfig{Enabled: true, FromStart: true, MaxReadBytes: int64(len(refreshTestLine)), AllowDirs: []string{dir}, Sources: f.sources}}}
	cfg.applyDefaults()
	a, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	f.a = a
	t.Cleanup(func() { _ = a.Close() })
	a.DetectSys = &DetectSys{Root: t.TempDir(), Procs: []ProcInfo{},
		LookPath: func(string) (string, error) { return "", errors.New("not installed") },
		Exec:     func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not installed") }}
	return f
}

func (f *refreshFixture) refresh(t *testing.T) {
	t.Helper()
	if err := f.a.RefreshConfiguration(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}

func (f *refreshFixture) collect(t *testing.T) []Counter {
	t.Helper()
	records, _, err := f.a.site.Collect()
	if err != nil {
		t.Fatal(err)
	}
	f.a.st.SiteOffsets = f.a.site.Offsets()
	return records
}

func TestSiteRefreshRoundRobinFairness(t *testing.T) {
	f := newRefreshFixture(t, 6)
	seen := map[string]int{}
	for tick := 0; tick < 20; tick++ {
		if tick%5 == 0 {
			f.refresh(t)
		}
		for _, c := range f.collect(t) {
			seen[c.Key]++
		}
	}
	t.Logf("20 polls with four refreshes: %v", seen)
	for _, src := range f.sources {
		if seen[src.Site] < 3 || seen[src.Site] > 4 {
			t.Errorf("%s collected %d times, want 3 or 4", src.Site, seen[src.Site])
		}
	}
}

func TestSiteRefreshSourceChanges(t *testing.T) {
	cases := []struct {
		name     string
		order    []int
		disabled []int
		want     int
	}{
		{name: "same order", order: []int{0, 1, 2, 3}, want: 2},
		{name: "reordered", order: []int{3, 2, 1, 0}, want: 2},
		{name: "source inserted before next", order: []int{3, 0, 1, 2}, want: 2},
		{name: "next source removed", order: []int{0, 1, 3}, want: 3},
		{name: "earlier source removed", order: []int{1, 2, 3}, want: 2},
		{name: "no surviving source", order: []int{3}, disabled: []int{0, 1, 2}, want: 3},
		{name: "next source disabled", disabled: []int{2}, want: 3},
		{name: "all sources disabled", disabled: []int{0, 1, 2, 3}, want: -1},
		{name: "empty configuration", order: []int{}, want: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRefreshFixture(t, 4)
			// Start with three sources for the insertion case; the fourth file already exists.
			if tc.name == "source inserted before next" || tc.name == "no surviving source" {
				f.a.baseConfig.Collectors.Site.Sources = f.sources[:3]
			}
			f.refresh(t)
			for i := 0; i < 2; i++ {
				records := f.collect(t)
				if len(records) != 1 || records[0].Key != f.sources[i].Site {
					t.Fatalf("initial collection %d: %+v", i, records)
				}
			}
			if tc.order != nil {
				next := []SiteSource{}
				for _, i := range tc.order {
					next = append(next, f.sources[i])
				}
				// Exercise supported explicit local source configuration, after remembered descriptors merge.
				raw, err := json.Marshal(struct {
					Sources []SiteSource `json:"sources"`
				}{next})
				if err != nil {
					t.Fatal(err)
				}
				f.a.baseConfig.localCollectors = map[string]json.RawMessage{"site": raw}
			}
			f.mu.Lock()
			for _, i := range tc.disabled {
				f.policy.DisabledSources = append(f.policy.DisabledSources, SourceID(f.sources[i].Path))
			}
			f.mu.Unlock()
			f.refresh(t)
			records := f.collect(t)
			if tc.want < 0 {
				if len(records) != 0 {
					t.Fatalf("disabled/empty sources collected: %+v", records)
				}
			} else if len(records) != 1 || records[0].Key != f.sources[tc.want].Site {
				t.Fatalf("next source after refresh: %+v, want %s", records, f.sources[tc.want].Site)
			}
			// Even refreshing on every tick must give all remaining sources a turn.
			seen := map[string]int{}
			for _, record := range records {
				seen[record.Key]++
			}
			for tick := 0; tick < 2*len(f.a.site.cfg.Sources); tick++ {
				f.refresh(t)
				for _, record := range f.collect(t) {
					seen[record.Key]++
				}
			}
			for _, src := range f.a.site.cfg.Sources {
				if seen[src.Site] < 2 {
					t.Errorf("%s not fairly visited after config change: %v", src.Site, seen)
				}
			}
		})
	}
}

func TestSiteRefreshDisableReenable(t *testing.T) {
	f := newRefreshFixture(t, 2)
	f.refresh(t)
	if got := f.collect(t); len(got) != 1 {
		t.Fatalf("initial collection: %+v", got)
	}
	f.mu.Lock()
	f.policy.Collectors = map[string]bool{"site": false}
	f.mu.Unlock()
	f.refresh(t)
	if f.a.site != nil {
		t.Fatal("disabled collector remains active")
	}
	f.mu.Lock()
	f.policy.Collectors = map[string]bool{"site": true}
	f.mu.Unlock()
	f.refresh(t)
	for i := 0; i < 2; i++ {
		got := f.collect(t)
		if len(got) != 1 || got[0].Key != f.sources[i].Site {
			t.Fatalf("reenabled collection %d: %+v", i, got)
		}
	}
}

func TestSiteRefreshRotationOffsetsAndNoDuplicates(t *testing.T) {
	f := newRefreshFixture(t, 1)
	path := f.sources[0].Path
	if err := os.WriteFile(path, []byte(strings.Repeat(refreshTestLine, 3)), 0600); err != nil {
		t.Fatal(err)
	}
	f.refresh(t)
	var bytes, requests uint64
	offsets := map[[2]uint64]int64{}
	collect := func() {
		for _, c := range f.collect(t) {
			bytes += c.TX
			requests += c.Site.Requests
		}
		for _, cursor := range f.a.site.Offsets() {
			id := [2]uint64{cursor.Dev, cursor.Inode}
			if cursor.Offset < offsets[id] {
				t.Fatalf("inode %v offset regressed: %d < %d", id, cursor.Offset, offsets[id])
			}
			offsets[id] = cursor.Offset
		}
	}
	collect()
	if bytes != 100 || requests != 1 {
		t.Fatalf("initial traffic: %d bytes/%d requests", bytes, requests)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat(refreshTestLine, 2)), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		file := f.a.site.files[path]
		f.refresh(t)
		if f.a.site.files[path] != file {
			t.Fatal("refresh lost ownership of current open file")
		}
		collect()
	}
	if bytes != 500 || requests != 5 {
		t.Fatalf("rotation plus refresh: %d bytes/%d requests, want exactly 500/5", bytes, requests)
	}
}
