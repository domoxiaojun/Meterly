package e2e

import (
	"context"
	"encoding/pem"
	"github.com/domoxiaojun/Meterly/internal/agent"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/server"
	"github.com/domoxiaojun/Meterly/internal/store"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHTTPSRegistrationAndPush(t *testing.T) {
	dir := t.TempDir()
	st, e := store.Open(filepath.Join(dir, "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	ctx := context.Background()
	cfg := &server.Config{Listen: "127.0.0.1:0", DBPath: filepath.Join(dir, "db")}
	if e = cfgFinish(cfg); e != nil {
		t.Fatal(e)
	}
	srv := server.New(cfg, st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer srv.Close()
	ts := httptest.NewTLSServer(srv.Handler())
	defer ts.Close()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0600)
	code, _, e := st.CreateEnroll(ctx, "tls-host", store.EnrollOpts{TTL: time.Hour}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	auth, e := agent.RedeemEnroll(ctx, ts.URL, ca, false, code, "tls-host", time.Second*5)
	if e != nil {
		t.Fatal(e)
	}
	ac := &agent.Config{ServerURL: ts.URL, CAFile: ca, Token: auth.Token, StateDir: filepath.Join(dir, "agent"), BatchSize: 50, BufferMaxBytes: 1 << 20, HTTPTimeout: agent.Duration{Duration: time.Second * 5}}
	a, e := agent.New(ac, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	record := proto.Record{Seq: 1, TS: time.Now().Unix(), Kind: proto.KindSite, Dim1: "tls.test", Dim2: "2001:db8::1", TX: 123, Site: &proto.SiteMetrics{Status: 200, Requests: 1}}
	if _, e = a.Buffer().Append([]proto.Record{record}); e != nil {
		t.Fatal(e)
	}
	if n, e := a.Flush(ctx, 2); e != nil || n != 1 || a.Buffer().Count() != 0 {
		t.Fatalf("flush %d %v buffered %d", n, e, a.Buffer().Count())
	}
	h, _ := st.HostByToken(ctx, auth.Token)
	r, e := st.Analyze(ctx, store.AnalysisFilter{From: record.TS - 3600, To: record.TS + 60, HostIDs: []int64{h.ID}, Source: "site", Group: "ip", Table: "minute", Page: 1, PageSize: 50})
	if e != nil || r.Count != 1 || r.Totals.TX != 123 {
		t.Fatalf("%+v %v", r, e)
	}
}
