// Local browser fixture: ephemeral SQLite file, loopback only, no scheduler or external services.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/server"
	"github.com/domoxiaojun/Meterly/internal/store"
	"github.com/domoxiaojun/Meterly/web"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	dir, e := os.MkdirTemp("", "meterly-browser-")
	if e != nil {
		panic(e)
	}
	defer os.RemoveAll(dir)
	config := filepath.Join(dir, "config.json")
	body, _ := json.Marshal(map[string]any{"listen": "127.0.0.1:18787", "db_path": filepath.Join(dir, "db.sqlite"), "tz": "Asia/Singapore", "geo": map[string]any{"auto_update": false}, "maintenance": map[string]any{"backups": false}})
	os.WriteFile(config, body, 0600)
	cfg, e := server.LoadConfig(config)
	if e != nil {
		panic(e)
	}
	st, e := store.Open(cfg.DBPath)
	if e != nil {
		panic(e)
	}
	defer st.Close()
	now := time.Date(2026, 11, 11, 12, 30, 0, 0, time.FixedZone("SGT", 8*3600))
	ctx := context.Background()
	for _, name := range []string{"server-a", "server-b"} {
		tok, e := st.CreateHost(ctx, name, store.HostOpts{BillingIface: "ens3", QuotaBytes: 1 << 40})
		if e != nil {
			panic(e)
		}
		h, _ := st.HostByToken(ctx, tok)
		var records []proto.Record
		for _, day := range []int{9, 10, 11} {
			for i := 1; i <= 137; i++ {
				for hour := 0; hour < 12; hour++ {
					status := 200
					if i%10 == 0 {
						status = 503
					}
					ts := time.Date(2026, 11, day, hour, 1, 0, 0, time.FixedZone("SGT", 8*3600)).Unix()
					records = append(records, proto.Record{Seq: int64(len(records) + 1), TS: ts, Kind: "site", Dim1: "example.com", Dim2: fmt.Sprintf("198.51.100.%d", i), RX: uint64(i), TX: uint64(i * 1000), Site: &proto.SiteMetrics{Status: status, Requests: 1, RXKnown: true, IPSource: "remote_addr"}})
				}
			}
		}
		records = append(records, proto.Record{Seq: int64(len(records) + 1), TS: now.Unix() - 60, Kind: "iface", Dim1: "ens3", RX: 12345, TX: 98765})
		meta := proto.Meta{Protocol: 2, InstanceID: name, AgentVersion: "v2.0.0", Inventory: &proto.Inventory{DefaultInterface: "ens3", Interfaces: []string{"ens3", "ens4"}, Sources: []proto.Source{{ID: "test", Site: "example.com", Path: "/var/log/nginx/access.log", Available: true, IPMode: "remote_addr"}}, OS: "linux", Arch: "amd64"}, Collectors: map[string]proto.CollectorHealth{"site": {Enabled: true, LastOK: now.Unix()}, "netdev": {Enabled: true, LastOK: now.Unix()}}}
		for i := 0; i < len(records); i += 4000 {
			if _, e = st.Ingest(ctx, h, time.UTC, meta, records[i:min(i+4000, len(records))], "", now); e != nil {
				panic(e)
			}
		}
	}
	srv := server.New(cfg, st, web.FS(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer srv.Close()
	srv.Now = func() time.Time { return now }
	fmt.Println("UI fixture listening at http://127.0.0.1:18787")
	if e := http.ListenAndServe(cfg.Listen, srv.Handler()); e != nil {
		panic(e)
	}
}
