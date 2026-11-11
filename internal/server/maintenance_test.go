package server

import (
	"compress/gzip"
	"context"
	"github.com/domoxiaojun/Meterly/internal/store"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupRetentionAndReleaseValidation(t *testing.T) {
	s, st, _, _ := setup(t, "127.0.0.1:8787")
	var seq int
	var name, file string
	if e := st.R.QueryRow("PRAGMA database_list").Scan(&seq, &name, &file); e != nil {
		t.Fatal(e)
	}
	s.cfg.DBPath = file
	s.cfg.Maintenance.BackupDir = filepath.Join(t.TempDir(), "backups")
	s.cfg.Maintenance.Keep = 7
	now := time.Now()
	s.Now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		now = now.Add(time.Second)
		s.runJob(ctx, "backup")
	}
	matches, _ := filepath.Glob(filepath.Join(s.cfg.Maintenance.BackupDir, "*.db.gz"))
	if len(matches) != 7 {
		t.Fatalf("backups retained=%d", len(matches))
	}
	in, e := os.Open(matches[0])
	if e != nil {
		t.Fatal(e)
	}
	defer in.Close()
	gz, e := gzip.NewReader(in)
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	dest := filepath.Join(t.TempDir(), "restored.db")
	out, e := os.Create(dest)
	if e != nil {
		t.Fatal(e)
	}
	_, e = io.Copy(out, gz)
	out.Close()
	if e != nil {
		t.Fatal(e)
	}
	restored, e := store.Open(dest)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	hosts, e := restored.Hosts(ctx)
	if e != nil || len(hosts) != 1 {
		t.Fatalf("restore hosts=%d %v", len(hosts), e)
	}
	s.ReleaseGet = func(_ context.Context, url string) ([]byte, error) {
		if strings.HasSuffix(url, "SHA256SUMS") {
			return []byte(strings.Repeat("a", 64) + "  meterly-agent-linux-amd64\n"), nil
		}
		return []byte(`{"tag_name":"v2.0.0","draft":false,"prerelease":false}`), nil
	}
	asset, e := s.releaseFor(ctx, "v2.0.0", "amd64")
	if e != nil || asset.SHA256 != strings.Repeat("a", 64) {
		t.Fatal(asset, e)
	}
	if _, e = s.releaseFor(ctx, "../../evil", "amd64"); e == nil {
		t.Fatal("untrusted release tag accepted")
	}
	if e = s.checkRelease(ctx); e != nil {
		t.Fatal(e)
	}
}
