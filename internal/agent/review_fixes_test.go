package agent

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegressionEnrollmentRefusesHTTPSDowngrade(t *testing.T) {
	exposed := false
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		exposed = strings.Contains(string(b), "synthetic-enrollment-code")
		io.WriteString(w, `{"token":"synthetic-reply","host":"audit"}`)
	}))
	defer plain.Close()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusTemporaryRedirect)
	}))
	defer tlsServer.Close()
	cert, e := x509.ParseCertificate(tlsServer.TLS.Certificates[0].Certificate[0])
	if e != nil {
		t.Fatal(e)
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600)
	_, e = RedeemEnroll(context.Background(), tlsServer.URL, ca, false, "synthetic-enrollment-code", "audit", time.Second)
	t.Logf("allowHTTP=false, request downgraded=%v, returned error=%v", exposed, e)
	if exposed || e == nil {
		t.Fatal("HTTPS 307 redirect transmitted the enrollment secret over plaintext HTTP")
	}
}

func TestRegressionUpgradeRecovery(t *testing.T) {
	for _, state := range []string{"active", "activating", "failed", "inactive", "queued", "unknown"} {
		for _, requestOnly := range []bool{false, true} {
			t.Run(fmtTestName(state, requestOnly), func(t *testing.T) {
				reports := []UpgradeResult{}
				reject := true
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var v UpgradeResult
					json.NewDecoder(r.Body).Decode(&v)
					reports = append(reports, v)
					if reject {
						w.WriteHeader(503)
					} else {
						w.WriteHeader(200)
					}
				}))
				defer srv.Close()
				cfg := &Config{StateDir: t.TempDir(), ServerURL: srv.URL, Token: "test-only", AllowHTTP: true}
				cfg.applyDefaults()
				a, e := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
				if e != nil {
					t.Fatal(e)
				}
				defer a.Close()
				a.UpgradeExec = func(context.Context, string, ...string) ([]byte, error) {
					if state == "unknown" {
						return nil, errors.New("unavailable")
					}
					if state == "queued" {
						return []byte("ActiveState=inactive\nJob=42"), nil
					}
					return []byte("ActiveState=" + state + "\nJob="), nil
				}
				task := proto.UpgradeTask{ID: 42, Version: "v2.0.1", CreatedAt: time.Now().Add(-time.Hour).Unix()}
				request := filepath.Join(cfg.StateDir, "upgrade-request.json")
				result := filepath.Join(cfg.StateDir, "upgrade-result.json")
				b, _ := json.Marshal(task)
				os.WriteFile(request, b, 0600)
				if !requestOnly {
					b, _ = json.Marshal(UpgradeResult{ID: 42, Status: "running"})
					os.WriteFile(result, b, 0600)
				}
				old := time.Now().Add(-time.Hour)
				os.Chtimes(result, old, old)
				os.Chtimes(request, old, old)
				a.processUpgrade(context.Background(), &task)
				if _, e := os.Stat(request); e != nil {
					t.Fatal("unacknowledged request removed")
				}
				reject = false
				a.processUpgrade(context.Background(), &task)
				stopped := state == "failed" || state == "inactive"
				if stopped {
					if len(reports) == 0 || reports[len(reports)-1].Status != "failed" {
						t.Fatal(reports)
					}
					if _, e := os.Stat(result); !os.IsNotExist(e) {
						t.Fatal("terminal result not acknowledged")
					}
				} else {
					if _, e := os.Stat(request); e != nil {
						t.Fatal("live or unknown service was discarded")
					}
				}
			})
		}
	}
}
func fmtTestName(state string, requestOnly bool) string {
	if requestOnly {
		return state + "/request-only"
	}
	return state + "/running"
}
func TestRegressionCredentialOriginRedirects(t *testing.T) {
	for _, code := range []int{307, 308} {
		for _, same := range []bool{true, false} {
			exposed := false
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { exposed = true; w.Write([]byte("{}")) }))
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/done" {
					w.Write([]byte("{}"))
					return
				}
				dest := target.URL
				if same {
					dest = "/done"
				}
				http.Redirect(w, r, dest, code)
			}))
			cfg := &Config{StateDir: t.TempDir(), ServerURL: origin.URL, Token: "test-only", AllowHTTP: true}
			cfg.applyDefaults()
			a, e := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if e != nil {
				t.Fatal(e)
			}
			e = a.control(context.Background(), "POST", "/start", map[string]string{"test": "body"}, nil)
			if exposed || (same && e != nil) || (!same && e == nil) {
				t.Fatalf("code %d same %v exposed %v err %v", code, same, exposed, e)
			}
			a.Close()
			origin.Close()
			target.Close()
		}
	}
}
func TestRegressionPartialNetdevStillCollects(t *testing.T) {
	cfg := &Config{StateDir: t.TempDir(), ServerURL: "http://127.0.0.1", Token: "test-only", AllowHTTP: true}
	cfg.applyDefaults()
	a, e := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	p := filepath.Join(t.TempDir(), "netdev")
	os.WriteFile(p, []byte("wt0: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n"), 0600)
	a.netdev = &NetDev{Path: p, Ifaces: []string{"ens3", "wt0"}}
	a.BootID = func() string { return "boot" }
	a.health = map[string]proto.CollectorHealth{"netdev": {Enabled: true}}
	now := time.Now()
	a.Now = func() time.Time { return now }
	a.st.Ifaces["wt0"] = IfaceState{BootID: "boot", RX: 90, TX: 180, TS: now.Add(-time.Minute).Unix()}
	if n, e := a.Tick(context.Background()); e != nil || n != 1 {
		t.Fatalf("partial collect %d %v", n, e)
	}
	recs, e := a.buf.Pending(10)
	if e != nil || len(recs) != 1 || recs[0].RX != 10 || recs[0].TX != 20 {
		t.Fatal(recs, e)
	}
	if a.health["netdev"].Fails != 1 || !strings.Contains(a.health["netdev"].LastErr, "ens3") {
		t.Fatal(a.health)
	}
	a.netdev.Ifaces = []string{"wt0"}
	now = now.Add(time.Minute)
	a.Tick(context.Background())
	if a.health["netdev"].Fails != 0 {
		t.Fatal(a.health)
	}
}
