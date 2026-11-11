package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

// Pair 收/发字节。
type Pair struct {
	RX int64 `json:"rx"`
	TX int64 `json:"tx"`
}

func (p Pair) Total() int64 { return p.RX + p.TX }

func inList(ids []int64) (string, []any) {
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i], args[i] = "?", id
	}
	return "(" + strings.Join(ph, ",") + ")", args
}

// HourRetention 小时表保留期;超出时查询回落到天表。
var HourRetention = 400 * 24 * time.Hour

// TableFor 选择查询表:范围在小时表保留期内用小时表(时区无关,精确),否则用天表。
func TableFor(from int64, now time.Time) string {
	if from >= now.Add(-HourRetention).Unix() {
		return "fact_hour"
	}
	return "fact_day"
}

// KindDim1Totals 返回某 kind 在 [from,to) 内按 dim1 汇总,如各网卡总量。
func (s *Store) KindDim1Totals(ctx context.Context, table string, ids []int64, kind string, from, to int64) (map[string]Pair, error) {
	in, args := inList(ids)
	rows, err := s.R.QueryContext(ctx, fmt.Sprintf(`SELECT dim1, SUM(rx), SUM(tx) FROM %s WHERE kind=? AND host_id IN %s AND ts>=? AND ts<? GROUP BY dim1`, table, in),
		append(append([]any{kind}, args...), from, to)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Pair{}
	for rows.Next() {
		var k string
		var p Pair
		if err := rows.Scan(&k, &p.RX, &p.TX); err != nil {
			return nil, err
		}
		out[k] = p
	}
	return out, rows.Err()
}

// Point 时间序列点。
type Point struct {
	TS int64 `json:"ts"`
	Pair
}

// HourSeries 返回 [from,to) 的小时序列(对 ids 求和),kind/dim1 指定(dim1 为空表示该 kind 全部)。
func (s *Store) HourSeries(ctx context.Context, ids []int64, kind, dim1 string, from, to int64) ([]Point, error) {
	return s.Series(ctx, "fact_hour", ids, Filter{Kind: kind, Dim1: dim1}, from, to)
}

// DaySeries 按 loc 的自然日汇总(小时表保留期内由小时表折算,精确;否则读天表)。
func (s *Store) DaySeries(ctx context.Context, ids []int64, kind, dim1 string, from, to int64, loc *time.Location, now time.Time) ([]Point, error) {
	var hours []Point
	var err error
	if TableFor(from, now) == "fact_hour" {
		hours, err = s.HourSeries(ctx, ids, kind, dim1, from, to)
	} else {
		in, args := inList(ids)
		q := fmt.Sprintf(`SELECT ts, SUM(rx), SUM(tx) FROM fact_day WHERE kind=? AND host_id IN %s AND ts>=? AND ts<?`, in)
		a := append(append([]any{kind}, args...), from, to)
		if dim1 != "" {
			q += ` AND dim1=?`
			a = append(a, dim1)
		}
		rows, qerr := s.R.QueryContext(ctx, q+` GROUP BY ts ORDER BY ts`, a...)
		if qerr != nil {
			return nil, qerr
		}
		defer rows.Close()
		for rows.Next() {
			var p Point
			if err := rows.Scan(&p.TS, &p.RX, &p.TX); err != nil {
				return nil, err
			}
			hours = append(hours, p)
		}
		err = rows.Err()
	}
	if err != nil {
		return nil, err
	}
	m := map[int64]*Point{}
	var keys []int64
	for _, h := range hours {
		d := DayStart(h.TS, loc)
		p := m[d]
		if p == nil {
			p = &Point{TS: d}
			m[d] = p
			keys = append(keys, d)
		}
		p.RX += h.RX
		p.TX += h.TX
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]Point, 0, len(keys))
	for _, k := range keys {
		out = append(out, *m[k])
	}
	return out, nil
}

// InboundShare 某 IP 在某入站上的流量。
type InboundShare struct {
	Inbound string `json:"inbound"`
	Relay   bool   `json:"relay"`
	Pair
}

// IPRow IP 排行行。
type IPRow struct {
	IP       string         `json:"ip"`
	Special  string         `json:"special,omitempty"` // unattributed / other
	Relay    bool           `json:"relay"`             // 主要入站是中转(direct)
	Inbounds []InboundShare `json:"inbounds"`
	Pair
	Total int64 `json:"total"`
}

// IsRelay 入站类型为 direct(端口转发/中转)时,来源 IP 是中转机。
func IsRelay(inbound string) bool {
	return strings.HasPrefix(inbound, "direct/") || inbound == "direct"
}

func (s *Store) singboxRows(ctx context.Context, table string, ids []int64, from, to int64) (map[[2]string]Pair, error) {
	in, args := inList(ids)
	rows, err := s.R.QueryContext(ctx, fmt.Sprintf(`SELECT dim1, dim2, SUM(rx), SUM(tx) FROM %s WHERE kind=? AND host_id IN %s AND ts>=? AND ts<? GROUP BY dim1, dim2`, table, in),
		append(append([]any{proto.KindSingbox}, args...), from, to)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]string]Pair{}
	for rows.Next() {
		var d1, d2 string
		var p Pair
		if err := rows.Scan(&d1, &d2, &p.RX, &p.TX); err != nil {
			return nil, err
		}
		out[[2]string{d1, d2}] = p
	}
	return out, rows.Err()
}

// TopIPs IP × 入站排行;超出 limit 的 IP 并入"其他",合计与 sing-box 总量一致。
func (s *Store) TopIPs(ctx context.Context, table string, ids []int64, from, to int64, limit int) ([]IPRow, Pair, error) {
	raw, err := s.singboxRows(ctx, table, ids, from, to)
	if err != nil {
		return nil, Pair{}, err
	}
	by := map[string]*IPRow{}
	var total Pair
	for k, p := range raw {
		total.RX += p.RX
		total.TX += p.TX
		r := by[k[0]]
		if r == nil {
			r = &IPRow{IP: k[0], Inbounds: []InboundShare{}}
			switch k[0] {
			case proto.DimUnattributed:
				r.Special = "unattributed"
			case proto.DimOther:
				r.Special = "other"
			}
			by[k[0]] = r
		}
		r.RX += p.RX
		r.TX += p.TX
		if k[1] != "" {
			r.Inbounds = append(r.Inbounds, InboundShare{Inbound: k[1], Relay: IsRelay(k[1]), Pair: p})
		}
	}
	var list, special []IPRow
	for _, r := range by {
		r.Total = r.RX + r.TX
		sort.Slice(r.Inbounds, func(i, j int) bool { return r.Inbounds[i].Total() > r.Inbounds[j].Total() })
		if len(r.Inbounds) > 0 {
			r.Relay = r.Inbounds[0].Relay
		}
		if r.Special != "" {
			special = append(special, *r)
		} else {
			list = append(list, *r)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Total > list[j].Total || list[i].Total == list[j].Total && list[i].IP < list[j].IP
	})
	if limit > 0 && len(list) > limit {
		other := IPRow{IP: proto.DimOther, Special: "other", Inbounds: []InboundShare{}}
		for _, r := range list[limit:] {
			other.RX += r.RX
			other.TX += r.TX
		}
		list = list[:limit]
		merged := false
		for i := range special {
			if special[i].Special == "other" {
				special[i].RX += other.RX
				special[i].TX += other.TX
				special[i].Total = special[i].RX + special[i].TX
				merged = true
			}
		}
		if !merged {
			other.Total = other.RX + other.TX
			special = append(special, other)
		}
	}
	sort.Slice(special, func(i, j int) bool { return special[i].Special > special[j].Special })
	return append(list, special...), total, nil
}

// InboundRow 入站汇总。
type InboundRow struct {
	Inbound string `json:"inbound"`
	Relay   bool   `json:"relay"`
	IPs     int    `json:"ips"`
	Pair
	Total int64 `json:"total"`
}

func (s *Store) Inbounds(ctx context.Context, table string, ids []int64, from, to int64) ([]InboundRow, error) {
	raw, err := s.singboxRows(ctx, table, ids, from, to)
	if err != nil {
		return nil, err
	}
	by := map[string]*InboundRow{}
	for k, p := range raw {
		name := k[1]
		if name == "" {
			name = "(未归因)"
		}
		r := by[name]
		if r == nil {
			r = &InboundRow{Inbound: name, Relay: IsRelay(name)}
			by[name] = r
		}
		r.RX += p.RX
		r.TX += p.TX
		if !strings.HasPrefix(k[0], "~") {
			r.IPs++
		}
	}
	out := make([]InboundRow, 0, len(by))
	for _, r := range by {
		r.Total = r.RX + r.TX
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	return out, nil
}
