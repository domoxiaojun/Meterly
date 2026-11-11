package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

func TestParseCombinedAndMainXFF(t *testing.T) {
	combined, err := CompileLogFormat("combined", FormatCombined)
	if err != nil {
		t.Fatal(err)
	}
	main, err := CompileLogFormat("main", FormatMainXFF)
	if err != nil {
		t.Fatal(err)
	}
	// host-a default access.log (combined, no extra field)
	line := `2001:19f0:ac01:1ad7:5400:6ff:fe72:b8f2 - - [10/Nov/2026:00:24:48 +0000] "GET / HTTP/1.1" 200 409 "-" "Mozilla/5.0 (compatible; CyberConvoyScout/1.0; +https://scout.cyberconvoy.co)"`
	lg := combined.Parse(line)
	if lg == nil {
		t.Fatal("combined parse failed")
	}
	if lg.IP != "2001:19f0:ac01:1ad7:5400:6ff:fe72:b8f2" || lg.Status != 200 || lg.Sent != 409 {
		t.Fatalf("got %+v", lg)
	}
	if lg.Time.UTC().Format(time.RFC3339) != "2026-11-10T00:24:48Z" {
		t.Fatalf("time %s", lg.Time)
	}
	// 1Panel main (combined + xff). same line should also parse with main? trailing missing field fails.
	if main.Parse(line) != nil {
		t.Fatal("main should not parse combined-without-xff")
	}
	// host-b git.example.com / cpa
	xff := `198.51.100.10 - - [17/Apr/2026:11:08:37 +0800] "GET /v1/models HTTP/1.1" 200 337 "-" "-" "203.0.113.50"`
	lg = main.Parse(xff)
	if lg == nil || lg.IP != "198.51.100.10" || lg.Sent != 337 || lg.Status != 200 {
		t.Fatalf("main parse: %+v", lg)
	}
	if lg.Time.UTC().Hour() != 3 { // +0800 → 03:08 UTC
		t.Fatalf("tz convert hour=%d", lg.Time.UTC().Hour())
	}
	// funmedia-style combined + xff "-"
	fm := `203.0.113.80 - - [10/Nov/2026:00:09:50 +0000] "GET / HTTP/1.1" 302 145 "-" "Mozilla/5.0 (iPhone; CPU iPhone OS 13_2_3 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/13.0.3 Mobile/15E148 Safari/604.1" "-"`
	lg = main.Parse(fm)
	if lg == nil || lg.Status != 302 || lg.Sent != 145 {
		t.Fatalf("funmedia: %+v", lg)
	}
	// autodetect
	if DetectFormat(BuiltinFormats(), xff).Name != "main" {
		t.Fatalf("detect xff: %v", DetectFormat(BuiltinFormats(), xff))
	}
	if DetectFormat(BuiltinFormats(), line).Name != "combined" && DetectFormat(BuiltinFormats(), line).Name != "main" {
		// combined is tried after main; main fails, combined works
		t.Fatalf("detect combined got %v", DetectFormat(BuiltinFormats(), line))
	}
}

func TestParseIPv4MappedAndBadLine(t *testing.T) {
	f, _ := CompileLogFormat("c", FormatCombined)
	lg := f.Parse(`::ffff:192.0.2.10 - - [11/Nov/2026:08:00:00 +0800] "GET / HTTP/1.1" 200 12 "-" "-"`)
	if lg == nil || lg.IP != "192.0.2.10" {
		t.Fatalf("mapped: %+v", lg)
	}
	if f.Parse("not a log line") != nil {
		t.Fatal("expected fail")
	}
	if StatusClass(204) != "2xx" || StatusClass(301) != "3xx" || StatusClass(404) != "4xx" || StatusClass(502) != "5xx" || StatusClass(0) != "other" {
		t.Fatal("status class")
	}
}

func TestLogRotationAndTruncate(t *testing.T) {
	dir := t.TempDir()
	logp := filepath.Join(dir, "access.log")
	write := func(s string) {
		if err := os.WriteFile(logp, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	line := func(n int, status, sent string) string {
		return `192.0.2.` + itoa(int64(n)) + ` - - [11/Nov/2026:12:00:0` + itoa(int64(n)) + ` +0000] "GET / HTTP/1.1" ` + status + ` ` + sent + ` "-" "-" "x"` + "\n"
	}
	write(line(1, "200", "100") + line(2, "200", "200"))
	acc := NewSiteAcc(SiteConfig{
		Enabled: true, FromStart: true, AllowDirs: []string{dir},
		Sources: []SiteSource{{Path: logp, Site: "demo.example", Format: "main"}},
	}, nil)
	recs, st, err := acc.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if st.Used != 2 {
		t.Fatalf("used %d lines %+v recs=%d", st.Used, st, len(recs))
	}
	var sent uint64
	for _, c := range recs {
		if c.Kind == proto.KindSite {
			sent += c.TX
		}
	}
	if sent != 300 {
		t.Fatalf("sent %d recs=%+v", sent, recs)
	}
	// 追加
	f, err := os.OpenFile(logp, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line(3, "404", "50")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	recs, st, err = acc.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if st.Used != 1 {
		t.Fatalf("append used %d (should be only new line)", st.Used)
	}
	// 截断
	write(line(4, "200", "999"))
	recs, st, err = acc.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if st.Used != 1 {
		t.Fatalf("truncate used %d", st.Used)
	}
	found := false
	for _, c := range recs {
		if c.Kind == proto.KindSite && c.TX == 999 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing truncated line: %+v", recs)
	}
	// inode 变化(模拟 logrotate:改名再新建)
	if err := os.Rename(logp, logp+".1"); err != nil {
		t.Fatal(err)
	}
	write(line(5, "200", "7"))
	recs, st, err = acc.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if st.Used != 1 {
		t.Fatalf("inode change used %d", st.Used)
	}
	found = false
	for _, c := range recs {
		if c.Kind == proto.KindSite && c.TX == 7 && c.Key == "demo.example" {
			found = true
		}
	}
	if !found {
		t.Fatalf("inode recs %+v", recs)
	}
}

func TestFirstReadStartsAtEOF(t *testing.T) {
	dir := t.TempDir()
	logp := filepath.Join(dir, "access.log")
	os.WriteFile(logp, []byte(`1.1.1.1 - - [11/Nov/2026:12:00:00 +0000] "GET / HTTP/1.1" 200 100 "-" "-" "-"\n`), 0o644)
	acc := NewSiteAcc(SiteConfig{
		Enabled: true, FromStart: false, AllowDirs: []string{dir},
		Sources: []SiteSource{{Path: logp, Site: "s", Format: "main"}},
	}, nil)
	recs, st, err := acc.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if st.Used != 0 || len(recs) != 0 {
		t.Fatalf("first run should skip history, used=%d recs=%d", st.Used, len(recs))
	}
}

func TestSymlinkEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	allow := filepath.Join(dir, "allow")
	secret := filepath.Join(dir, "secret")
	os.Mkdir(allow, 0o755)
	os.WriteFile(secret, []byte("nope\n"), 0o600)
	link := filepath.Join(allow, "access.log")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	acc := NewSiteAcc(SiteConfig{
		Enabled: true, FromStart: true, AllowDirs: []string{allow},
		Sources: []SiteSource{{Path: link, Site: "s", Format: "combined"}},
	}, nil)
	_, st, err := acc.Collect()
	if err != nil && !strings.Contains(err.Error(), "允许") {
		// Collect skips disallowed files (Skipped++) rather than hard error
		_ = err
	}
	if st.Used != 0 {
		t.Fatalf("should not read escaped symlink, used=%d err=%v", st.Used, err)
	}
}

func TestSiteTopNOther(t *testing.T) {
	dir := t.TempDir()
	logp := filepath.Join(dir, "a.log")
	var b strings.Builder
	for i := 1; i <= 5; i++ {
		b.WriteString("10.0.0." + itoa(int64(i)) + " - - [11/Nov/2026:12:00:00 +0000] \"GET / HTTP/1.1\" 200 " + itoa(int64(i*100)) + " \"-\" \"-\" \"-\"\n")
	}
	os.WriteFile(logp, []byte(b.String()), 0o644)
	acc := NewSiteAcc(SiteConfig{
		Enabled: true, FromStart: true, TopIPs: 2, AllowDirs: []string{dir},
		Sources: []SiteSource{{Path: logp, Site: "s", Format: "main"}},
	}, nil)
	recs, _, err := acc.Collect()
	if err != nil {
		t.Fatal(err)
	}
	var other, kept uint64
	nIP := 0
	for _, c := range recs {
		if c.Kind != proto.KindSite {
			continue
		}
		if c.Key2 == proto.DimOther {
			other += c.TX
		} else {
			kept += c.TX
			nIP++
		}
	}
	if nIP != 2 || kept != 500+400 || other != 300+200+100 {
		t.Fatalf("kept=%d n=%d other=%d recs=%+v", kept, nIP, other, recs)
	}
}

func TestParseLogFormatDirective(t *testing.T) {
	text := `
http {
    log_format main '$remote_addr - $remote_user [$time_local] "$request" '
                    '$status $body_bytes_sent "$http_referer" '
                    '"$http_user_agent" "$http_x_forwarded_for"';
    server {
        server_name git.example.com;
        access_log /www/sites/git.example.com/log/access.log main;
    }
}
`
	fmts := ParseLogFormats(text)
	if !strings.Contains(fmts["main"], "$http_x_forwarded_for") || !strings.Contains(fmts["main"], "$body_bytes_sent") {
		t.Fatalf("formats: %#v", fmts)
	}
	blocks := parseServerBlocks(text)
	if len(blocks) != 1 || blocks[0].Names[0] != "git.example.com" || blocks[0].Logs[0].Path != "/www/sites/git.example.com/log/access.log" {
		t.Fatalf("blocks %+v", blocks)
	}
	cands := MapContainerLogPath("/www/sites/git.example.com/log/access.log")
	if len(cands) < 2 || cands[1] != "/opt/1panel/www/sites/git.example.com/log/access.log" {
		t.Fatalf("map %v", cands)
	}
}
