package server

import (
	"encoding/csv"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/domoxiaojun/Meterly/internal/cycle"
	"github.com/domoxiaojun/Meterly/internal/store"
)

type analysisMeta struct {
	From          int64    `json:"from"`
	To            int64    `json:"to"`
	TZ            string   `json:"tz"`
	Table         string   `json:"resolution"`
	Gran          string   `json:"gran"`
	Complete      bool     `json:"complete"`
	State         string   `json:"state"`
	Reasons       []string `json:"reasons"`
	AvailableFrom int64    `json:"available_from"`
	LastSample    int64    `json:"last_sample"`
	DetailUntil   int64    `json:"detail_retention_seconds"`
	IPsAvailable  bool     `json:"ips_available"`
	Live          bool     `json:"live"`
}

func (s *Server) analysisScope(w http.ResponseWriter, r *http.Request) (store.AnalysisFilter, analysisMeta, bool) {
	f := store.AnalysisFilter{Source: "site", Group: "ip", Sort: "total", Order: "desc", Page: 1, PageSize: 50}
	m := analysisMeta{TZ: s.cfg.TZ, Complete: true, State: "available", Reasons: []string{}, IPsAvailable: true, DetailUntil: int64(s.st.Retention.HourDays) * 86400}
	ids, _, _, _, ok := s.scope(w, r)
	if !ok {
		return f, m, false
	}
	f.HostIDs = ids
	now := s.Now()
	q := r.URL.Query()
	if v := q.Get("source"); v != "" {
		f.Source = v
	}
	if v := q.Get("group"); v != "" {
		f.Group = v
	}
	f.Site = q.Get("site")
	f.IP = q.Get("ip")
	f.Search = q.Get("search")
	f.Country = q.Get("country")
	if v := q.Get("asn"); v != "" {
		n, e := strconv.ParseUint(strings.TrimPrefix(strings.ToUpper(v), "AS"), 10, 32)
		if e != nil {
			writeErr(w, 400, "ASN 无效")
			return f, m, false
		}
		f.ASN = uint(n)
	}
	if v := q.Get("status"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 100 || n > 599 {
			writeErr(w, 400, "状态码无效")
			return f, m, false
		}
		f.Status = n
	}
	if v := q.Get("sort"); v != "" {
		f.Sort = v
	}
	if v := q.Get("order"); v != "" {
		f.Order = v
	}
	if v := q.Get("page"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 1000000 {
			writeErr(w, 400, "页码无效")
			return f, m, false
		}
		f.Page = n
	}
	if v := q.Get("page_size"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 500 {
			writeErr(w, 400, "每页数量需为 1–500")
			return f, m, false
		}
		f.PageSize = n
	}
	from, to, e := s.analysisPeriod(r, now)
	if e != nil {
		writeErr(w, 400, e.Error())
		return f, m, false
	}
	f.From, f.To = from, to
	m.From, m.To = from, to
	m.Live = to >= now.Unix()-60
	minuteCut := now.Unix() - int64(s.st.Retention.MinuteDetailHours)*3600
	hourCut := now.Unix() - int64(s.st.Retention.HourDays)*86400
	f.Table = "hour"
	if from >= minuteCut {
		f.Table = "minute"
	}
	if from < hourCut {
		f.Table = "day"
		m.IPsAvailable = false
		m.Complete = false
		m.Reasons = append(m.Reasons, "完整 IP 明细已超过保留期，仅有长期日汇总")
	}
	m.Table = f.Table
	m.Gran = q.Get("gran")
	if m.Gran == "" || m.Gran == "auto" {
		m.Gran = "day"
		if to-from <= 7*86400 {
			m.Gran = "hour"
		}
		if to-from <= 6*3600 && f.Table == "minute" {
			m.Gran = "minute"
		}
	}
	if m.Gran != "minute" && m.Gran != "hour" && m.Gran != "day" {
		writeErr(w, 400, "gran 必须是 minute/hour/day/auto")
		return f, m, false
	}
	if m.Gran == "minute" && (f.Table != "minute" || to-from > 48*3600) {
		writeErr(w, 400, "分钟曲线只支持分钟明细保留期内的范围")
		return f, m, false
	}
	step := int64(3600)
	if f.Table == "minute" {
		step = 60
	}
	if f.Table == "day" {
		step = 86400
		m.Gran = "day"
	}
	if from%step != 0 || to%step != 0 {
		m.Complete = false
		m.Reasons = append(m.Reasons, "边界按现有时间桶对齐，末桶可能尚未完成")
		f.From = from / step * step
		f.To = (to + step - 1) / step * step
		m.From, m.To = f.From, f.To
	}
	first, last, e := s.st.Coverage(r.Context(), ids, f.Source)
	if f.Site != "" {
		first, last, e = s.st.SourceCoverage(r.Context(), ids, f.Site)
	}
	if e != nil {
		writeErr(w, 500, "读取采集覆盖范围失败")
		return f, m, false
	}
	m.AvailableFrom, m.LastSample = first, last
	if last > 0 && last < to-120 {
		m.Complete = false
		m.State = "partial"
		m.Reasons = append(m.Reasons, "采集尚未覆盖查询末尾，请检查离线、采集错误或缓冲积压")
	}
	if first == 0 {
		m.State = "not_collected"
		m.Complete = false
		m.Reasons = append(m.Reasons, "此来源尚未采集")
	} else if first > from {
		m.State = "partial"
		m.Complete = false
		m.Reasons = append(m.Reasons, "部分日期早于开始采集时间")
	}
	issues, e := s.st.DataIssues(r.Context(), f)
	if e != nil {
		writeErr(w, 500, "读取完整性信息失败")
		return f, m, false
	}
	if len(issues) > 0 {
		m.Complete = false
		m.State = "partial"
		m.Reasons = append(m.Reasons, issues...)
	}
	if f.Table == "day" && (f.Group == "ip" || f.Group == "country" || f.Group == "asn" || f.IP != "" || f.Country != "" || f.ASN > 0) {
		m.State = "expired"
	}
	if f.Source != "site" && f.Source != "singbox" {
		writeErr(w, 400, "source 必须为 site 或 singbox")
		return f, m, false
	}
	if !m.Complete && m.State == "available" {
		m.State = "partial"
	}
	if e := store.ValidateAnalysis(f); e != nil {
		writeErr(w, 400, e.Error())
		return f, m, false
	}
	return f, m, true
}
func (s *Server) analysisPeriod(r *http.Request, now time.Time) (int64, int64, error) {
	q := r.URL.Query()
	if q.Get("from") != "" || q.Get("to") != "" {
		from, e1 := strconv.ParseInt(q.Get("from"), 10, 64)
		to, e2 := strconv.ParseInt(q.Get("to"), 10, 64)
		if e1 != nil || e2 != nil || from >= to || to-from > 800*86400 || from < 0 {
			return 0, 0, errors.New("from/to 必须为不超过 800 天的 UTC 秒区间")
		}
		to = min(to, now.Unix())
		if from >= to {
			return 0, 0, errors.New("查询日期尚未开始")
		}
		return from, to, nil
	}
	local := now.In(s.cfg.loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, s.cfg.loc)
	var from, to time.Time
	to = now.Truncate(time.Minute)
	switch q.Get("period") {
	case "", "today":
		from = today
	case "yesterday":
		from = today.AddDate(0, 0, -1)
		to = today
	case "last7d":
		from = today.AddDate(0, 0, -6)
	case "last30d":
		from = today.AddDate(0, 0, -29)
	case "last1h":
		from = now.Truncate(time.Minute).Add(-time.Hour)
		to = now.Truncate(time.Minute)
	case "current":
		from, _ = cycle.Bounds(now, s.cfg.ResetDay, s.cfg.loc)
	case "previous":
		from, to = cycle.Offset(now, s.cfg.ResetDay, -1, s.cfg.loc)
	case "day", "custom":
		var e1, e2 error
		from, e1 = cycle.ParseDay(q.Get("start"), s.cfg.loc)
		end := q.Get("end")
		if end == "" {
			end = q.Get("start")
		}
		to, e2 = cycle.ParseDay(end, s.cfg.loc)
		to = to.AddDate(0, 0, 1)
		if e1 != nil || e2 != nil || !to.After(from) || to.Sub(from) > 800*24*time.Hour {
			return 0, 0, errors.New("日期范围无效")
		}
		if to.After(now) {
			to = now
		}
	default:
		return 0, 0, errors.New("未知日期范围")
	}
	if !to.After(from) {
		return 0, 0, errors.New("查询日期尚未开始")
	}
	return from.Unix(), to.Unix(), nil
}
func (s *Server) handleAnalysis(w http.ResponseWriter, r *http.Request) {
	f, m, ok := s.analysisScope(w, r)
	if !ok {
		return
	}
	if m.State == "expired" {
		writeJSON(w, 200, map[string]any{"rows": []store.AnalysisRow{}, "totals": nil, "count": 0, "page": f.Page, "page_size": f.PageSize, "meta": m})
		return
	}
	result, e := s.st.Analyze(r.Context(), f)
	if e != nil {
		s.log.Error("分析查询失败", "err", e)
		writeErr(w, 500, "读取分析数据失败，请重试")
		return
	}
	if result.Count == 0 && m.State == "available" {
		m.State = "zero"
	}
	writeJSON(w, 200, map[string]any{"rows": result.Rows, "totals": result.Totals, "count": result.Count, "page": result.Page, "page_size": result.PageSize, "meta": m})
}
func (s *Server) handleAnalysisSeries(w http.ResponseWriter, r *http.Request) {
	f, m, ok := s.analysisScope(w, r)
	if !ok {
		return
	}
	if m.State == "expired" {
		writeJSON(w, 200, map[string]any{"points": []store.AnalysisPoint{}, "meta": m})
		return
	}
	points, e := s.st.AnalysisSeries(r.Context(), f, m.Gran, s.cfg.loc)
	if e != nil {
		writeErr(w, 400, "趋势筛选无效")
		return
	}
	// Fill only the known collection span. Earlier/missing history remains explicitly unavailable.
	by := map[int64]store.AnalysisPoint{}
	for _, p := range points {
		by[p.TS] = p
	}
	step := int64(3600)
	if m.Gran == "minute" {
		step = 60
	}
	start := f.From / step * step
	if m.Gran == "day" {
		start = store.DayStart(f.From, s.cfg.loc)
	}
	filled := []store.AnalysisPoint{}
	for ts := start; ts < f.To; {
		if p, has := by[ts]; has {
			filled = append(filled, p)
		} else if m.Complete && ts >= m.AvailableFrom && ts <= m.LastSample && m.AvailableFrom > 0 {
			filled = append(filled, store.AnalysisPoint{TS: ts})
		}
		if m.Gran == "day" {
			ts = time.Unix(ts, 0).In(s.cfg.loc).AddDate(0, 0, 1).Unix()
		} else {
			ts += step
		}
	}
	writeJSON(w, 200, map[string]any{"points": filled, "meta": m})
}
func csvSafe(s string) string {
	if len(s) > 0 && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
func (s *Server) handleAnalysisExport(w http.ResponseWriter, r *http.Request) {
	f, m, ok := s.analysisScope(w, r)
	if !ok {
		return
	}
	if m.State == "expired" {
		writeErr(w, 410, "该日期的完整 IP 明细已过期")
		return
	}
	file, e := os.CreateTemp("", "meterly-export-*.csv")
	if e != nil {
		writeErr(w, 500, "无法准备导出")
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="meterly-traffic.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	cw := csv.NewWriter(file)
	_ = cw.Write([]string{"维度", "源站上行/上传字节", "源站下行/下载字节", "已知流量合计", "请求数", "错误请求", "国家", "ASN", "运营商", "开始UTC秒", "结束UTC秒", "数据完整"})
	e = s.st.ExportAnalysis(r.Context(), f, func(row store.AnalysisRow) error {
		rx := "不可用"
		if row.RXKnown {
			rx = strconv.FormatInt(row.RX, 10)
		}
		req := "不可用"
		if row.RequestsKnown {
			req = strconv.FormatInt(row.Requests, 10)
		}
		return cw.Write([]string{csvSafe(row.Key), rx, strconv.FormatInt(row.TX, 10), strconv.FormatInt(row.Total, 10), req, strconv.FormatInt(row.Errors, 10), row.Country, strconv.FormatUint(uint64(row.ASN), 10), csvSafe(row.Org), strconv.FormatInt(m.From, 10), strconv.FormatInt(m.To, 10), strconv.FormatBool(m.Complete)})
	})
	cw.Flush()
	if e == nil {
		e = cw.Error()
	}
	if e != nil {
		w.Header().Del("Content-Disposition")
		writeErr(w, 500, "导出失败，请重试")
		return
	}
	if _, e = file.Seek(0, 0); e != nil {
		writeErr(w, 500, "导出失败")
		return
	}
	http.ServeContent(w, r, "meterly-traffic.csv", time.Time{}, file)
}
func (s *Server) handleAnalysisOptions(w http.ResponseWriter, r *http.Request) {
	hosts, e := s.st.Hosts(r.Context())
	if e != nil {
		writeErr(w, 500, "读取主机失败")
		return
	}
	rows, e := s.st.R.QueryContext(r.Context(), `SELECT site,label,first_seen,last_seen FROM sites ORDER BY site`)
	if e != nil {
		writeErr(w, 500, "读取网站失败")
		return
	}
	defer rows.Close()
	sites := []map[string]any{}
	for rows.Next() {
		var id, label string
		var first, last int64
		if e := rows.Scan(&id, &label, &first, &last); e != nil {
			writeErr(w, 500, "读取网站失败")
			return
		}
		sites = append(sites, map[string]any{"site": id, "label": label, "first_seen": first, "last_seen": last})
	}
	writeJSON(w, 200, map[string]any{"hosts": hosts, "sites": sites, "tz": s.cfg.TZ, "sources": []string{"site", "singbox"}, "minute_hours": s.st.Retention.MinuteDetailHours, "detail_days": s.st.Retention.HourDays})
}
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	copyURL := *r.URL
	r = r.Clone(r.Context())
	r.URL = &copyURL
	q := r.URL.Query()
	q.Set("source", "site")
	q.Set("group", "site")
	for _, key := range []string{"ip", "site", "status", "search", "country", "asn"} {
		q.Del(key)
	}
	r.URL.RawQuery = q.Encode()
	f, m, ok := s.analysisScope(w, r)
	if !ok {
		return
	}
	f.Group = "site"
	f.PageSize = 5
	f.Site = ""
	f.IP = ""
	f.Status = 0
	f.Source = "site"
	sites, e := s.st.Analyze(r.Context(), f)
	if e != nil {
		writeErr(w, 500, "读取总览失败")
		return
	}
	prev := f
	span := m.To - m.From
	if r.URL.Query().Get("period") == "today" || r.URL.Query().Get("period") == "" {
		prev.From = time.Unix(m.From, 0).In(s.cfg.loc).AddDate(0, 0, -1).Unix()
		prev.To = prev.From + span
	} else {
		prev.To = m.From
		prev.From = m.From - span
	}
	previous, pe := s.st.Analyze(r.Context(), prev)
	growth := any(nil)
	if pe == nil && previous.Totals.Total > 0 && prev.From >= m.AvailableFrom && prev.From >= s.Now().Unix()-m.DetailUntil {
		growth = float64(sites.Totals.Total-previous.Totals.Total) * 100 / float64(previous.Totals.Total)
	}
	hosts, e := s.st.Hosts(r.Context())
	if e != nil {
		writeErr(w, 500, "读取主机失败")
		return
	}
	network := store.Pair{}
	online := 0
	alerts := []map[string]any{}
	for _, h := range hosts {
		selected := false
		for _, id := range f.HostIDs {
			if id == h.ID {
				selected = true
			}
		}
		if !selected {
			continue
		}
		if h.LastSeen > 0 && s.Now().Unix()-h.LastSeen < 300 {
			online++
		}
		table := "fact_" + f.Table
		ifs, e := s.st.KindDim1Totals(r.Context(), table, []int64{h.ID}, "iface", f.From, f.To)
		if e == nil {
			p := ifs[h.BillingIface]
			network.RX += p.RX
			network.TX += p.TX
		}
		alerts = append(alerts, hostAlerts(h, s.Now())...)
	}
	f.Group = "ip"
	ips := store.AnalysisResult{Rows: []store.AnalysisRow{}}
	if m.IPsAvailable {
		ips, _ = s.st.Analyze(r.Context(), f)
	}
	writeJSON(w, 200, map[string]any{"site": sites.Totals, "top_sites": sites.Rows, "top_ips": ips.Rows, "network": network, "online": online, "host_count": len(f.HostIDs), "growth_pct": growth, "previous": previous.Totals, "previous_from": prev.From, "previous_to": prev.To, "alerts": alerts, "meta": m})
}
