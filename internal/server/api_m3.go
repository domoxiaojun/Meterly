package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
)

func (s *Server) handleEnrollRedeem(w http.ResponseWriter, r *http.Request) {
	ip, now := s.clientIP(r), s.Now()
	if !s.enrollRL.allow(ip, now) {
		writeErr(w, http.StatusTooManyRequests, "请求过于频繁")
		return
	}
	var in struct {
		Code     string `json:"code"`
		Hostname string `json:"hostname"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求无效")
		return
	}
	tok, host, err := s.st.RedeemEnroll(r.Context(), strings.TrimSpace(in.Code), strings.TrimSpace(in.Hostname), now)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrEnrollInvalid):
			s.log.Warn("注册码核销失败", "ip", ip)
			writeErr(w, http.StatusUnauthorized, "注册码无效或已过期")
		case errors.Is(err, store.ErrEnrollExists):
			writeErr(w, http.StatusConflict, "主机已注册")
		default:
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	s.log.Info("主机已通过注册码加入", "host", host.Name, "ip", ip)
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "host": host.Name})
}

func (s *Server) handleEnrollCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		HostName string `json:"host_name"`
		TTLHours int    `json:"ttl_hours"`
		MaxUses  int    `json:"max_uses"`
		Reissue  bool   `json:"reissue"`
		Note     string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求无效")
		return
	}
	ttl := time.Duration(in.TTLHours) * time.Hour
	if in.TTLHours == 0 {
		ttl = 24 * time.Hour
	}
	code, rec, err := s.st.CreateEnroll(r.Context(), strings.TrimSpace(in.HostName), store.EnrollOpts{
		TTL: ttl, MaxUses: in.MaxUses, Reissue: in.Reissue, Note: in.Note,
	}, s.Now())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "enroll": rec})
}

func (s *Server) handleEnrollList(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListEnroll(r.Context(), s.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	if list == nil {
		list = []*store.EnrollCode{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"codes": list})
}

func (s *Server) handleEnrollDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if id == 0 {
		writeErr(w, http.StatusBadRequest, "缺少 id")
		return
	}
	if err := s.st.DeleteEnroll(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "未找到")
			return
		}
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSites(w http.ResponseWriter, r *http.Request) {
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
	rows, err := s.st.SiteTotals(r.Context(), factTableFor(s.st, p, now), ids, p.From, p.To)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	if rows == nil {
		rows = []store.SiteRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"period": p, "sites": rows, "note": "站点流量按请求结束时刻归因;长连接(如 Emby 播放)的字节全部记在结束那一分钟/小时,曲线会后移,总量仍准确。日志通常只有下行 $body_bytes_sent,没有 $request_length 时入站为 0。"})
}

func (s *Server) handleSiteIPs(w http.ResponseWriter, r *http.Request) {
	ids, loc, rd, _, ok := s.scope(w, r)
	if !ok {
		return
	}
	site := r.URL.Query().Get("site")
	if site == "" || len(site) > 128 {
		writeErr(w, http.StatusBadRequest, "缺少 site")
		return
	}
	now := s.Now()
	p, ok := resolvePeriod(r, now, loc, rd)
	if !ok {
		writeErr(w, http.StatusBadRequest, "周期参数无效")
		return
	}
	rows, tot, err := s.st.SiteIPs(r.Context(), factTableFor(s.st, p, now), ids, site, p.From, p.To, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	if rows == nil {
		rows = []store.IPRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"period": p, "site": site, "rows": rows, "total": tot})
}

func (s *Server) handleNetbird(w http.ResponseWriter, r *http.Request) {
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
	rows, tot, err := s.st.Dim1Rank(r.Context(), factTableFor(s.st, p, now), ids, proto.KindNetbird, p.From, p.To)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	if rows == nil {
		rows = []store.Dim1Row{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"period": p, "peers": rows, "total": tot})
}

func (s *Server) handleXray(w http.ResponseWriter, r *http.Request) {
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
	inb, itot, err := s.st.Dim1Rank(r.Context(), factTableFor(s.st, p, now), ids, proto.KindXray, p.From, p.To)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	users, utot, err := s.st.Dim1Rank(r.Context(), factTableFor(s.st, p, now), ids, proto.KindXrayUser, p.From, p.To)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	if inb == nil {
		inb = []store.Dim1Row{}
	}
	if users == nil {
		users = []store.Dim1Row{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"period": p, "inbounds": inb, "inbound_total": itot, "users": users, "user_total": utot,
		"note": "xray stats API 只能按入站或用户累计,没有来源 IP。",
	})
}

// knownCollectors 面板固定展示的采集器(未上报则显示「未启用」,绝不显示 0)。
var knownCollectors = []struct{ ID, Label, Note string }{
	{"netdev", "网卡", ""},
	{"singbox", "sing-box", ""},
	{"site", "站点日志", "按请求结束时刻归因"},
	{"netbird", "NetBird", ""},
	{"xray", "xray", "无来源 IP"},
}

func (s *Server) handleCollectors(w http.ResponseWriter, r *http.Request) {
	ctx, now := r.Context(), s.Now()
	hosts, err := s.st.Hosts(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	type col struct {
		ID      string `json:"id"`
		Label   string `json:"label"`
		Status  string `json:"status"` // enabled / disabled / error
		LastOK  int64  `json:"last_ok,omitempty"`
		Fails   int    `json:"fails,omitempty"`
		LastErr string `json:"last_err,omitempty"`
		Note    string `json:"note,omitempty"`
	}
	type hostRow struct {
		Name         string `json:"name"`
		Hostname     string `json:"hostname"`
		Online       bool   `json:"online"`
		LastSeen     int64  `json:"last_seen"`
		AgentVersion string `json:"agent_version"`
		Buffered     int64  `json:"buffered"`
		Dropped      int64  `json:"dropped"`
		Revoked      bool   `json:"revoked"`
		Collectors   []col  `json:"collectors"`
	}
	out := []hostRow{}
	for _, h := range hosts {
		row := hostRow{Name: h.Name, LastSeen: h.LastSeen, AgentVersion: h.AgentVersion, Revoked: h.RevokedAt > 0,
			Online: h.LastSeen > 0 && now.Unix()-h.LastSeen < int64(s.cfg.OfflineAfter)*60}
		var meta proto.Meta
		if json.Unmarshal([]byte(h.MetaJSON), &meta) == nil {
			row.Hostname, row.Buffered, row.Dropped = meta.Hostname, meta.Buffered, meta.Dropped
		}
		for _, k := range knownCollectors {
			c := col{ID: k.ID, Label: k.Label, Status: "disabled", Note: k.Note}
			if meta.Collectors != nil {
				if hth, ok := meta.Collectors[k.ID]; ok && hth.Enabled {
					c.LastOK, c.Fails, c.LastErr = hth.LastOK, hth.Fails, hth.LastErr
					if hth.Note != "" {
						c.Note = hth.Note
					}
					if hth.Fails > 0 {
						c.Status = "error"
					} else {
						c.Status = "enabled"
					}
				}
			}
			row.Collectors = append(row.Collectors, c)
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"now": now.Unix(), "hosts": out})
}
