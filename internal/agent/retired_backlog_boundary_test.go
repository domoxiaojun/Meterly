package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetiredPartialLineKeepsBacklogAndOffset(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, "access.log")
	retired := active + ".1"
	line := "192.0.2.1 - - [11/Nov/2026:04:00:00 +0000] \"GET / HTTP/1.1\" 200 100 \"-\" \"-\""
	if err := os.WriteFile(active, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retired, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(retired)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	dev, ino := inodeOf(fi)
	key := "retired:partial"
	offsets := map[string]LogOffset{key: {Path: active, SourcePath: active, Dev: dev, Inode: ino, Retired: true, EmptyReads: 1}}
	a := NewSiteAcc(SiteConfig{Enabled: true, FromStart: true, MaxReadBytes: 1024, AllowDirs: []string{dir}, Sources: []SiteSource{{Path: active, Site: "partial.test", Format: "combined"}}}, offsets)
	a.files[key] = f
	defer a.Close()
	for i := 0; i < 3; i++ {
		records, stats, err := a.Collect()
		if err != nil {
			t.Fatal(err)
		}
		cursor, ok := a.offsets[key]
		if !ok || cursor.Offset != 0 || cursor.EmptyReads != 0 || len(records) != 0 || stats.Backlogged == 0 {
			t.Fatalf("partial retired line lost state: cursor=%+v exists=%v records=%v stats=%+v", cursor, ok, records, stats)
		}
	}
	writer, err := os.OpenFile(retired, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.WriteString("\n"); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	writer.Close()
	records, stats, err := a.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].TX != 100 || stats.Backlogged != 0 {
		t.Fatalf("completed retired line was lost/duplicated: records=%v stats=%+v", records, stats)
	}
	for i := 0; i < 2; i++ {
		records, stats, err = a.Collect()
		if err != nil || len(records) != 0 || stats.Backlogged != 0 {
			t.Fatalf("EOF repeat: records=%v stats=%+v err=%v", records, stats, err)
		}
	}
	if _, ok := a.offsets[key]; ok {
		t.Fatal("confirmed EOF did not retire old descriptor")
	}
}
