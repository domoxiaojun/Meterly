package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
)

func hostAlerts(h *store.Host, now time.Time) []map[string]any {
	out := []map[string]any{}
	add := func(kind, msg string) {
		out = append(out, map[string]any{"host": h.Name, "kind": kind, "message": msg})
	}
	if h.LastSeen == 0 {
		add("pending", "尚未上报")
	} else if now.Unix()-h.LastSeen > 300 {
		add("offline", "主机离线超过 5 分钟")
	}
	var m proto.Meta
	_ = json.Unmarshal([]byte(h.MetaJSON), &m)
	if m.Dropped > 0 {
		add("data_loss", "有数据丢弃，请查看完整性信息")
	}
	if m.Buffered > 0 {
		add("backlog", "存在待补发数据")
	}
	if m.ConfigError != "" {
		add("config", m.ConfigError)
	}
	for name, c := range m.Collectors {
		if c.Enabled && c.Fails >= 3 {
			add("collector", name+" 连续采集失败："+c.LastErr)
		}
	}
	return out
}
func (s *Server) handleHostList(w http.ResponseWriter, r *http.Request) {
	hs, e := s.st.Hosts(r.Context())
	if e != nil {
		writeErr(w, 500, "读取主机失败")
		return
	}
	out := []map[string]any{}
	for _, h := range hs {
		var m proto.Meta
		_ = json.Unmarshal([]byte(h.MetaJSON), &m)
		c, e := s.st.HostConfig(r.Context(), h.ID)
		if e != nil {
			writeErr(w, 500, "读取配置失败")
			return
		}
		out = append(out, map[string]any{"host": h, "meta": m, "desired": c, "alerts": hostAlerts(h, s.Now())})
	}
	writeJSON(w, 200, map[string]any{"hosts": out})
}
func (s *Server) handleHostSettings(w http.ResponseWriter, r *http.Request) {
	h, e := s.st.HostByName(r.Context(), r.PathValue("host"))
	if e != nil {
		writeErr(w, 404, "主机不存在")
		return
	}
	var p struct {
		Version      int64  `json:"version"`
		TZ           string `json:"tz"`
		ResetDay     int    `json:"reset_day"`
		BillingMode  string `json:"billing_mode"`
		BillingIface string `json:"billing_iface"`
		Quota        int64  `json:"quota"`
		Note         string `json:"note"`
	}
	if e = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&p); e != nil {
		writeErr(w, 400, "配置无效")
		return
	}
	o := store.HostOpts{TZ: p.TZ, ResetDay: p.ResetDay, BillingMode: p.BillingMode, BillingIface: p.BillingIface, QuotaBytes: p.Quota, Note: p.Note}
	if e = o.Validate(); e != nil {
		writeErr(w, 400, e.Error())
		return
	}
	if o.BillingIface == "" {
		o.BillingIface = h.BillingIface
	}
	if len(o.BillingIface) > 64 || len(o.Note) > 1000 {
		writeErr(w, 400, "字段过长")
		return
	}
	var meta proto.Meta
	_ = json.Unmarshal([]byte(h.MetaJSON), &meta)
	if meta.Inventory != nil && o.BillingIface != "" {
		found := false
		for _, iface := range meta.Inventory.Interfaces {
			if iface == o.BillingIface {
				found = true
			}
		}
		if !found {
			writeErr(w, 400, "请选择实际发现的网卡")
			return
		}
	}
	rr, err := s.st.W.ExecContext(r.Context(), "UPDATE hosts SET tz=?,reset_day=?,billing_mode=?,billing_iface=?,quota_bytes=?,note=?,settings_version=settings_version+1 WHERE id=? AND settings_version=?", o.TZ, o.ResetDay, o.BillingMode, o.BillingIface, p.Quota, p.Note, h.ID, p.Version)
	if err != nil {
		writeErr(w, 500, "保存失败")
		return
	}
	n, _ := rr.RowsAffected()
	if n != 1 {
		writeErr(w, 409, "主机设置已更新，请刷新后重试")
		return
	}

	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) handleConfigSet(w http.ResponseWriter, r *http.Request) {
	h, e := s.st.HostByName(r.Context(), r.PathValue("host"))
	if e != nil {
		writeErr(w, 404, "主机不存在")
		return
	}
	var c proto.DesiredConfig
	if e = json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&c); e != nil {
		writeErr(w, 400, "配置无效")
		return
	}
	next, e := s.st.SetHostConfig(r.Context(), h.ID, c.Version, c.Policy)
	if errors.Is(e, store.ErrConflict) {
		writeErr(w, 409, e.Error())
		return
	}
	if e != nil {
		writeErr(w, 400, e.Error())
		return
	}
	writeJSON(w, 200, next)
}
func (s *Server) agentHost(w http.ResponseWriter, r *http.Request) *store.Host {
	if !s.ingestRL.allow(s.clientIP(r), s.Now()) {
		writeErr(w, 429, "请求过于频繁")
		return nil
	}
	h, e := s.st.HostByToken(r.Context(), bearer(r))
	if e != nil {
		writeErr(w, 401, "主机 token 无效")
		return nil
	}
	return h
}
func (s *Server) handleAgentControl(w http.ResponseWriter, r *http.Request) {
	h := s.agentHost(w, r)
	if h == nil {
		return
	}
	c, e := s.st.HostConfig(r.Context(), h.ID)
	if e != nil {
		writeErr(w, 500, "读取配置失败")
		return
	}
	u, e := s.st.NextUpgrade(r.Context(), h.ID)
	if e != nil {
		writeErr(w, 500, "读取任务失败")
		return
	}
	writeJSON(w, 200, proto.ControlResponse{Config: c, Upgrade: u})
}
func (s *Server) handleAgentUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	h := s.agentHost(w, r)
	if h == nil {
		return
	}
	var p struct {
		ID      int64  `json:"id"`
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&p) != nil || len(p.Message) > 500 {
		writeErr(w, 400, "状态无效")
		return
	}
	if e := s.st.UpgradeStatus(r.Context(), h.ID, p.ID, p.Status, p.Message); e != nil {
		writeErr(w, 409, e.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) handleUpgradeCreate(w http.ResponseWriter, r *http.Request) {
	h, e := s.st.HostByName(r.Context(), r.PathValue("host"))
	if e != nil {
		writeErr(w, 404, "主机不存在")
		return
	}
	var m proto.Meta
	_ = json.Unmarshal([]byte(h.MetaJSON), &m)
	if m.Inventory == nil || m.Inventory.OS != "linux" {
		writeErr(w, 400, "需已上报平台信息的 Linux agent")
		return
	}
	var input struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
		writeErr(w, 400, "版本无效")
		return
	}
	release, e := s.releaseFor(r.Context(), input.Version, m.Inventory.Arch)
	if e != nil {
		writeErr(w, 400, e.Error())
		return
	}
	id, e := s.st.CreateUpgrade(r.Context(), h.ID, proto.UpgradeTask{Version: input.Version, URL: release.URL, SHA256: release.SHA256, CreatedAt: s.Now().Unix()})
	if e != nil {
		writeErr(w, 409, "主机已有进行中的升级任务")
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "status": "pending"})
}
func (s *Server) handleUpgradeList(w http.ResponseWriter, r *http.Request) {
	rows, e := s.st.R.QueryContext(r.Context(), `SELECT u.id,h.name,u.version,u.status,u.message,u.created_at,u.updated_at FROM upgrade_tasks u JOIN hosts h ON h.id=u.host_id ORDER BY u.id DESC LIMIT 100`)
	if e != nil {
		writeErr(w, 500, "读取任务失败")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, created, updated int64
		var host, version, status, msg string
		if e = rows.Scan(&id, &host, &version, &status, &msg, &created, &updated); e != nil {
			writeErr(w, 500, "读取任务失败")
			return
		}
		out = append(out, map[string]any{"id": id, "host": host, "version": version, "status": status, "message": msg, "created_at": created, "updated_at": updated})
	}
	writeJSON(w, 200, map[string]any{"tasks": out})
}
func (s *Server) enrichIPs(ctx context.Context, limit int) error {
	gs := s.geo.Status()
	epoch := max(gs.City.BuildTime, gs.ASN.BuildTime)
	if epoch == 0 {
		return nil
	}
	rows, e := s.st.R.QueryContext(ctx, `SELECT id,address FROM ips WHERE geo_at<? AND family>0 LIMIT ?`, epoch, limit)
	if e != nil {
		return e
	}
	type item struct {
		id int64
		ip string
	}
	items := []item{}
	for rows.Next() {
		var x item
		if e = rows.Scan(&x.id, &x.ip); e != nil {
			rows.Close()
			return e
		}
		items = append(items, x)
	}
	rows.Close()
	tx, e := s.st.W.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, x := range items {
		g := s.geo.Lookup(x.ip)
		if _, e = tx.ExecContext(ctx, `UPDATE ips SET country=?,country_name=?,asn=?,org=?,geo_at=? WHERE id=?`, g.CC, g.Country, g.ASN, g.Org, epoch, x.id); e != nil {
			return e
		}
	}
	return tx.Commit()
}
