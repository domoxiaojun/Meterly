// Package agent 实现采集端:采集 -> 计算增量 -> 写本地缓冲 -> 推送中心。
// agent 只发起出站连接,不监听任何端口;只用标准库。
package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/version"
)

type Agent struct {
	mu      sync.Mutex
	cfg     *Config
	buf     *Buffer
	st      *State
	netdev  *NetDev
	clash   *ClashClient
	sb      *SingboxAcc
	site    *SiteAcc
	netbird *NetbirdAcc
	xray    *XrayAcc
	client  *http.Client
	log     *slog.Logger

	// 可注入,便于测试伪造时间/开机 ID/clash 拉取
	Now    func() time.Time
	BootID func() string

	health        map[string]proto.CollectorHealth
	backoff       time.Duration
	nextTry       time.Time
	lastSentMeta  time.Time
	baseConfig    Config
	inventory     *proto.Inventory
	desired       proto.DesiredConfig
	configVersion int64
	configError   string
	nextDiscovery time.Time
	DetectSys     *DetectSys
	upload        chan struct{}
}

func New(cfg *Config, log *slog.Logger) (*Agent, error) {
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, err
	}
	st, err := LoadState(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	buf, err := OpenBuffer(cfg.StateDir+"/buffer", cfg.BufferMaxBytes)
	if err != nil {
		return nil, err
	}
	// 崩溃恢复:补写已记入 state 但未落入缓冲的记录
	if last := buf.LastSeq(); len(st.Pending) > 0 {
		var miss []proto.Record
		for _, r := range st.Pending {
			if r.Seq > last {
				miss = append(miss, r)
			}
		}
		if len(miss) > 0 {
			if err := buf.AppendChunks(miss); err != nil && !errors.Is(err, ErrBufferFull) {
				return nil, err
			}
			log.Info("崩溃恢复:已补写待写记录", "n", len(miss))
		}
		if len(st.Pending) > 0 && buf.LastSeq() >= st.Pending[len(st.Pending)-1].Seq {
			st.Pending = nil
		}
	}
	if last := buf.LastSeq(); st.NextSeq <= last {
		st.NextSeq = last + 1
	}
	tr := &http.Transport{
		Proxy:               nil, // 不走环境代理
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		MaxIdleConns:        2,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("ca_file 无有效证书")
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	a := &Agent{
		cfg: cfg, buf: buf, st: st, log: log,
		client: &http.Client{Transport: tr, Timeout: cfg.HTTPTimeout.Duration},
		Now:    time.Now, BootID: ReadBootID,
		health: map[string]proto.CollectorHealth{},
	}
	a.baseConfig = *cfg
	if cfg.Collectors.NetDev.Enabled {
		a.netdev = &NetDev{Path: cfg.Collectors.NetDev.ProcPath, Ifaces: cfg.Collectors.NetDev.Interfaces}
		a.health["netdev"] = proto.CollectorHealth{Enabled: true}
	}
	if cfg.Collectors.Singbox.Enabled {
		a.clash = NewClashClient(cfg.Collectors.Singbox)
		a.sb = NewSingboxAcc(st.Singbox)
		a.sb.Restart = func() { a.log.Info("检测到 sing-box 重启(全局计数下降),连接状态已重置") }
		a.health["singbox"] = proto.CollectorHealth{Enabled: true}
	}
	if cfg.Collectors.Site.Enabled {
		a.site = NewSiteAcc(cfg.Collectors.Site, st.SiteOffsets)
		a.health["site"] = proto.CollectorHealth{Enabled: true}
	}
	if cfg.Collectors.Netbird.Enabled {
		a.netbird = NewNetbirdAcc(cfg.Collectors.Netbird, st.Netbird)
		a.health["netbird"] = proto.CollectorHealth{Enabled: true}
	}
	if cfg.Collectors.Xray.Enabled {
		a.xray = NewXrayAcc(cfg.Collectors.Xray, st.Xray)
		a.health["xray"] = proto.CollectorHealth{Enabled: true}
	}
	return a, nil
}

func (a *Agent) Close() error {
	if a.site != nil {
		a.site.Close()
	}
	return a.buf.Close()
}

// Buffer 暴露缓冲(测试/诊断用)。
func (a *Agent) Buffer() *Buffer { return a.buf }

// SetNetbirdExec / SetXrayExec 注入外部命令(测试用)。
func (a *Agent) SetNetbirdExec(fn ExecFunc) {
	if a.netbird != nil {
		a.netbird.Exec = fn
	}
}
func (a *Agent) SetXrayExec(fn ExecFunc) {
	if a.xray != nil {
		a.xray.Exec = fn
	}
}

func ReadBootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}

func (a *Agent) markOK(name string, now int64, note string) {
	h := a.health[name]
	h.LastOK, h.Fails, h.Note = now, 0, note
	a.health[name] = h
}

func (a *Agent) markErr(name string, now int64, err error) {
	h := a.health[name]
	h.Fails++
	h.LastErr, h.ErrAt = truncate(err.Error(), 200), now
	a.health[name] = h
	if h.Fails == 1 || h.Fails%30 == 0 {
		a.log.Warn("采集失败", "collector", name, "fails", h.Fails, "err", h.LastErr)
	}
}

// Poll 轮询一次 sing-box(每 poll_interval 调用)。
func (a *Agent) Poll(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sb == nil {
		return
	}
	now := a.Now().Unix()
	snap, err := a.clash.Fetch(ctx)
	if err != nil {
		a.markErr("singbox", now, err)
		return
	}
	a.sb.Apply(snap)
	a.markOK("singbox", now, fmt.Sprintf("连接 %d,重启 %d 次", len(snap.Connections), a.sb.St.Restarts))
}

// Tick 汇总一次(每 interval 调用):网卡增量 + sing-box 累计 -> 记录 -> state -> 缓冲。返回新增记录数。
func (a *Agent) Tick(ctx context.Context) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// 上次提交未完成时先重试；期间不继续消费日志或 Drain 累加器。
	if err := a.commitPending(); err != nil {
		return 0, err
	}
	now := a.Now().Unix()
	prevTick := a.st.LastTick
	var recs []proto.Record
	add := func(c Counter, prevTS int64) {
		ts := now
		pts := prevTS
		if c.TS > 0 { // 站点日志按请求结束所在分钟归因
			pts, ts = c.TS, c.TS+60
		}
		recs = append(recs, proto.Record{Seq: a.st.NextSeq, TS: ts, PrevTS: pts, Kind: c.Kind, Dim1: c.Key, Dim2: c.Key2, RX: c.RX, TX: c.TX, Site: c.Site})
		a.st.NextSeq++
	}
	if a.netdev != nil {
		boot := a.BootID()
		cs, err := a.netdev.Collect()
		if err != nil {
			a.markErr("netdev", now, err)
		} else {
			a.markOK("netdev", now, "")
			for _, cur := range cs {
				prev, had := a.st.Ifaces[cur.Key]
				d, ok := a.ifaceDelta(prev, had, cur, boot, now)
				a.st.Ifaces[cur.Key] = IfaceState{BootID: boot, RX: cur.RX, TX: cur.TX, TS: now}
				if ok && (d.RX > 0 || d.TX > 0) {
					pts := int64(0)
					if prev.BootID == boot {
						pts = prev.TS
					}
					add(d, pts)
				}
			}
		}
	}
	if a.sb != nil {
		for _, c := range a.sb.Drain() {
			add(c, prevTick)
		}
		a.st.Singbox = a.sb.St
	}
	if a.site != nil && a.buf.Size() > a.cfg.BufferMaxBytes*3/4 {
		a.markErr("site", now, errors.New("缓冲积压，暂缓读取日志；请保留轮转文件直到追平"))
	} else if a.site != nil {
		got, st, err := a.site.Collect()
		if err != nil {
			a.markErr("site", now, err)
		} else {
			a.markOK("site", now, fmt.Sprintf("文件 %d,新行 %d,解析失败 %d,IP待识别 %d,积压源 %d", st.Files, st.Used, st.Failed, st.Unresolved, st.Backlogged))
			if st.Failed > 0 || st.Unresolved > 0 {
				a.markErr("site", now, fmt.Errorf("解析失败 %d 行，客户端 IP 待识别 %d 行", st.Failed, st.Unresolved))
			}
		}
		for _, c := range got {
			add(c, prevTick)
		}
		a.st.SiteOffsets = a.site.Offsets()
	}
	if a.netbird != nil {
		got, err := a.netbird.Collect(ctx)
		if err != nil {
			a.markErr("netbird", now, err)
		} else {
			note := fmt.Sprintf("对等端 %d", len(a.netbird.St))
			if a.netbird.Self != "" {
				note += ",本机 " + a.netbird.Self
			}
			a.markOK("netbird", now, note)
		}
		for _, c := range got {
			add(c, prevTick)
		}
		a.st.Netbird = a.netbird.St
	}
	if a.xray != nil {
		got, err := a.xray.Collect(ctx)
		if err != nil {
			a.markErr("xray", now, err)
		} else {
			a.markOK("xray", now, fmt.Sprintf("指标 %d(无来源 IP)", len(a.xray.St)))
		}
		for _, c := range got {
			add(c, prevTick)
		}
		a.st.Xray = a.xray.St
	}
	a.st.LastTick = now
	a.st.Pending = recs
	if err := a.commitPending(); err != nil {
		return 0, err
	}
	return len(recs), nil
}

func (a *Agent) commitPending() error {
	if err := a.st.Save(a.cfg.StateDir); err != nil {
		return fmt.Errorf("写 state 失败: %w", err)
	}
	last := a.buf.LastSeq()
	var pending []proto.Record
	for _, r := range a.st.Pending {
		if r.Seq > last {
			pending = append(pending, r)
		}
	}
	if err := a.buf.AppendChunks(pending); err != nil {
		return fmt.Errorf("写缓冲失败: %w", err)
	}

	a.st.Pending = nil
	return a.st.Save(a.cfg.StateDir)
}

func (a *Agent) ifaceDelta(prev IfaceState, had bool, cur Counter, boot string, now int64) (Counter, bool) {
	d := Counter{Kind: proto.KindIface, Key: cur.Key}
	switch {
	case !had:
		return d, false // 首次:只建立基线
	case prev.BootID != boot:
		d.RX, d.TX = cur.RX, cur.TX // 重启:内核计数从 0 开始,开机以来累计即增量
		a.log.Info("检测到主机重启,按开机后累计计入", "iface", cur.Key)
		return d, true
	}
	elapsed := now - prev.TS
	if elapsed <= 0 {
		elapsed = int64(a.cfg.Interval.Seconds())
	}
	nd := a.cfg.Collectors.NetDev
	maxDelta := uint64(math.Min(nd.MaxRateMbps*1e6/8*float64(elapsed), math.MaxUint64/2))
	var rr, tr DeltaReason
	d.RX, rr = CounterDelta(prev.RX, cur.RX, nd.CounterBits, maxDelta)
	d.TX, tr = CounterDelta(prev.TX, cur.TX, nd.CounterBits, maxDelta)
	if rr != DeltaNormal || tr != DeltaNormal {
		a.log.Info("网卡计数器非单调", "iface", cur.Key, "rx", string(rr), "tx", string(tr))
	}
	return d, true
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (a *Agent) meta(buffered int64) proto.Meta {
	a.mu.Lock()
	defer a.mu.Unlock()
	hn, _ := os.Hostname()
	h := make(map[string]proto.CollectorHealth, len(a.health))
	for k, v := range a.health {
		h[k] = v
	}
	return proto.Meta{Hostname: hn, AgentVersion: version.Version, InstanceID: a.st.InstanceID, Timezone: localTZ(),
		BootID: a.BootID(), SentAt: a.Now().Unix(), Buffered: buffered, Dropped: a.st.Dropped, Collectors: h,
		Protocol: proto.Version, DroppedFrom: a.st.DroppedFrom, DroppedTo: a.st.DroppedTo,
		Inventory: a.inventory, ConfigVersion: a.configVersion, ConfigError: a.configError}
}

func localTZ() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		return strings.TrimSpace(string(b))
	}
	if l, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(l, "zoneinfo/"); i >= 0 {
			return l[i+9:]
		}
	}
	return time.Local.String()
}

// ErrUnauthorized token 无效或已吊销,不应频繁重试。
var ErrUnauthorized = errors.New("中心拒绝:token 无效或已吊销")

// Flush 把缓冲分批推送(每次最多 maxBatches 批),返回已确认条数。
func (a *Agent) Flush(ctx context.Context, maxBatches int) (int, error) {
	sent := 0
	for i := 0; i < maxBatches; i++ {
		recs, err := a.buf.Pending(a.cfg.BatchSize)
		if err != nil {
			return sent, err
		}
		if len(recs) == 0 {
			if sent == 0 {
				_, err = a.post(ctx, proto.Batch{Meta: a.meta(0), Records: []proto.Record{}})
			}
			return sent, err
		}
		left := a.buf.Count() - int64(len(recs))
		ir, err := a.post(ctx, proto.Batch{Meta: a.meta(left), Records: recs})
		if err != nil {
			return sent, err
		}
		if ir.MaxSeq < recs[len(recs)-1].Seq {
			return sent, errors.New("中心未确认完整序列，保留缓冲")
		}
		if ir.Rejected > 0 {
			a.log.Error("中心拒绝了记录", "count", ir.Rejected)
		}
		if err := a.buf.Ack(recs[len(recs)-1].Seq); err != nil {
			return sent, err
		}
		sent += len(recs)
	}
	return sent, nil
}

func (a *Agent) post(ctx context.Context, b proto.Batch) (*proto.IngestResponse, error) {
	body, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.cfg.ServerURL, "/")+"/api/v2/ingest", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("User-Agent", "meterly-agent/"+version.Version)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("推送失败: %s", netErrKind(err))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrUnauthorized
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("推送失败: HTTP %d", resp.StatusCode)
	}
	var ir proto.IngestResponse
	if err := json.Unmarshal(data, &ir); err != nil {
		return nil, errors.New("响应解析失败")
	}
	return &ir, nil
}

// Run 常驻:每 poll_interval 轮询 sing-box,每 interval(对齐整分钟)汇总并推送;
// 推送失败指数退避(最长 5 分钟,token 失效 30 分钟),期间继续采集写缓冲。
func (a *Agent) Run(ctx context.Context) error {
	ready, _ := json.Marshal(map[string]any{"version": version.Version, "pid": os.Getpid(), "at": a.Now().Unix()})
	if e := writeFileAtomic(filepath.Join(a.cfg.StateDir, "ready.json"), ready); e != nil {
		return e
	}
	if err := a.RefreshConfiguration(ctx, true); err != nil {
		a.log.Warn("初始化自动发现", "err", err)
	}
	a.upload = make(chan struct{}, 1)
	var worker sync.WaitGroup
	worker.Add(1)
	go func() {
		defer worker.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.upload:
				a.flushCycle(ctx)
			}
		}
	}()
	defer worker.Wait()
	iv := a.cfg.Interval.Duration
	step := iv
	if a.sb != nil {
		step = a.cfg.Collectors.Singbox.PollInterval.Duration
	}
	a.log.Info("agent 启动", "version", version.Version, "config", a.cfg.Redacted(), "buffered", a.buf.Count())
	a.Poll(ctx)
	nextTick := a.Now().Truncate(iv).Add(iv)
	for {
		now := a.Now()
		step = iv
		if a.sb != nil {
			step = a.cfg.Collectors.Singbox.PollInterval.Duration
		}
		wake := now.Truncate(step).Add(step)
		if wake.After(nextTick) {
			wake = nextTick
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wake.Sub(now)):
		}
		a.Poll(ctx)
		if !a.Now().Before(nextTick) {
			a.Cycle(ctx)
			nextTick = a.Now().Truncate(iv).Add(iv)
		}
	}
}

// Cycle 汇总一次并尝试推送(带退避)。
func (a *Agent) Cycle(ctx context.Context) {
	if _, err := a.Tick(ctx); err != nil {
		a.log.Error("汇总失败", "err", err.Error())
	}
	if a.upload != nil {
		select {
		case a.upload <- struct{}{}:
		default:
		}
		if err := a.RefreshConfiguration(ctx, false); err != nil {
			a.log.Warn("配置同步失败", "err", err)
		}
		return
	}
	a.flushCycle(ctx)
}

func (a *Agent) flushCycle(ctx context.Context) {
	if a.Now().Before(a.nextTry) {
		return
	}
	n, err := a.Flush(ctx, 30)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnauthorized):
			a.backoff = 30 * time.Minute
		case a.backoff == 0:
			a.backoff = a.cfg.Interval.Duration
		default:
			a.backoff = min(a.backoff*2, 5*time.Minute)
		}
		a.nextTry = a.Now().Add(a.backoff)
		a.log.Warn("中心不可达,数据保留在本地缓冲", "err", err.Error(), "buffered", a.buf.Count(), "retry_in", a.backoff.String())
		return
	}
	if a.backoff > 0 {
		a.log.Info("中心恢复,缓冲补发中", "sent", n, "left", a.buf.Count())
	}
	a.backoff = 0
}

// Check 自检:读取一次网卡、拉取一次 sing-box(不推送、不写缓冲),打印摘要。返回退出码。
func (a *Agent) Check(ctx context.Context, w io.Writer) int {
	code := 0
	fmt.Fprintln(w, "配置:", a.cfg.Redacted())
	if a.netdev != nil {
		cs, err := a.netdev.Collect()
		if err != nil {
			fmt.Fprintln(w, "网卡: 失败 -", err)
			code = 1
		}
		for _, c := range cs {
			fmt.Fprintf(w, "网卡 %s: rx=%d tx=%d(累计)\n", c.Key, c.RX, c.TX)
		}
	}
	if a.clash != nil {
		snap, err := a.clash.Fetch(ctx)
		if err != nil {
			fmt.Fprintln(w, "sing-box: 失败 -", err)
			code = 1
		} else {
			fmt.Fprintf(w, "sing-box: 连接 %d,全局 上传=%d 下载=%d\n", len(snap.Connections), snap.UploadTotal, snap.DownloadTotal)
		}
	}
	if a.site != nil {
		fmt.Fprintf(w, "站点: %d 个日志源(只读,按请求结束时刻归因)\n", len(a.cfg.Collectors.Site.Sources))
		for _, s := range a.cfg.Collectors.Site.Sources {
			fmt.Fprintf(w, "  %s ← %s\n", s.Site, s.Path)
		}
	}
	if a.netbird != nil {
		got, err := a.netbird.Collect(ctx)
		if err != nil {
			fmt.Fprintln(w, "NetBird: 失败 -", err)
			code = 1
		} else {
			fmt.Fprintf(w, "NetBird: 对等端 %d(本轮增量 %d 条)\n", len(a.netbird.St), len(got))
		}
	}
	if a.xray != nil {
		got, err := a.xray.Collect(ctx)
		if err != nil {
			fmt.Fprintln(w, "xray: 失败 -", err)
			code = 1
		} else {
			fmt.Fprintf(w, "xray: 指标 %d,本轮增量 %d 条(无来源 IP)\n", len(a.xray.St), len(got))
		}
	}
	fmt.Fprintf(w, "缓冲: %d 条待发送,%d 字节,实例 %s,累计丢弃 %d\n", a.buf.Count(), a.buf.Size(), a.st.InstanceID, a.st.Dropped)
	return code
}
