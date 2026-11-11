package server

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/domoxiaojun/Meterly/internal/cycle"
	"github.com/domoxiaojun/Meterly/internal/geo"
	"github.com/domoxiaojun/Meterly/internal/human"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
)

// ipRowGeo IP 排行行 + 归属地(server 侧本地查询,IP 不出本机)。
type ipRowGeo struct {
	store.IPRow
	Geo geo.Info `json:"geo"`
}

type badRequest struct{ msg string }

func (e badRequest) Error() string { return e.msg }

// factTable 区间对应的事实表:最近 1 小时用分钟表,否则小时表(超出小时表保留期用天表)。
func factTable(p period, now time.Time) string {
	if p.Label == "last1h" {
		return "fact_minute"
	}
	return store.TableFor(p.From, now)
}

// series 按粒度取序列并补齐空桶。gran: minute(≤6h)/ hour(≤62 天)/ day。
func (s *Server) series(r *http.Request, ids []int64, f store.Filter, p period, loc *time.Location, now time.Time, gran string) ([]store.Point, error) {
	ctx := r.Context()
	if gran == "" {
		gran = "hour"
		if p.Label == "last1h" {
			gran = "minute"
		}
	}
	switch gran {
	case "minute":
		if p.To-p.From > 6*3600 {
			return nil, badRequest{"分钟粒度最多 6 小时"}
		}
		pts, err := s.st.Series(ctx, "fact_minute", ids, f, p.From, p.To)
		if err != nil {
			return nil, errors.New("内部错误")
		}
		return fill(pts, p.From, min(p.To, now.Unix()/60*60+60), 60, nil), nil
	case "hour":
		if p.To-p.From > 62*86400 {
			return nil, badRequest{"小时粒度最多 62 天"}
		}
		pts, err := s.st.Series(ctx, "fact_hour", ids, f, p.From, p.To)
		if err != nil {
			return nil, errors.New("内部错误")
		}
		return fill(pts, p.From, min(p.To, now.Unix()/3600*3600+3600), 3600, nil), nil
	case "day":
		tbl := store.TableFor(p.From, now)
		pts, err := s.st.Series(ctx, tbl, ids, f, p.From, p.To)
		if err != nil {
			return nil, errors.New("内部错误")
		}
		if tbl == "fact_hour" {
			pts = foldDays(pts, loc)
		}
		return fill(pts, p.From, min(p.To, store.DayStart(now.Unix(), loc)+86400), 86400, loc), nil
	}
	return nil, badRequest{"gran 只能是 minute/hour/day"}
}

func foldDays(hours []store.Point, loc *time.Location) []store.Point {
	var out []store.Point
	for _, h := range hours {
		d := store.DayStart(h.TS, loc)
		if n := len(out); n > 0 && out[n-1].TS == d {
			out[n-1].RX += h.RX
			out[n-1].TX += h.TX
		} else {
			out = append(out, store.Point{TS: d, Pair: h.Pair})
		}
	}
	return out
}

func (s *Server) seriesErr(w http.ResponseWriter, err error) {
	var br badRequest
	if errors.As(err, &br) {
		writeErr(w, http.StatusBadRequest, br.msg)
		return
	}
	writeErr(w, http.StatusInternalServerError, "内部错误")
}

// jsonOnly 修改类接口只接受 application/json:跨站表单无法不经预检发送该类型,配合 SameSite=Strict 防 CSRF。
func (s *Server) jsonOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mt != "application/json" {
			writeErr(w, http.StatusUnsupportedMediaType, "需要 Content-Type: application/json")
			return
		}
		h(w, r)
	}
}

// ---- IP 明细 ----

func (s *Server) handleIPDetail(w http.ResponseWriter, r *http.Request) {
	ids, loc, rd, _, ok := s.scope(w, r)
	if !ok {
		return
	}
	ip := r.URL.Query().Get("ip")
	if a, err := netip.ParseAddr(ip); err != nil || a.Is4In6() || a.String() != ip {
		writeErr(w, http.StatusBadRequest, "ip 参数无效")
		return
	}
	now := s.Now()
	p, ok := resolvePeriod(r, now, loc, rd)
	if !ok {
		writeErr(w, http.StatusBadRequest, "周期参数无效")
		return
	}
	gran := "hour"
	if p.Label == "last1h" {
		gran = "minute"
	} else if p.To-p.From > 62*86400 {
		gran = "day"
	}
	f := store.Filter{Kind: proto.KindSingbox, Dim1: ip}
	pts, err := s.series(r, ids, f, p, loc, now, gran)
	if err != nil {
		s.seriesErr(w, err)
		return
	}
	tbl := s.topTable(p, loc, now)
	inb, err := s.st.IPInbounds(r.Context(), tbl, ids, ip, p.From, p.To)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	var tot store.Pair
	for _, x := range inb {
		tot.RX += x.RX
		tot.TX += x.TX
	}
	writeJSON(w, http.StatusOK, map[string]any{"period": p, "ip": ip, "geo": s.geo.Lookup(ip), "gran": gran,
		"points": pts, "inbounds": inb, "total": tot})
}

// ---- 国家 / ASN 汇总 ----

type geoRow struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	CC      string   `json:"cc,omitempty"`
	ASN     uint     `json:"asn,omitempty"`
	Special string   `json:"special,omitempty"` // unattributed / other / scope / unknown
	IPs     int      `json:"ips"`
	Top     []string `json:"top"` // 流量最大的几个 IP
	store.Pair
	Total int64 `json:"total"`
}

func (s *Server) handleGeoSummary(w http.ResponseWriter, r *http.Request) {
	ids, loc, rd, _, ok := s.scope(w, r)
	if !ok {
		return
	}
	by := r.URL.Query().Get("by")
	if by != "country" && by != "asn" {
		writeErr(w, http.StatusBadRequest, "by 只能是 country/asn")
		return
	}
	now := s.Now()
	p, ok := resolvePeriod(r, now, loc, rd)
	if !ok {
		writeErr(w, http.StatusBadRequest, "周期参数无效")
		return
	}
	rows, total, err := s.st.TopIPs(r.Context(), s.topTable(p, loc, now), ids, p.From, p.To, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	out := summarizeGeo(rows, by, s.geo.Lookup)
	writeJSON(w, http.StatusOK, map[string]any{"period": p, "by": by, "rows": out, "total": total, "geo": s.geo.Status()})
}

// summarizeGeo 按国家或 ASN 汇总;未归因 / 其他 / 内网等特殊桶单列,合计与 sing-box 总量一致。
func summarizeGeo(rows []store.IPRow, by string, lookup func(string) geo.Info) []geoRow {
	m := map[string]*geoRow{}
	get := func(key string, init geoRow) *geoRow {
		g := m[key]
		if g == nil {
			init.Key = key
			init.Top = []string{}
			g = &init
			m[key] = g
		}
		return g
	}
	for _, row := range rows {
		var g *geoRow
		switch row.Special {
		case "unattributed":
			g = get("~unattributed", geoRow{Name: "未归因(轮询间隙)", Special: "unattributed"})
		case "other":
			g = get("~other", geoRow{Name: "其他(Top-N 之外,无 IP 明细)", Special: "other"})
		default:
			info := lookup(row.IP)
			switch {
			case info.Scope != "":
				g = get("~scope:"+info.Scope, geoRow{Name: info.Scope, Special: "scope"})
			case by == "country" && info.CC != "":
				g = get(info.CC, geoRow{Name: info.Country, CC: info.CC})
			case by == "asn" && info.ASN != 0:
				g = get("AS"+strconv.FormatUint(uint64(info.ASN), 10), geoRow{Name: info.Org, ASN: info.ASN, CC: info.CC})
			default:
				g = get("~unknown", geoRow{Name: "未知(库中无记录或未加载归属地库)", Special: "unknown"})
			}
			g.IPs++
			if len(g.Top) < 3 {
				g.Top = append(g.Top, row.IP) // rows 已按流量降序
			}
		}
		g.RX += row.RX
		g.TX += row.TX
	}
	out := make([]geoRow, 0, len(m))
	for _, g := range m {
		g.Total = g.RX + g.TX
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		si, sj := out[i].Special == "unattributed" || out[i].Special == "other", out[j].Special == "unattributed" || out[j].Special == "other"
		if si != sj {
			return !si
		}
		return out[i].Total > out[j].Total || out[i].Total == out[j].Total && out[i].Key < out[j].Key
	})
	return out
}

// ---- 对账视图 ----

type reconRow struct {
	Host          string      `json:"host"`
	Iface         string      `json:"iface"`
	Period        period      `json:"period"`
	IfaceBytes    store.Pair  `json:"iface_bytes"`
	IfaceTotal    int64       `json:"iface_total"`
	Singbox       store.Pair  `json:"singbox"`        // 载荷字节
	SingboxFactor float64     `json:"singbox_factor"` // 估算网卡占用的倍数
	SingboxEst    int64       `json:"singbox_est"`    // 载荷 × 倍数
	Site          *store.Pair `json:"site"`           // M3 前为 null
	Netbird       *store.Pair `json:"netbird"`        // M3 前为 null(wt0 计数仅供参考,见 netbird_iface)
	NetbirdIface  *store.Pair `json:"netbird_iface,omitempty"`
	Unattributed  int64       `json:"unattributed"` // = 网卡总量 − 已归因估算(可能为负:口径重叠)
	UnattribPct   float64     `json:"unattributed_pct"`
}

// handleReconcile eth0(计费网卡)总量 = sing-box(估算)+ 站点 + NetBird + 未归因。
// sing-box 载荷在出口与入口各经过一次网卡,默认按 ×2 估算(factor=1 可切换为直接比较载荷)。
func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	_, _, _, hosts, ok := s.scope(w, r)
	if !ok {
		return
	}
	factor := 2.0
	if v := r.URL.Query().Get("factor"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0.5 || f > 4 {
			writeErr(w, http.StatusBadRequest, "factor 需在 0.5~4 之间")
			return
		}
		factor = f
	}
	now := s.Now()
	ctx := r.Context()
	out := []reconRow{}
	for _, h := range hosts {
		loc := s.loc(h.TZ)
		p, ok := resolvePeriod(r, now, loc, h.ResetDay)
		if !ok {
			writeErr(w, http.StatusBadRequest, "周期参数无效")
			return
		}
		tbl := factTable(p, now)
		ids := []int64{h.ID}
		ifs, err := s.st.KindDim1Totals(ctx, tbl, ids, proto.KindIface, p.From, p.To)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "内部错误")
			return
		}
		sb, err := s.st.KindDim1Totals(ctx, tbl, ids, proto.KindSingbox, p.From, p.To)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "内部错误")
			return
		}
		row := reconRow{Host: h.Name, Iface: h.BillingIface, Period: p, IfaceBytes: ifs[h.BillingIface], SingboxFactor: factor}
		row.IfaceTotal = row.IfaceBytes.Total()
		for _, v := range sb {
			row.Singbox.RX += v.RX
			row.Singbox.TX += v.TX
		}
		row.SingboxEst = int64(float64(row.Singbox.Total()) * factor)
		if wt, ok := ifs["wt0"]; ok && h.BillingIface != "wt0" {
			row.NetbirdIface = &wt
		}
		if site, err := s.st.KindTotals(ctx, tbl, ids, proto.KindSite, p.From, p.To); err == nil && (site.RX != 0 || site.TX != 0) {
			row.Site = &site
		}
		if nb, err := s.st.KindTotals(ctx, tbl, ids, proto.KindNetbird, p.From, p.To); err == nil && (nb.RX != 0 || nb.TX != 0) {
			row.Netbird = &nb
		}
		known := row.SingboxEst
		if row.Site != nil {
			known += row.Site.Total()
		}
		if row.Netbird != nil {
			known += row.Netbird.Total()
		}
		row.Unattributed = row.IfaceTotal - known
		row.UnattribPct = store.PctOf(row.Unattributed, row.IfaceTotal)
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": out, "factor": factor})
}

// ---- 服务商数字手工录入 ----

type providerRow struct {
	Host        string                 `json:"host"`
	BillingMode string                 `json:"billing_mode"`
	Iface       string                 `json:"iface"`
	CycleStart  string                 `json:"cycle_start"`
	CycleEnd    string                 `json:"cycle_end"`
	Ours        int64                  `json:"ours"`
	Reading     *store.ProviderReading `json:"reading"`
	Diff        int64                  `json:"diff"` // ours − 服务商
	Pct         float64                `json:"pct"`  // diff / 服务商 × 100
	History     []providerHist         `json:"history"`
}

type providerHist struct {
	store.ProviderReading
	CycleEnd string  `json:"cycle_end"`
	Ours     int64   `json:"ours"`
	Diff     int64   `json:"diff"`
	Pct      float64 `json:"pct"`
}

func (s *Server) billedIn(ctx context.Context, h *store.Host, from, to int64, now time.Time) (int64, error) {
	m, err := s.st.KindDim1Totals(ctx, store.TableFor(from, now), []int64{h.ID}, proto.KindIface, from, to)
	if err != nil {
		return 0, err
	}
	return billed(h.BillingMode, m[h.BillingIface]), nil
}

func (s *Server) handleProviderList(w http.ResponseWriter, r *http.Request) {
	_, _, _, hosts, ok := s.scope(w, r)
	if !ok {
		return
	}
	ctx, now := r.Context(), s.Now()
	out := []providerRow{}
	for _, h := range hosts {
		loc := s.loc(h.TZ)
		p, ok := resolvePeriod(r, now, loc, h.ResetDay)
		if !ok || p.Label == "last1h" {
			p, _ = resolvePeriodLabel("current", now, loc, h.ResetDay)
		}
		row := providerRow{Host: h.Name, BillingMode: h.BillingMode, Iface: h.BillingIface, CycleStart: p.Start, CycleEnd: p.End, History: []providerHist{}}
		var err error
		if row.Ours, err = s.billedIn(ctx, h, p.From, p.To, now); err != nil {
			writeErr(w, http.StatusInternalServerError, "内部错误")
			return
		}
		if rd, err := s.st.ProviderReadingFor(ctx, h.ID, p.Start); err == nil {
			row.Reading = rd
			row.Diff = row.Ours - rd.Bytes
			row.Pct = store.PctOf(row.Diff, rd.Bytes)
		}
		list, err := s.st.ProviderReadings(ctx, h.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "内部错误")
			return
		}
		for _, rd := range list {
			st, err := cycle.ParseDay(rd.CycleStart, loc)
			if err != nil {
				continue
			}
			_, e := cycle.Offset(st, h.ResetDay, 0, loc)
			ph := providerHist{ProviderReading: rd, CycleEnd: e.AddDate(0, 0, -1).Format("2006-01-02")}
			if ph.Ours, err = s.billedIn(ctx, h, st.Unix(), e.Unix(), now); err != nil {
				writeErr(w, http.StatusInternalServerError, "内部错误")
				return
			}
			ph.Diff = ph.Ours - rd.Bytes
			ph.Pct = store.PctOf(ph.Diff, rd.Bytes)
			row.History = append(row.History, ph)
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": out})
}

func resolvePeriodLabel(label string, now time.Time, loc *time.Location, rd int) (period, bool) {
	r, _ := http.NewRequest("GET", "/?period="+label, nil)
	return resolvePeriod(r, now, loc, rd)
}

func (s *Server) handleProviderSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Host       string `json:"host"`
		CycleStart string `json:"cycle_start"`
		Value      string `json:"value"` // 如 "1.23 TB"(照抄服务商后台单位)或字节数
		Note       string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 无效")
		return
	}
	h, err := s.st.HostByName(r.Context(), in.Host)
	if err != nil {
		writeErr(w, http.StatusNotFound, "主机不存在")
		return
	}
	b, err := human.ParseBytes(in.Value)
	if err != nil || strings.TrimSpace(in.Value) == "" {
		writeErr(w, http.StatusBadRequest, "用量格式无效,例:1.23 TB、850 GiB、123456789")
		return
	}
	if err := s.st.SetProviderReading(r.Context(), store.ProviderReading{HostID: h.ID, CycleStart: in.CycleStart, Bytes: b,
		Note: strings.TrimSpace(in.Note), UpdatedAt: s.Now().Unix()}); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Info("录入服务商用量", "host", h.Name, "cycle_start", in.CycleStart)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": b})
}

func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h, err := s.st.HostByName(r.Context(), q.Get("host"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "主机不存在")
		return
	}
	if err := s.st.DeleteProviderReading(r.Context(), h.ID, q.Get("cycle_start")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "记录不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- vnstat 一致性自检 ----

func (s *Server) handleVnstatCheck(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h, err := s.st.HostByName(r.Context(), q.Get("host"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "请选择具体主机")
		return
	}
	ifaces := []string{h.BillingIface}
	if v := q.Get("ifaces"); v != "" {
		ifaces = strings.Split(v, ",")
	}
	data, err := store.ParseVnstat(http.MaxBytesReader(w, r.Body, 32<<20), ifaces, 0)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "vnstat JSON 无效(请上传 vnstat --json 的输出)")
		return
	}
	res, err := s.st.CompareVnstat(r.Context(), h.ID, s.loc(h.TZ), data, s.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"host": h.Name, "tz": h.TZ, "results": res})
}

// ---- 归属地状态 / 健康检查 ----

func (s *Server) handleGeoStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.geo.Status())
}

// handleHealthz 进程存活且数据库可读才返回 200(容器 HEALTHCHECK 与 systemd 自检用)。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.st.Ping(ctx); err != nil {
		http.Error(w, "db error", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok\n"))
}
