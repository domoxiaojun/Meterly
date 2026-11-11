// Package server 中心服务:ingest、查询 API、面板静态文件。
package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/domoxiaojun/Meterly/internal/geo"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
	"github.com/domoxiaojun/Meterly/internal/version"
)

type Server struct {
	cfg         *Config
	st          *store.Store
	geo         *geo.DB
	log         *slog.Logger
	web         fs.FS
	Now         func() time.Time
	ReleaseGet  func(context.Context, string) ([]byte, error)
	mux         *http.ServeMux
	locMu       sync.Mutex
	locs        map[string]*time.Location
	jobMu       sync.Mutex
	runningJobs map[string]bool
	bgCtx       context.Context
	bgCancel    context.CancelFunc
	jobWG       sync.WaitGroup

	ingestRL *limiter // 每来源 IP
	authFail *limiter // 鉴权失败
	apiRL    *limiter
	enrollRL *limiter // 注册码核销(更严)
}

// Geo 归属地库(serve 用它启动更新器)。
func (s *Server) Geo() *geo.DB { return s.geo }

func New(cfg *Config, st *store.Store, web fs.FS, log *slog.Logger) *Server {
	st.Retention = cfg.Retention
	st.Retention.Defaults()
	s := &Server{cfg: cfg, st: st, geo: geo.Open(cfg.Geo.Dir), web: web, log: log, Now: time.Now, locs: map[string]*time.Location{},
		ingestRL: newLimiter(5, 60), authFail: newLimiter(0.2, 10), apiRL: newLimiter(20, 100),
		enrollRL: newLimiter(0.2, 5)}
	s.ReleaseGet = releaseGet
	s.runningJobs = map[string]bool{}
	s.bgCtx, s.bgCancel = context.WithCancel(context.Background())
	m := http.NewServeMux()
	m.HandleFunc("POST /api/v2/ingest", s.handleIngest)
	m.HandleFunc("POST /api/v2/enroll/redeem", s.handleEnrollRedeem)
	m.HandleFunc("POST /api/login", s.handleLogin)
	m.HandleFunc("POST /api/logout", s.handleLogout)
	m.HandleFunc("GET /api/v2/me", s.panel(s.handleMe))
	m.HandleFunc("GET /api/v2/overview", s.panel(s.handleDashboard))
	m.HandleFunc("GET /api/v2/billing/overview", s.panel(s.handleOverview))
	m.HandleFunc("GET /api/v2/analytics/rank", s.panel(s.handleAnalysis))
	m.HandleFunc("GET /api/v2/analytics/series", s.panel(s.handleAnalysisSeries))
	m.HandleFunc("GET /api/v2/analytics/export", s.panel(s.handleAnalysisExport))
	m.HandleFunc("GET /api/v2/analytics/options", s.panel(s.handleAnalysisOptions))
	m.HandleFunc("GET /api/v2/hosts", s.panel(s.handleHostList))
	m.HandleFunc("PUT /api/v2/hosts/{host}", s.panel(s.jsonOnly(s.handleHostSettings)))
	m.HandleFunc("PUT /api/v2/hosts/{host}/config", s.panel(s.jsonOnly(s.handleConfigSet)))
	m.HandleFunc("POST /api/v2/hosts/{host}/upgrade", s.panel(s.jsonOnly(s.handleUpgradeCreate)))
	m.HandleFunc("GET /api/v2/upgrades", s.panel(s.handleUpgradeList))
	m.HandleFunc("GET /api/v2/releases", s.panel(s.handleRelease))
	m.HandleFunc("GET /api/v2/system", s.panel(s.handleSystem))
	m.HandleFunc("POST /api/v2/system/jobs/{kind}", s.panel(s.jsonOnly(s.handleRunJob)))
	m.HandleFunc("GET /api/v2/install", s.panel(s.handleInstallInfo))
	m.HandleFunc("GET /api/v2/agent/control", s.handleAgentControl)
	m.HandleFunc("POST /api/v2/agent/upgrade/status", s.handleAgentUpgradeStatus)
	m.HandleFunc("GET /api/v2/series", s.panel(s.handleSeries))
	m.HandleFunc("GET /api/v2/singbox/top", s.panel(s.handleTopIPs))
	m.HandleFunc("GET /api/v2/singbox/inbounds", s.panel(s.handleInbounds))
	m.HandleFunc("GET /api/v2/singbox/ip", s.panel(s.handleIPDetail))
	m.HandleFunc("GET /api/v2/singbox/geo", s.panel(s.handleGeoSummary))
	m.HandleFunc("GET /api/v2/reconcile", s.panel(s.handleReconcile))
	m.HandleFunc("GET /api/v2/provider", s.panel(s.handleProviderList))
	m.HandleFunc("POST /api/v2/provider", s.panel(s.jsonOnly(s.handleProviderSet)))
	m.HandleFunc("DELETE /api/v2/provider", s.panel(s.handleProviderDelete))
	m.HandleFunc("POST /api/v2/vnstat/check", s.panel(s.jsonOnly(s.handleVnstatCheck)))
	m.HandleFunc("GET /api/v2/geo/status", s.panel(s.handleGeoStatus))
	m.HandleFunc("GET /api/v2/netbird", s.panel(s.handleNetbird))
	m.HandleFunc("GET /api/v2/xray", s.panel(s.handleXray))
	m.HandleFunc("GET /api/v2/collectors", s.panel(s.handleCollectors))
	m.HandleFunc("GET /api/v2/enroll", s.panel(s.handleEnrollList))
	m.HandleFunc("POST /api/v2/enroll", s.panel(s.jsonOnly(s.handleEnrollCreate)))
	m.HandleFunc("DELETE /api/v2/enroll", s.panel(s.handleEnrollDelete))
	m.HandleFunc("GET /healthz", s.handleHealthz)
	if web != nil {
		m.Handle("GET /", http.FileServerFS(web))
	}
	s.mux = m
	return s
}

// Handler 带安全头与访问日志(不记录 query、header、body)。
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		sw := &statusWriter{ResponseWriter: w, code: 200}
		s.mux.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") && (sw.code >= 400 || r.URL.Path != "/api/v2/ingest") {
			s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.code, "ms", time.Since(start).Milliseconds(), "ip", s.clientIP(r))
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, aerr := netip.ParseAddr(host)
	for _, p := range s.cfg.proxies {
		if aerr == nil && p.Contains(a.Unmap()) {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.Split(xff, ",")
				return strings.TrimSpace(parts[len(parts)-1])
			}
		}
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func bearer(r *http.Request) string {
	a := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(a, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// ---- ingest(host token 鉴权,与面板分离) ----

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	ip, now := s.clientIP(r), s.Now()
	if !s.ingestRL.allow(ip, now) {
		writeErr(w, http.StatusTooManyRequests, "请求过于频繁")
		return
	}
	tok := bearer(r)
	if tok == "" {
		writeErr(w, http.StatusUnauthorized, "缺少 token")
		return
	}
	h, err := s.st.HostByToken(r.Context(), tok)
	if err != nil {
		if !s.authFail.allow(ip, now) {
			writeErr(w, http.StatusTooManyRequests, "鉴权失败次数过多")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			s.log.Warn("ingest 鉴权失败", "ip", ip)
			writeErr(w, http.StatusUnauthorized, "token 无效或已吊销")
			return
		}
		writeErr(w, http.StatusInternalServerError, "内部错误")
		return
	}
	var b proto.Batch
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	if err := dec.Decode(&b); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求体过大")
			return
		}
		writeErr(w, http.StatusBadRequest, "JSON 无效")
		return
	}
	if len(b.Records) > proto.MaxRecordsPerBatch {
		writeErr(w, http.StatusRequestEntityTooLarge, "单批记录过多")
		return
	}
	if b.Meta.Protocol != proto.Version {
		writeErr(w, http.StatusUpgradeRequired, "需要新版 agent 协议 v2，请成套升级")
		return
	}
	res, err := s.st.Ingest(r.Context(), h, s.loc(h.TZ), b.Meta, b.Records, ip, now)
	if err != nil {
		s.log.Error("ingest 写入失败", "host", h.Name, "err", err.Error())
		writeErr(w, http.StatusServiceUnavailable, "写入失败,请稍后重试")
		return
	}
	if res.Rejected > 0 {
		s.log.Warn("ingest 跳过非法记录", "host", h.Name, "rejected", res.Rejected)
	}
	_ = s.enrichIPs(r.Context(), proto.MaxRecordsPerBatch)
	writeJSON(w, http.StatusOK, proto.IngestResponse{Accepted: res.Accepted, Duplicates: res.Duplicates, Rejected: res.Rejected, Host: h.Name, MaxSeq: res.MaxSeq})
}

func (s *Server) loc(name string) *time.Location {
	s.locMu.Lock()
	defer s.locMu.Unlock()
	if l, ok := s.locs[name]; ok {
		return l
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		l = s.cfg.loc
	}
	s.locs[name] = l
	return l
}

// ---- 面板鉴权:独立面板口令 -> HttpOnly 会话 Cookie ----

const sessionCookie = "ts_session"
const sessionTTL = 7 * 24 * time.Hour

func (s *Server) sessionMAC(exp string) string {
	k := sha256.Sum256([]byte("meterly-session\x00" + s.cfg.panelToken))
	m := hmac.New(sha256.New, k[:])
	m.Write([]byte(exp))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) authed(r *http.Request) bool {
	if s.cfg.panelToken == "" {
		return s.cfg.Loopback() // 未配置口令时仅允许回环监听(开发/本机)
	}
	if t := bearer(r); t != "" {
		return subtle.ConstantTimeCompare([]byte(t), []byte(s.cfg.panelToken)) == 1
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	exp, mac, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(s.sessionMAC(exp))) {
		return false
	}
	e, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && s.Now().Unix() < e
}

func (s *Server) panel(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.apiRL.allow(s.clientIP(r), s.Now()) {
			writeErr(w, http.StatusTooManyRequests, "请求过于频繁")
			return
		}
		if !s.authed(r) {
			writeErr(w, http.StatusUnauthorized, "需要登录")
			return
		}
		h(w, r)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip, now := s.clientIP(r), s.Now()
	if !s.authFail.allow(ip, now) {
		writeErr(w, http.StatusTooManyRequests, "尝试次数过多,请稍后再试")
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil || s.cfg.panelToken == "" ||
		subtle.ConstantTimeCompare([]byte(in.Token), []byte(s.cfg.panelToken)) != 1 {
		s.log.Warn("面板登录失败", "ip", ip)
		writeErr(w, http.StatusUnauthorized, "口令错误")
		return
	}
	exp := strconv.FormatInt(now.Add(sessionTTL).Unix(), 10)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: exp + "." + s.sessionMAC(exp), Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", MaxAge: int(sessionTTL.Seconds())})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version.Version, "tz": s.cfg.TZ,
		"auth": s.cfg.panelToken != ""})
}

// RunMaintenance 周期执行保留与 Top-N 合并。
func (s *Server) RunMaintenance(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		n, err := s.st.Maintain(ctx, s.cfg.Retention, s.Now())
		if err != nil {
			s.log.Error("维护任务失败", "err", err.Error())
		} else if n > 0 {
			s.log.Info("Top-N 合并完成", "merged_rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) Close() { s.bgCancel(); s.jobWG.Wait(); s.geo.Close() }
