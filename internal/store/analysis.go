package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

type AnalysisFilter struct {
	From, To                          int64
	HostIDs                           []int64
	Source, Site, IP, Search, Country string
	ASN                               uint
	Status                            int
	Group, Sort, Order                string
	Page, PageSize                    int
	Table                             string
}
type Metrics struct {
	Overflow      bool    `json:"overflow"`
	RX            int64   `json:"rx"`
	TX            int64   `json:"tx"`
	Total         int64   `json:"total"`
	Requests      int64   `json:"requests"`
	Errors        int64   `json:"errors"`
	IPs           int64   `json:"ips"`
	Sites         int64   `json:"sites"`
	Hosts         int64   `json:"hosts"`
	RXKnown       bool    `json:"rx_known"`
	RequestsKnown bool    `json:"requests_known"`
	ErrorRate     float64 `json:"error_rate"`
}
type AnalysisRow struct {
	Key         string `json:"key"`
	Country     string `json:"country"`
	CountryName string `json:"country_name"`
	Region      string `json:"region"`
	City        string `json:"city"`
	Scope       string `json:"scope"`
	ASN         uint   `json:"asn"`
	Org         string `json:"org"`
	Special     bool   `json:"special"`
	Metrics
}
type AnalysisResult struct {
	Rows     []AnalysisRow `json:"rows"`
	Totals   Metrics       `json:"totals"`
	Count    int64         `json:"count"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}
type AnalysisPoint struct {
	TS int64 `json:"ts"`
	Metrics
}

func networkBounds(raw string) (int, []byte, []byte, error) {
	p, e := netip.ParsePrefix(raw)
	if e != nil {
		a, ae := netip.ParseAddr(raw)
		if ae != nil {
			return 0, nil, nil, e
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	if p.Addr().Is4In6() {
		if p.Bits() < 96 {
			return 0, nil, nil, errors.New("映射 IPv4 前缀长度至少为 96")
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
	}
	p = p.Masked()
	a := p.Addr()
	family := 6
	bits := p.Bits()
	if a.Is4() {
		family = 4
		bits += 96
	}
	lo := a.As16()
	hi := lo
	for bit := bits; bit < 128; bit++ {
		hi[bit/8] |= 1 << uint(7-bit%8)
	}
	return family, lo[:], hi[:], nil
}
func ValidateAnalysis(f AnalysisFilter) error {
	if f.Source != "site" && f.Source != "singbox" {
		return errors.New("source 必须为 site 或 singbox")
	}
	if f.Table != "minute" && f.Table != "hour" && f.Table != "day" {
		return errors.New("无效统计粒度")
	}
	if f.From >= f.To {
		return errors.New("无效时间范围")
	}
	switch f.Group {
	case "ip", "site", "host", "status", "country", "asn", "inbound", "":
	default:
		return errors.New("无效分组")
	}
	if f.Source != "site" && (f.Status != 0 || f.Site != "") {
		return errors.New("状态码和网站筛选仅适用于源站日志")
	}
	if f.IP != "" {
		if _, _, _, e := networkBounds(f.IP); e != nil {
			return errors.New("IP/CIDR 无效")
		}
	}
	return nil
}

// Expressions are internal constants; every user value is a bound SQL parameter.
func analysisSQL(f AnalysisFilter) (from, where, group, fields string, args []any, err error) {
	if err = ValidateAnalysis(f); err != nil {
		return
	}
	fields = `COALESCE(metric_sum(f.rx),0),COALESCE(metric_sum(f.tx),0),0,0,COUNT(DISTINCT CASE WHEN i.family>0 THEN i.id END),0,COUNT(DISTINCT f.host_id),1`
	if f.Source == "site" {
		from = `site_` + f.Table + ` f JOIN hosts h ON h.id=f.host_id `
		if f.Table == "day" {
			from += `LEFT JOIN ips i ON 0 `
		} else {
			from += `JOIN ips i ON i.id=f.ip_id `
		}
		fields = `COALESCE(metric_sum(f.rx),0),COALESCE(metric_sum(f.tx),0),COALESCE(metric_sum(f.requests),0),COALESCE(metric_sum(CASE WHEN f.status>=400 THEN f.requests ELSE 0 END),0),COUNT(DISTINCT CASE WHEN i.family>0 THEN i.id END),COUNT(DISTINCT f.site),COUNT(DISTINCT f.host_id),COALESCE(MIN(f.rx_known),0)`
	} else {
		from = `fact_` + f.Table + ` f JOIN hosts h ON h.id=f.host_id LEFT JOIN ips i ON i.address=f.dim1 `
	}
	where = `f.ts>=? AND f.ts<?`
	args = []any{f.From, f.To}
	if f.Source == "singbox" {
		where += ` AND f.kind='singbox'`
	}
	if len(f.HostIDs) > 0 {
		in, a := inList(f.HostIDs)
		where += ` AND f.host_id IN ` + in
		args = append(args, a...)
	}
	if f.Site != "" {
		where += ` AND f.site=?`
		args = append(args, f.Site)
	}
	if f.Status > 0 {
		where += ` AND f.status=?`
		args = append(args, f.Status)
	}
	if f.IP != "" {
		family, lo, hi, _ := networkBounds(f.IP)
		where += ` AND i.family=? AND i.packed>=? AND i.packed<=?`
		args = append(args, family, lo, hi)
	}
	if f.Country != "" {
		where += ` AND i.country=?`
		args = append(args, strings.ToUpper(f.Country))
	}
	if f.ASN > 0 {
		where += ` AND i.asn=?`
		args = append(args, f.ASN)
	}
	switch f.Group {
	case "site":
		if f.Source != "site" {
			err = errors.New("site 分组只适用于网站")
			return
		}
		group = "f.site"
	case "host":
		group = "h.name"
	case "status":
		if f.Source != "site" {
			err = errors.New("status 分组只适用于网站")
			return
		}
		group = "CAST(f.status AS TEXT)"
	case "country":
		group = "i.country"
	case "asn":
		group = "CAST(i.asn AS TEXT)"
	case "inbound":
		if f.Source != "singbox" {
			err = errors.New("入站分组只适用于 sing-box")
			return
		}
		group = "f.dim2"
	default:
		group = "COALESCE(i.address,'~unknown')"
	}
	if f.Search != "" {
		where += ` AND INSTR(LOWER(COALESCE(` + group + `,'')),LOWER(?))>0`
		args = append(args, f.Search)
	}
	return
}
func metricScan(m *Metrics) []any {
	return []any{&m.RX, &m.TX, &m.Requests, &m.Errors, &m.IPs, &m.Sites, &m.Hosts, &m.RXKnown}
}
func finishMetrics(m *Metrics, source string) {
	m.Total = AddMetric(m.RX, m.TX)
	m.Overflow = m.Total >= MetricLimit || m.Requests >= MetricLimit || m.Errors >= MetricLimit
	m.RequestsKnown = source == "site"
	if m.Requests > 0 && !m.Overflow {
		m.ErrorRate = float64(m.Errors) * 100 / float64(m.Requests)
	}
}
func rankSQL(f AnalysisFilter, limited bool) (string, []any, error) {
	from, where, group, fields, args, e := analysisSQL(f)
	if e != nil {
		return "", nil, e
	}
	geo := `'', '',0,''`
	if f.Group == "ip" || f.Group == "" {
		geo = `COALESCE(MAX(i.country),''),COALESCE(MAX(i.country_name),''),COALESCE(MAX(i.asn),0),COALESCE(MAX(i.org),'')`
	}
	q := `SELECT COALESCE(` + group + `,''),` + geo + `,` + fields + ` FROM ` + from + ` WHERE ` + where + ` GROUP BY ` + group
	sortExpr := map[string]string{"total": "metric_sum(f.rx)+metric_sum(f.tx)", "rx": "metric_sum(f.rx)", "tx": "metric_sum(f.tx)", "key": group}
	if f.Source == "site" {
		sortExpr["requests"] = "metric_sum(f.requests)"
		sortExpr["error_rate"] = "1.0*metric_sum(CASE WHEN f.status>=400 THEN f.requests ELSE 0 END)/MAX(metric_sum(f.requests),1)"
	}
	order := sortExpr[f.Sort]
	if order == "" {
		order = sortExpr["total"]
	}
	dir := "DESC"
	if f.Order == "asc" {
		dir = "ASC"
	}
	q += ` ORDER BY ` + order + ` ` + dir + `,` + group + ` ASC`
	if limited {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	}
	return q, args, nil
}
func (s *Store) Analyze(ctx context.Context, f AnalysisFilter) (AnalysisResult, error) {
	out := AnalysisResult{Rows: []AnalysisRow{}, Page: f.Page, PageSize: f.PageSize}
	from, where, group, fields, args, e := analysisSQL(f)
	if e != nil {
		return out, e
	}
	tx, e := s.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if e = tx.QueryRowContext(ctx, `SELECT `+fields+` FROM `+from+` WHERE `+where, args...).Scan(metricScan(&out.Totals)...); e != nil {
		return out, e
	}
	finishMetrics(&out.Totals, f.Source)
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT `+group+` FROM `+from+` WHERE `+where+` GROUP BY `+group+`)`, args...).Scan(&out.Count); e != nil {
		return out, e
	}
	q, a, e := rankSQL(f, true)
	if e != nil {
		return out, e
	}
	rows, e := tx.QueryContext(ctx, q, a...)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		r, e := scanAnalysisRow(rows, f.Source)
		if e != nil {
			return out, e
		}
		out.Rows = append(out.Rows, r)
	}
	return out, rows.Err()
}
func scanAnalysisRow(rows *sql.Rows, source string) (AnalysisRow, error) {
	var r AnalysisRow
	a := []any{&r.Key, &r.Country, &r.CountryName, &r.ASN, &r.Org}
	a = append(a, metricScan(&r.Metrics)...)
	e := rows.Scan(a...)
	finishMetrics(&r.Metrics, source)
	r.Special = strings.HasPrefix(r.Key, "~")
	return r, e
}
func (s *Store) ExportAnalysis(ctx context.Context, f AnalysisFilter, emit func(AnalysisRow) error) error {
	q, args, e := rankSQL(f, false)
	if e != nil {
		return e
	}
	rows, e := s.R.QueryContext(ctx, q, args...)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		r, e := scanAnalysisRow(rows, f.Source)
		if e != nil {
			return e
		}
		if e = emit(r); e != nil {
			return e
		}
	}
	return rows.Err()
}
func (s *Store) AnalysisSeries(ctx context.Context, f AnalysisFilter, gran string, loc *time.Location) ([]AnalysisPoint, error) {
	from, where, _, fields, args, e := analysisSQL(f)
	if e != nil {
		return nil, e
	}
	bucket := "f.ts"
	if gran == "day" {
		var b strings.Builder
		b.WriteString("CASE ")
		for day := time.Unix(DayStart(f.From, loc), 0).In(loc); day.Unix() < f.To; day = day.AddDate(0, 0, 1) {
			fmt.Fprintf(&b, "WHEN f.ts < %d THEN %d ", day.AddDate(0, 0, 1).Unix(), day.Unix())
		}
		b.WriteString("ELSE f.ts END")
		bucket = b.String()
	}
	if gran == "hour" {
		bucket = "f.ts/3600*3600"
	}
	rows, e := s.R.QueryContext(ctx, `SELECT `+bucket+`,`+fields+` FROM `+from+` WHERE `+where+` GROUP BY `+bucket+` ORDER BY `+bucket, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []AnalysisPoint{}
	for rows.Next() {
		var p AnalysisPoint
		a := append([]any{&p.TS}, metricScan(&p.Metrics)...)
		if e = rows.Scan(a...); e != nil {
			return nil, e
		}
		finishMetrics(&p.Metrics, f.Source)

		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) Coverage(ctx context.Context, ids []int64, kind string) (int64, int64, error) {
	in, args := inList(ids)
	var first, last sql.NullInt64
	healthKind := kind
	if kind == "iface" {
		healthKind = "netdev"
	}
	e := s.R.QueryRowContext(ctx, `SELECT CASE WHEN COUNT(c.kind)=COUNT(h.id) THEN MAX(c.first_ts) END,MIN(c.last_ts)
 FROM hosts h LEFT JOIN collection_coverage c ON c.host_id=h.id AND c.kind=?
 WHERE h.id IN `+in+` AND (c.kind IS NOT NULL OR json_extract(h.meta_json,?)=1)`, append(append([]any{kind}, args...), "$.collectors."+healthKind+".enabled")...).Scan(&first, &last)
	return first.Int64, last.Int64, e
}
func (s *Store) DataIssues(ctx context.Context, f AnalysisFilter) ([]string, error) {
	in, args := inList(f.HostIDs)
	args = append(args, f.From, f.To)
	rows, e := s.R.QueryContext(ctx, `SELECT DISTINCT reason FROM data_issues WHERE host_id IN `+in+` AND to_ts>? AND from_ts<?`, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if e = rows.Scan(&v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// NetworkBoundStrings is used in diagnostics/tests without exposing raw database bytes.
func NetworkBoundStrings(cidr string) (string, string, error) {
	_, lo, hi, e := networkBounds(cidr)
	return hex.EncodeToString(lo), hex.EncodeToString(hi), e
}

func (s *Store) SourceCoverage(ctx context.Context, ids []int64, site string) (int64, int64, error) {
	in, args := inList(ids)
	var first, last sql.NullInt64
	e := s.R.QueryRowContext(ctx, `SELECT CASE WHEN COUNT(c.site)=COUNT(h.id) THEN MAX(c.first_ts) END,MIN(c.last_ts)
 FROM hosts h LEFT JOIN source_coverage c ON c.host_id=h.id AND c.site=?
 WHERE h.id IN `+in+` AND (c.site IS NOT NULL OR EXISTS(SELECT 1 FROM json_each(h.meta_json,'$.inventory.sources') src WHERE json_extract(src.value,'$.site')=? AND json_extract(src.value,'$.disabled')=0))`, append(append([]any{site}, args...), site)...).Scan(&first, &last)
	return first.Int64, last.Int64, e
}
