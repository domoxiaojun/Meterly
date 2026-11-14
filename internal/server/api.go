package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/domoxiaojun/Meterly/internal/cycle"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
)

// period 查询区间 [From, To)。
type period struct {
	Label    string `json:"label"` // current | previous | custom
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	Start    string `json:"start"` // 本地日期(含)
	End      string `json:"end"`   // 本地日期(含)
	TZ       string `json:"tz"`
	ResetDay int    `json:"reset_day"`
}

func resolvePeriod(r *http.Request, now time.Time, loc *time.Location, resetDay int) (period, bool) {
	q := r.URL.Query()
	p := period{Label: q.Get("period"), TZ: loc.String(), ResetDay: resetDay}
	var s, e time.Time
	switch p.Label {
	case "", "current":
		p.Label = "current"
		s, e = cycle.Offset(now, resetDay, 0, loc)
	case "previous":
		s, e = cycle.Offset(now, resetDay, -1, loc)
	case "today", "yesterday", "last7d", "last30d", "day":
		local := now.In(loc)
		s = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		e = now
		switch p.Label {
		case "yesterday":
			e = s
			s = s.AddDate(0, 0, -1)
		case "last7d":
			s = s.AddDate(0, 0, -6)
		case "last30d":
			s = s.AddDate(0, 0, -29)
		case "day":
			var err error
			s, err = cycle.ParseDay(q.Get("start"), loc)
			if err != nil {
				return p, false
			}
			e = s.AddDate(0, 0, 1)
		}
	case "last1h":
		e = now.Truncate(time.Minute)
		s = e.Add(-time.Hour)
	case "custom":
		var err1, err2 error
		s, err1 = cycle.ParseDay(q.Get("start"), loc)
		e, err2 = cycle.ParseDay(q.Get("end"), loc)
		if err1 != nil || err2 != nil || e.Before(s) || e.Sub(s) > 800*24*time.Hour {
			return p, false
		}
		e = e.AddDate(0, 0, 1)
	default:
		return p, false
	}
	p.From, p.To = s.Unix(), e.Unix()
	p.Start, p.End = s.In(loc).Format("2006-01-02"), e.In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	if p.Label == "last1h" {
		p.Start, p.End = s.In(loc).Format("2006-01-02 15:04"), e.In(loc).Format("2006-01-02 15:04")
	}
	return p, true
}

// scope 解析 host 参数:空/all = 全部主机(用面板时区与默认重置日);否则单机(用该机配置)。
func (s *Server) scope(w http.ResponseWriter, r *http.Request) (ids []int64, loc *time.Location, resetDay int, hosts []*store.Host, ok bool) {
	all, err := s.st.Hosts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	name := r.URL.Query().Get("host")
	loc, resetDay = s.cfg.loc, s.cfg.ResetDay
	for _, h := range all {
		if name == "" || name == "all" || h.Name == name {
			ids = append(ids, h.ID)
			hosts = append(hosts, h)
			if h.Name == name {
				loc, resetDay = s.loc(h.TZ), h.ResetDay
			}
		}
	}
	if name != "" && name != "all" && len(ids) == 0 {
		writeErr(w, http.StatusNotFound, "主机不存在")
		return
	}
	if len(ids) == 0 {
		ids = []int64{-1}
	}
	return ids, loc, resetDay, hosts, true
}

func billed(mode string, p store.Pair) int64 {
	switch mode {
	case "tx":
		return p.TX
	case "rx":
		return p.RX
	case "max":
		return max(p.RX, p.TX)
	}
	return store.AddMetric(p.RX, p.TX)
}

type hostOverview struct {
	Name         string                           `json:"name"`
	Online       bool                             `json:"online"`
	LastSeen     int64                            `json:"last_seen"`
	AgentVersion string                           `json:"agent_version"`
	Hostname     string                           `json:"hostname"`
	Collectors   map[string]proto.CollectorHealth `json:"collectors"`
	Buffered     int64                            `json:"buffered"`
	Dropped      int64                            `json:"dropped"`
	Revoked      bool                             `json:"revoked"`
	TZ           string                           `json:"tz"`
	ResetDay     int                              `json:"reset_day"`
	BillingMode  string                           `json:"billing_mode"`
	BillingIface string                           `json:"billing_iface"`
	Quota        int64                            `json:"quota"`
	Period       period                           `json:"period"`
	Ifaces       map[string]store.Pair            `json:"ifaces"`
	Billed       int64                            `json:"billed"`
	Today        int64                            `json:"today"`
	Forecast     int64                            `json:"forecast"` // 本周期预测(仅 current)
	QuotaPct     float64                          `json:"quota_pct"`
	Singbox      store.Pair                       `json:"singbox"`
	DaysLeft     float64                          `json:"days_left"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx, now := r.Context(), s.Now()
	var err error
	_, _, _, hosts, scopeOK := s.scope(w, r)
	if !scopeOK {
		return
	}
	gp, ok := resolvePeriod(r, now, s.cfg.loc, s.cfg.ResetDay)
	if !ok {
		writeErr(w, http.StatusBadRequest, "周期参数无效")
		return
	}
	out := []hostOverview{}
	var totBilled int64
	var totIface, totSB store.Pair
	for _, h := range hosts {
		loc := s.loc(h.TZ)
		p, _ := resolvePeriod(r, now, loc, h.ResetDay)
		ho := hostOverview{Name: h.Name, LastSeen: h.LastSeen, AgentVersion: h.AgentVersion, Revoked: h.RevokedAt > 0,
			TZ: h.TZ, ResetDay: h.ResetDay, BillingMode: h.BillingMode, BillingIface: h.BillingIface, Quota: h.QuotaBytes, Period: p,
			Online: s.hostOnline(h, now)}
		var meta proto.Meta
		if json.Unmarshal([]byte(h.MetaJSON), &meta) == nil {
			ho.Collectors, ho.Buffered, ho.Dropped, ho.Hostname = meta.Collectors, meta.Buffered, meta.Dropped, meta.Hostname
		}
		tbl := factTableFor(s.st, p, now)
		ids := []int64{h.ID}
		if ho.Ifaces, err = s.st.KindDim1Totals(ctx, tbl, ids, proto.KindIface, p.From, p.To); err != nil {
			writeErr(w, http.StatusInternalServerError, "内部错误")
			return
		}
		bi := ho.Ifaces[h.BillingIface]
		ho.Billed = billed(h.BillingMode, bi)
		totBilled = store.AddMetric(totBilled, ho.Billed)
		totIface.RX = store.AddMetric(totIface.RX, bi.RX)
		totIface.TX = store.AddMetric(totIface.TX, bi.TX)
		sb, _ := s.st.KindDim1Totals(ctx, tbl, ids, proto.KindSingbox, p.From, p.To)
		for _, v := range sb {
			ho.Singbox.RX = store.AddMetric(ho.Singbox.RX, v.RX)
			ho.Singbox.TX = store.AddMetric(ho.Singbox.TX, v.TX)
		}
		totSB.RX = store.AddMetric(totSB.RX, ho.Singbox.RX)
		totSB.TX = store.AddMetric(totSB.TX, ho.Singbox.TX)
		day := store.DayStart(now.Unix(), loc)
		if td, err := s.st.KindDim1Totals(ctx, "fact_hour", ids, proto.KindIface, day, day+86400); err == nil {
			ho.Today = billed(h.BillingMode, td[h.BillingIface])
		}
		if p.Label == "current" {
			ho.DaysLeft = float64(p.To-now.Unix()) / 86400
			ho.Forecast = s.forecast(r, h, loc, p, now, ho.Billed)
		}
		if h.QuotaBytes > 0 {
			ref := ho.Billed
			ho.QuotaPct = float64(ref) * 100 / float64(h.QuotaBytes)
		}
		out = append(out, ho)
	}
	writeJSON(w, http.StatusOK, map[string]any{"now": now.Unix(), "tz": s.cfg.TZ, "period": gp, "hosts": out,
		"totals": map[string]any{"billed": totBilled, "iface": totIface, "singbox": totSB}})
}

// forecast 当期预测 = 已用 + 近 7 个完整日日均 × 剩余天数(不足 7 天按已有完整日;无完整日按已用/已过时长)。
func (s *Server) forecast(r *http.Request, h *store.Host, loc *time.Location, p period, now time.Time, used int64) int64 {
	today := store.DayStart(now.Unix(), loc)
	from := max(time.Unix(today, 0).In(loc).AddDate(0, 0, -7).Unix(), p.From)
	remain := float64(p.To-now.Unix()) / 86400
	if remain < 0 {
		return used
	}
	if from < today {
		days, err := s.st.DaySeries(r.Context(), []int64{h.ID}, proto.KindIface, h.BillingIface, from, today, loc, now)
		if err == nil && len(days) > 0 {
			var sum int64
			for _, d := range days {
				sum = store.AddMetric(sum, billed(h.BillingMode, d.Pair))
			}
			n := float64(today-from) / 86400
			return store.AddMetric(used, store.EstimateMetric(float64(sum)/n*remain))
		}
	}
	el := float64(now.Unix()-p.From) / 86400
	if el <= 0 {
		return used
	}
	return store.AddMetric(used, store.EstimateMetric(float64(used)/el*remain))
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	ids, loc, rd, _, ok := s.scope(w, r)
	if !ok {
		return
	}
	now := s.Now()
	p, ok := resolvePeriod(r, now, loc, rd)
	if !ok {
		writeErr(w, http.StatusBadRequest, "周期参数无效")
		return
	}
	q := r.URL.Query()
	f := store.Filter{Kind: q.Get("kind"), Dim1: q.Get("dim1"), Dim2: q.Get("dim2")}
	if f.Kind == "" {
		f.Kind = proto.KindIface
	}
	if p.Label != "current" && p.Label != "previous" {
		from, to, e := s.analysisPeriod(r, now)
		if e != nil {
			writeErr(w, 400, e.Error())
			return
		}
		p.From, p.To = from, to
		loc = s.cfg.loc
	}
	if f.Kind == proto.KindIface && f.Dim1 == "" {
		_, _, _, hosts, _ := s.scope(w, r)
		combined := map[int64]store.Pair{}
		for _, h := range hosts {
			if h.BillingIface == "" {
				continue
			}
			hf := f
			hf.Dim1 = h.BillingIface
			points, e := s.series(r, []int64{h.ID}, hf, p, loc, now, q.Get("gran"))
			if e != nil {
				writeErr(w, 400, e.Error())
				return
			}
			for _, pt := range points {
				v := combined[pt.TS]
				v.RX = store.AddMetric(v.RX, pt.RX)
				v.TX = store.AddMetric(v.TX, pt.TX)
				combined[pt.TS] = v
			}
		}
		points := []store.Point{}
		for ts, pair := range combined {
			points = append(points, store.Point{TS: ts, Pair: pair})
		}
		sort.Slice(points, func(i, j int) bool { return points[i].TS < points[j].TS })
		writeJSON(w, 200, map[string]any{"period": p, "points": points, "kind": f.Kind, "dim1": "billing"})
		return
	}
	pts, err := s.series(r, ids, f, p, loc, now, q.Get("gran"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	kind, dim1 := f.Kind, f.Dim1
	writeJSON(w, http.StatusOK, map[string]any{"period": p, "points": pts, "kind": kind, "dim1": dim1, "dim2": f.Dim2})
}

// fill 补齐空桶(按天时考虑时区)。
func fill(pts []store.Point, from, to, step int64, loc *time.Location) []store.Point {
	m := map[int64]store.Point{}
	for _, p := range pts {
		m[p.TS] = p
	}
	out := []store.Point{}
	for t := from; t < to; {
		if p, ok := m[t]; ok {
			out = append(out, p)
		} else {
			out = append(out, store.Point{TS: t})
		}
		if loc != nil {
			t = time.Unix(t, 0).In(loc).AddDate(0, 0, 1).Unix()
		} else {
			t += step
		}
	}
	return out
}

// topTable IP 排行:区间按主机时区整日对齐时用天表(Top-200/天),否则用小时表。
func (s *Server) topTable(p period, loc *time.Location, now time.Time) string {
	if p.Label == "last1h" {
		return "fact_minute"
	}
	if s.st.TableFor(p.From, now) == "fact_day" {
		return "fact_day"
	}

	return "fact_hour"
}

func (s *Server) handleTopIPs(w http.ResponseWriter, r *http.Request) {
	ids, loc, rd, _, ok := s.scope(w, r)
	if !ok {
		return
	}
	now := s.Now()
	p, ok := resolvePeriod(r, now, loc, rd)
	if !ok {
		writeErr(w, http.StatusBadRequest, "周期参数无效")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, total, err := s.st.TopIPs(r.Context(), s.topTable(p, loc, now), ids, p.From, p.To, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	out := make([]ipRowGeo, len(rows))
	for i, row := range rows {
		out[i] = ipRowGeo{IPRow: row}
		if row.Special == "" {
			out[i].Geo = s.geo.Lookup(row.IP)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"period": p, "rows": out, "total": total})
}

func (s *Server) handleInbounds(w http.ResponseWriter, r *http.Request) {
	ids, loc, rd, _, ok := s.scope(w, r)
	if !ok {
		return
	}
	now := s.Now()
	p, ok := resolvePeriod(r, now, loc, rd)
	if !ok {
		writeErr(w, http.StatusBadRequest, "周期参数无效")
		return
	}
	tbl := s.st.TableFor(p.From, now)
	if p.Label == "last1h" {
		tbl = "fact_minute"
	}
	rows, err := s.st.Inbounds(r.Context(), tbl, ids, p.From, p.To)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Total > rows[j].Total })
	writeJSON(w, http.StatusOK, map[string]any{"period": p, "rows": rows})
}
