package store

import (
	"context"
	"database/sql"
	"net/netip"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

func ensureIP(ctx context.Context, tx *sql.Tx, address string) (int64, error) {
	var packed []byte
	family := 0
	if a, e := netip.ParseAddr(address); e == nil {
		a = a.Unmap().WithZone("")
		address = a.String()
		b := a.As16()
		packed = b[:]
		family = 6
		if a.Is4() {
			family = 4
		}
	}
	if _, e := tx.ExecContext(ctx, `INSERT INTO ips(address,packed,family) VALUES(?,?,?) ON CONFLICT(address) DO NOTHING`, address, packed, family); e != nil {
		return 0, e
	}
	var id int64
	e := tx.QueryRowContext(ctx, `SELECT id FROM ips WHERE address=?`, address).Scan(&id)
	return id, e
}
func ingestSite(ctx context.Context, tx *sql.Tx, host int64, r *proto.Record, ipID int64) error {
	if _, e := tx.ExecContext(ctx, `INSERT INTO source_coverage(host_id,site,first_ts,last_ts) VALUES(?,?,?,?) ON CONFLICT(host_id,site) DO UPDATE SET first_ts=MIN(first_ts,excluded.first_ts),last_ts=MAX(last_ts,excluded.last_ts)`, host, r.Dim1, r.TS-60, r.TS); e != nil {
		return e
	}
	m := r.Site
	if m == nil {
		m = &proto.SiteMetrics{IPSource: "unknown"}
	}
	known := 0
	if m.RXKnown {
		known = 1
	}
	for _, table := range []string{"site_minute", "site_hour"} {
		step := int64(60)
		if table == "site_hour" {
			step = 3600
		}
		ts := (r.TS - 1) / step * step
		_, e := tx.ExecContext(ctx, `INSERT INTO `+table+`(host_id,ts,site,ip_id,status,rx_known,ip_source,rx,tx,requests) VALUES(?,?,?,?,?,?,?,?,?,?)
   ON CONFLICT(host_id,ts,site,ip_id,status,rx_known,ip_source) DO UPDATE SET rx=rx+excluded.rx,tx=tx+excluded.tx,requests=requests+excluded.requests`, host, ts, r.Dim1, ipID, m.Status, known, m.IPSource, r.RX, r.TX, m.Requests)
		if e != nil {
			return e
		}
	}
	ts := (r.TS - 1) / 86400 * 86400
	if _, e := tx.ExecContext(ctx, `INSERT INTO site_day(host_id,ts,site,status,rx_known,rx,tx,requests) VALUES(?,?,?,?,?,?,?,?)
 ON CONFLICT(host_id,ts,site,status,rx_known) DO UPDATE SET rx=rx+excluded.rx,tx=tx+excluded.tx,requests=requests+excluded.requests`, host, ts, r.Dim1, m.Status, known, r.RX, r.TX, m.Requests); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO sites(site,first_seen,last_seen) VALUES(?,?,?) ON CONFLICT(site) DO UPDATE SET first_seen=MIN(first_seen,excluded.first_seen),last_seen=MAX(last_seen,excluded.last_seen)`, r.Dim1, r.TS, r.TS)
	return e
}
