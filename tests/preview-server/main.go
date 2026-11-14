// preview-server serves synthetic data through the real Meterly API on loopback.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/server"
	"github.com/domoxiaojun/Meterly/internal/store"
)

func main() {
	dir, err := os.MkdirTemp("", "meterly-preview-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	configPath := filepath.Join(dir, "server.json")
	body, _ := json.Marshal(map[string]any{"listen": "127.0.0.1:18788", "db_path": filepath.Join(dir, "preview.db"), "tz": "Asia/Singapore", "geo": map[string]any{"auto_update": false}, "maintenance": map[string]any{"backups": false}})
	if err = os.WriteFile(configPath, body, 0600); err != nil {
		panic(err)
	}
	cfg, err := server.LoadConfig(configPath)
	if err != nil {
		panic(err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		panic(err)
	}
	defer st.Close()
	loc := time.FixedZone("SGT", 8*3600)
	now := time.Date(2026, 11, 14, 15, 0, 0, 0, loc)
	ctx := context.Background()
	names := []string{"edge-sg-01", "core-hk-01", "relay-us-01", "edge-jp-01", "backup-eu-01"}
	sites := []string{"api.example.com", "static.example.com", "media.example.com", "app.example.com", "docs.example.com", "status.example.com"}
	for hi, name := range names {
		token, err := st.CreateHost(ctx, name, store.HostOpts{BillingIface: "eth0", QuotaBytes: int64(1+hi) * (1 << 40)})
		if err != nil {
			panic(err)
		}
		host, err := st.HostByToken(ctx, token)
		if err != nil {
			panic(err)
		}
		var records []proto.Record
		add := func(r proto.Record) { r.Seq = int64(len(records) + 1); records = append(records, r) }
		for day := -13; day <= 0; day++ {
			start := time.Date(2026, 11, 14+day, 0, 0, 0, 0, loc)
			for hour := 0; hour < 24; hour++ {
				end := start.Add(time.Duration(hour+1) * time.Hour)
				if end.After(now) {
					break
				}
				wave := 0.6 + 0.6*math.Pow(math.Sin(float64(hour-4)*math.Pi/24), 2) + float64((hour*7+day*day)%11)/30
				for si, site := range sites {
					for ii := 1; ii <= 3; ii++ {
						ip := fmt.Sprintf("198.51.100.%d", hi*30+si*3+ii)
						requests := uint64(wave * float64((7-si)*83+(hi+1)*17+ii*43) * float64(105+day*2) / 100)
						status := 200
						if si == 0 && ii == 3 && hour%7 == 0 {
							status = 502
						}
						if si == 1 {
							status = 304
						}
						tx := requests * uint64((si+1)*13913)
						rx := requests * 540
						if si == 2 {
							rx = 0
						}
						add(proto.Record{TS: end.Unix(), PrevTS: end.Add(-time.Minute).Unix(), Kind: proto.KindSite, Dim1: site, Dim2: ip, RX: rx, TX: tx, Site: &proto.SiteMetrics{Status: status, Requests: requests, RXKnown: si != 2, IPSource: "remote_addr"}})
					}
				}
				proxy := uint64(wave * float64(hi+1) * 8388608)
				add(proto.Record{TS: end.Unix(), PrevTS: end.Add(-time.Hour).Unix(), Kind: proto.KindIface, Dim1: "eth0", RX: proxy * 2, TX: proxy * 8})
				add(proto.Record{TS: end.Unix(), PrevTS: end.Add(-time.Hour).Unix(), Kind: proto.KindSingbox, Dim1: fmt.Sprintf("203.0.113.%d", hi+1), Dim2: "mixed / proxy-in", RX: proxy / 3, TX: proxy})
				add(proto.Record{TS: end.Unix(), PrevTS: end.Add(-time.Hour).Unix(), Kind: proto.KindSingbox, Dim1: proto.DimUnattributed, RX: proxy / 8, TX: proxy / 2})
			}
		}
		meta := proto.Meta{Protocol: 2, InstanceID: "preview-" + name, AgentVersion: "v0.4.0", SentAt: now.Unix(), Inventory: &proto.Inventory{DefaultInterface: "eth0", Interfaces: []string{"eth0", "eth1"}, OS: "linux", Arch: "amd64", ScannedAt: now.Unix(), Detected: map[string]bool{"netdev": true, "site": true, "singbox": true}}, Collectors: map[string]proto.CollectorHealth{"site": {Enabled: true, LastOK: now.Unix()}, "netdev": {Enabled: true, LastOK: now.Unix()}, "singbox": {Enabled: true, LastOK: now.Unix()}}}
		for si, site := range sites {
			meta.Inventory.Sources = append(meta.Inventory.Sources, proto.Source{ID: fmt.Sprintf("preview-%d", si), Site: site, Path: "/var/log/nginx/" + site + ".log", Available: true, IPMode: "remote_addr"})
		}
		if hi == 3 {
			meta.Buffered = 128
		}
		for from := 0; from < len(records); from += 2000 {
			result, err := st.Ingest(ctx, host, loc, meta, records[from:min(from+2000, len(records))], "127.0.0.1", now)
			if err != nil || result.Rejected > 0 {
				panic(fmt.Sprintf("preview seed failed: %v, rejected=%d", err, result.Rejected))
			}
		}
		if hi == 4 {
			if _, err = st.W.ExecContext(ctx, `UPDATE hosts SET last_seen=? WHERE id=?`, now.Add(-15*time.Minute).Unix(), host.ID); err != nil {
				panic(err)
			}
		}
	}
	if _, err = st.W.ExecContext(ctx, `UPDATE ips SET country='SG',country_name='新加坡',asn=64500,org='示例网络' WHERE address LIKE '198.51.100.%'`); err != nil {
		panic(err)
	}
	srv := server.New(cfg, st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer srv.Close()
	srv.Now = func() time.Time { return now }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/preview", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"demo": true, "label": "演示工作区", "as_of": now.Unix(), "tz": "Asia/Singapore"})
	})
	mux.Handle("/", srv.Handler())
	httpServer := &http.Server{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-stopCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()
	fmt.Println("Meterly preview API: http://127.0.0.1:18788 (synthetic data)")
	if err = httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		panic(err)
	}
}
