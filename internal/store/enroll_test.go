package store

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnrollCreateRedeem(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC)
	code, rec, err := st.CreateEnroll(ctx, "host-new", EnrollOpts{TTL: 24 * time.Hour, MaxUses: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if code == "" || rec.ID == 0 || rec.HostName != "host-new" {
		t.Fatalf("bad enroll: %+v code empty=%v", rec, code == "")
	}
	if HashEnroll(code) == code || len(code) < 10 {
		t.Fatal("code should be random tse_…")
	}
	tok, host, err := st.RedeemEnroll(ctx, code, "host-new", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" || host.Name != "host-new" {
		t.Fatalf("token/host: %q %+v", tok, host)
	}
	if _, err := st.HostByToken(ctx, tok); err != nil {
		t.Fatal("token should work", err)
	}
	// 第二次核销失败
	if _, _, err := st.RedeemEnroll(ctx, code, "host-new", now.Add(2*time.Minute)); err != ErrEnrollInvalid {
		t.Fatalf("expected invalid, got %v", err)
	}
	// 错码
	if _, _, err := st.RedeemEnroll(ctx, "tse_nope", "", now); err != ErrEnrollInvalid {
		t.Fatalf("expected invalid, got %v", err)
	}
}

func TestEnrollExpiryAndNameMismatch(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC)
	code, _, err := st.CreateEnroll(ctx, "box1", EnrollOpts{TTL: time.Hour}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RedeemEnroll(ctx, code, "box2", now); err == nil {
		t.Fatal("name mismatch should fail")
	}
	if _, _, err := st.RedeemEnroll(ctx, code, "box1", now.Add(2*time.Hour)); err != ErrEnrollInvalid {
		t.Fatalf("expired: %v", err)
	}
}

func TestEnrollExistingHostRequiresReissue(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	old, err := st.CreateHost(ctx, "host-a", HostOpts{})
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := st.CreateEnroll(ctx, "host-a", EnrollOpts{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RedeemEnroll(ctx, code, "", now); err != ErrEnrollExists {
		t.Fatalf("want exists, got %v", err)
	}
	// 旧 token 仍有效
	if _, err := st.HostByToken(ctx, old); err != nil {
		t.Fatal("existing token must keep working", err)
	}
	code2, _, err := st.CreateEnroll(ctx, "host-a", EnrollOpts{Reissue: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := st.RedeemEnroll(ctx, code2, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.HostByToken(ctx, old); err != ErrNotFound {
		t.Fatal("old token should be rotated away")
	}
	if _, err := st.HostByToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
}

func TestRejectOldDatabaseWithoutModification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("CREATE TABLE settings(k TEXT PRIMARY KEY,v TEXT); INSERT INTO settings VALUES('schema_version','3'); CREATE TABLE old_data(v TEXT); INSERT INTO old_data VALUES('keep')"); e != nil {
		t.Fatal(e)
	}
	db.Close()
	before, _ := os.ReadFile(path)
	if st, e := Open(path); e == nil {
		st.Close()
		t.Fatal("old schema must be rejected")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("old database was modified")
	}
	if _, e = os.Stat(path + "-wal"); e == nil {
		t.Fatal("created WAL for rejected database")
	}
}

func TestSiteCompactPerSite(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	tok, err := st.CreateHost(ctx, "h", HostOpts{})
	if err != nil {
		t.Fatal(err)
	}
	h, _ := st.HostByToken(ctx, tok)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	hour := now.Unix() - 3600
	// 一个站点 5 个 IP,topN=2
	var recs []struct {
		ip string
		tx int64
	}
	for i, tx := range []int64{100, 90, 80, 70, 60} {
		recs = append(recs, struct {
			ip string
			tx int64
		}{ip: "10.0.0." + string(rune('1'+i)), tx: tx})
	}
	// 直接插小时表
	for i, r := range []struct {
		ip string
		tx int64
	}{{"10.0.0.1", 500}, {"10.0.0.2", 400}, {"10.0.0.3", 30}, {"10.0.0.4", 20}, {"10.0.0.5", 10}} {
		_, _ = i, r
		if _, err := st.W.Exec(`INSERT INTO fact_hour(host_id,ts,kind,dim1,dim2,rx,tx) VALUES(?,?,?,?,?,?,?)`,
			h.ID, hour, "site", "a.example", r.ip, 0, r.tx); err != nil {
			t.Fatal(err)
		}
	}
	// 另一站点也有 3 个 IP,不应和第一个混在一起排
	for _, r := range []struct {
		ip string
		tx int64
	}{{"10.1.0.1", 9}, {"10.1.0.2", 8}, {"10.1.0.3", 7}} {
		if _, err := st.W.Exec(`INSERT INTO fact_hour(host_id,ts,kind,dim1,dim2,rx,tx) VALUES(?,?,?,?,?,?,?)`,
			h.ID, hour, "site", "b.example", r.ip, 0, r.tx); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.Maintain(ctx, Retention{}, time.Unix(hour+1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 { // a: 3 merged, b: 1 merged
		t.Fatalf("merged %d, want 4", n)
	}
	rows, tot, err := st.SiteIPs(ctx, "fact_hour", []int64{h.ID}, "a.example", hour, hour+3600, 20)
	if err != nil {
		t.Fatal(err)
	}
	if tot.TX != 500+400+30+20+10 {
		t.Fatalf("total tx %d", tot.TX)
	}
	var other, kept int64
	for _, r := range rows {
		if r.Special == "other" {
			other = r.TX
		} else {
			kept += r.TX
		}
	}
	if other != 0 {
		t.Fatalf("other %d", other)
	}
	if kept != 500+400+30+20+10 {
		t.Fatalf("kept %d", kept)
	}
}
