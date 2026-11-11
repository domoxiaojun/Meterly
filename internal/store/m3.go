package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

// SiteRow 一个站点在周期内的汇总。
type SiteRow struct {
	Site     string `json:"site"`
	RX       int64  `json:"rx"` // 请求字节(若日志有 $request_length,否则 0)
	TX       int64  `json:"tx"` // $body_bytes_sent
	Requests int64  `json:"requests"`
	S2xx     int64  `json:"s2xx"`
	S3xx     int64  `json:"s3xx"`
	S4xx     int64  `json:"s4xx"`
	S5xx     int64  `json:"s5xx"`
	SOther   int64  `json:"sother"`
	IPs      int    `json:"ips"`
}

// KindTotals 某 kind 在周期内的合计(所有 dim1/dim2)。
func (s *Store) KindTotals(ctx context.Context, table string, ids []int64, kind string, from, to int64) (Pair, error) {
	if !validTable(table) {
		return Pair{}, fmt.Errorf("非法表名")
	}
	in, args := inList(ids)
	row := s.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(rx),0), COALESCE(SUM(tx),0) FROM `+table+`
		WHERE kind=? AND host_id IN `+in+` AND ts>=? AND ts<?`, append(append([]any{kind}, args...), from, to)...)
	var p Pair
	err := row.Scan(&p.RX, &p.TX)
	return p, err
}

// SiteTotals 按站点汇总字节、请求数与状态类。
func (s *Store) SiteTotals(ctx context.Context, table string, ids []int64, from, to int64) ([]SiteRow, error) {
	if !validTable(table) {
		return nil, fmt.Errorf("非法表名")
	}
	in, args := inList(ids)
	qargs := append(append([]any{proto.KindSite}, args...), from, to)
	rows, err := s.R.QueryContext(ctx, `SELECT dim1, COALESCE(SUM(rx),0), COALESCE(SUM(tx),0),
		COUNT(DISTINCT CASE WHEN dim2<>'' AND dim2 NOT LIKE '~%' THEN dim2 END)
		FROM `+table+` WHERE kind=? AND host_id IN `+in+` AND ts>=? AND ts<?
		GROUP BY dim1`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[string]*SiteRow{}
	var order []string
	for rows.Next() {
		var r SiteRow
		if err := rows.Scan(&r.Site, &r.RX, &r.TX, &r.IPs); err != nil {
			return nil, err
		}
		by[r.Site] = &r
		order = append(order, r.Site)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 请求数 / 状态类
	qargs = append(append([]any{proto.KindSiteReq}, args...), from, to)
	r2, err := s.R.QueryContext(ctx, `SELECT dim1, dim2, COALESCE(SUM(rx),0) FROM `+table+`
		WHERE kind=? AND host_id IN `+in+` AND ts>=? AND ts<? GROUP BY dim1, dim2`, qargs...)
	if err != nil {
		return nil, err
	}
	defer r2.Close()
	for r2.Next() {
		var site, cls string
		var n int64
		if err := r2.Scan(&site, &cls, &n); err != nil {
			return nil, err
		}
		r := by[site]
		if r == nil {
			r = &SiteRow{Site: site}
			by[site] = r
			order = append(order, site)
		}
		r.Requests += n
		switch cls {
		case "2xx":
			r.S2xx += n
		case "3xx":
			r.S3xx += n
		case "4xx":
			r.S4xx += n
		case "5xx":
			r.S5xx += n
		default:
			r.SOther += n
		}
	}
	out := make([]SiteRow, 0, len(order))
	for _, k := range order {
		out = append(out, *by[k])
	}
	// 按 tx+rx 降序
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			ti, tj := out[i].RX+out[i].TX, out[j].RX+out[j].TX
			if tj > ti || (tj == ti && out[j].Site < out[i].Site) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, r2.Err()
}

// SiteIPs 某站点的客户端 IP 排行。
func (s *Store) SiteIPs(ctx context.Context, table string, ids []int64, site string, from, to int64, limit int) ([]IPRow, Pair, error) {
	var total Pair
	if !validTable(table) {
		return nil, total, fmt.Errorf("非法表名")
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	in, args := inList(ids)
	qargs := append(append([]any{proto.KindSite, site}, args...), from, to)
	rows, err := s.R.QueryContext(ctx, `SELECT dim2, COALESCE(SUM(rx),0), COALESCE(SUM(tx),0) FROM `+table+`
		WHERE kind=? AND dim1=? AND host_id IN `+in+` AND ts>=? AND ts<?
		GROUP BY dim2`, qargs...)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	var all []IPRow
	for rows.Next() {
		var r IPRow
		if err := rows.Scan(&r.IP, &r.RX, &r.TX); err != nil {
			return nil, total, err
		}
		r.Total = r.RX + r.TX
		if r.IP == proto.DimOther || strings.HasPrefix(r.IP, "~") {
			r.Special = strings.TrimPrefix(r.IP, "~")
		}
		total.RX += r.RX
		total.TX += r.TX
		all = append(all, r)
	}
	// 排序:特殊桶垫后,其余按流量
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			si, sj := all[i].Special != "", all[j].Special != ""
			swap := false
			if si != sj {
				swap = si && !sj
			} else if all[j].Total > all[i].Total || (all[j].Total == all[i].Total && all[j].IP < all[i].IP) {
				swap = true
			}
			if swap {
				all[i], all[j] = all[j], all[i]
			}
		}
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, total, rows.Err()
}

// Dim1Rows 通用 dim1 排行(NetBird 对等端、xray 入站/用户)。
type Dim1Row struct {
	Key   string `json:"key"`
	Dim2  string `json:"dim2,omitempty"`
	RX    int64  `json:"rx"`
	TX    int64  `json:"tx"`
	Total int64  `json:"total"`
}

func (s *Store) Dim1Rank(ctx context.Context, table string, ids []int64, kind string, from, to int64) ([]Dim1Row, Pair, error) {
	var total Pair
	if !validTable(table) {
		return nil, total, fmt.Errorf("非法表名")
	}
	in, args := inList(ids)
	rows, err := s.R.QueryContext(ctx, `SELECT dim1, MAX(dim2), COALESCE(SUM(rx),0), COALESCE(SUM(tx),0) FROM `+table+`
		WHERE kind=? AND host_id IN `+in+` AND ts>=? AND ts<?
		GROUP BY dim1 ORDER BY SUM(rx)+SUM(tx) DESC, dim1`, append(append([]any{kind}, args...), from, to)...)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	var out []Dim1Row
	for rows.Next() {
		var r Dim1Row
		if err := rows.Scan(&r.Key, &r.Dim2, &r.RX, &r.TX); err != nil {
			return nil, total, err
		}
		r.Total = r.RX + r.TX
		total.RX += r.RX
		total.TX += r.TX
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func validTable(t string) bool {
	switch t {
	case "fact_minute", "fact_hour", "fact_day":
		return true
	}
	return false
}
