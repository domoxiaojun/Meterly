package geo

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func copyFixture(t *testing.T, dir string) {
	t.Helper()
	for _, f := range []string{CityFile, ASNFile} {
		b, err := os.ReadFile(filepath.Join("testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLookupFixture(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, dir)
	d := Open(dir)
	defer d.Close()
	got := d.Lookup("::ffff:198.51.100.7")
	if got.CC != "CN" || got.Country != "中国" || got.City != "Shenzhen" || got.Region != "Guangdong" || got.ASN != 64501 || got.Org != "Example Mobile CN" {
		t.Fatalf("lookup: %+v", got)
	}
	if v := d.Lookup("2001:db8::1"); v.CC != "DE" || v.ASN != 64504 {
		t.Fatalf("v6: %+v", v)
	}
	for ip, scope := range map[string]string{"10.1.2.3": "内网", "127.0.0.1": "回环", "100.66.1.2": "CGNAT/NetBird", "192.168.1.10": "内网", "fe80::1": "保留"} {
		if v := d.Lookup(ip); v.Scope != scope || v.CC != "" {
			t.Fatalf("%s: %+v", ip, v)
		}
	}
	if v := d.Lookup("~other"); v != (Info{}) {
		t.Fatalf("非 IP 应为空: %+v", v)
	}
	if v := d.Lookup("8.8.8.8"); v.CC != "" || v.Scope != "" {
		t.Fatalf("fixture 外的公网 IP 应查不到: %+v", v)
	}
	st := d.Status()
	if !st.City.Loaded || !st.ASN.Loaded || st.City.Type != "DBIP-City-Lite" {
		t.Fatalf("status %+v", st)
	}
}

func TestNoDBDegrades(t *testing.T) {
	d := Open(t.TempDir())
	if v := d.Lookup("192.0.2.1"); v.CC != "" || v.Scope != "保留" {
		t.Fatalf("无库时: %+v", v)
	}
	// 损坏文件:打开失败不 panic,降级
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, CityFile), []byte("not an mmdb"), 0o600)
	d2 := Open(dir)
	if d2.Status().City.Loaded || d2.Status().LastErr == "" {
		t.Fatalf("损坏文件应未加载并记录错误: %+v", d2.Status())
	}
}

func gz(t *testing.T, path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func TestUpdaterDownloadAndMonthly(t *testing.T) {
	city, asn := gz(t, "testdata/"+CityFile), gz(t, "testdata/"+ASNFile)
	var hits atomic.Int32
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		paths = append(paths, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "-2026-11.mmdb.gz"):
			http.NotFound(w, r) // 月初当月库未发布
		case strings.HasPrefix(r.URL.Path, "/dbip-city-lite-"):
			w.Write(city)
		case strings.HasPrefix(r.URL.Path, "/dbip-asn-lite-"):
			w.Write(asn)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	d := Open(dir)
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	u := &Updater{DB: d, BaseURL: srv.URL, Now: func() time.Time { return now }}
	if err := u.UpdateOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := d.Lookup("192.0.2.9"); v.CC != "JP" || v.ASN != 64500 {
		t.Fatalf("下载后查询: %+v (%v)", v, paths)
	}
	if fi, _ := os.Stat(filepath.Join(dir, CityFile)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("库文件权限 %o", fi.Mode().Perm())
	}
	// fixture 的 build 时间是生成时刻(≥ 2026-11),同月再次检查不下载
	n := hits.Load()
	if err := u.UpdateOnce(context.Background()); err != nil || hits.Load() != n {
		t.Fatalf("同月不应重复下载: err=%v hits %d→%d", err, n, hits.Load())
	}
	if !needs(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), now) || needs(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), now) || !needs(time.Time{}, now) {
		t.Fatal("needs 判断错误")
	}
}

func TestUpdaterFailureKeepsOld(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, dir)
	d := Open(dir)
	mode := "500"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "500":
			http.Error(w, "boom", 500)
		case "garbage":
			w.Write([]byte("<html>not gzip</html>"))
		case "badmmdb":
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			zw.Write(bytes.Repeat([]byte{0xab}, 4096))
			zw.Close()
			w.Write(buf.Bytes())
		}
	}))
	defer srv.Close()
	// 让库看起来"过期",强制尝试下载
	u := &Updater{DB: d, BaseURL: srv.URL, Now: func() time.Time { return time.Now().AddDate(0, 2, 0) }}
	for _, m := range []string{"500", "garbage", "badmmdb"} {
		mode = m
		if err := u.UpdateOnce(context.Background()); err == nil {
			t.Fatalf("%s: 应返回错误", m)
		}
		if v := d.Lookup("203.0.113.200"); v.CC != "SG" {
			t.Fatalf("%s: 失败后旧库应继续可用: %+v", m, v)
		}
		if d.Status().LastErr == "" {
			t.Fatalf("%s: 应记录错误", m)
		}
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("残留临时文件 %s", e.Name())
		}
	}
	// 完全不可达(连接拒绝)
	u2 := &Updater{DB: Open(t.TempDir()), BaseURL: "http://127.0.0.1:1", Client: &http.Client{Timeout: 2 * time.Second}}
	if err := u2.UpdateOnce(context.Background()); err == nil {
		t.Fatal("不可达应报错")
	}
}

// TestRealDBIP 真实 DB-IP 库(可选):METERLY_GEO_REAL_DIR=/path/to/dir(含 dbip-*-lite.mmdb;也接受 TRAFFICSTAT_GEO_REAL_DIR)。
func TestRealDBIP(t *testing.T) {
	dir := os.Getenv("METERLY_GEO_REAL_DIR")
	if dir == "" {
		dir = os.Getenv("TRAFFICSTAT_GEO_REAL_DIR")
	}
	if dir == "" {
		t.Skip("未设置 METERLY_GEO_REAL_DIR")
	}
	d := Open(dir)
	defer d.Close()
	if v := d.Lookup("1.1.1.1"); v.CC != "AU" || v.ASN != 13335 {
		t.Fatalf("1.1.1.1: %+v", v)
	}
	if v := d.Lookup("114.114.114.114"); v.CC != "CN" || v.Country != "中国" {
		t.Fatalf("114: %+v", v)
	}
}

func BenchmarkLookupCached(b *testing.B) {
	dir := b.TempDir()
	for _, f := range []string{CityFile, ASNFile} {
		buf, _ := os.ReadFile(filepath.Join("testdata", f))
		os.WriteFile(filepath.Join(dir, f), buf, 0o600)
	}
	d := Open(dir)
	for i := 0; i < b.N; i++ {
		d.Lookup("198.51.100.7")
	}
}
