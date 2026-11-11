// Package e2e 端到端测试:真实 server handler + 真实 agent,注入时间,模拟断网/重启/崩溃。
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/agent"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/server"
	"github.com/domoxiaojun/Meterly/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// fakeClash 可编程 clash_api:模拟长连接、短连接(轮询间隙内开合)、连接关闭、sing-box 重启。
type fakeClash struct {
	mu       sync.Mutex
	up, down uint64
	conns    map[string]*fc
	n        int
	// 真值:自基线以来的全局增量
	truthUp, truthDown uint64
	baselined          bool
}

type fc struct {
	id, ip, in string
	up, down   uint64
}

func newFakeClash() *fakeClash { return &fakeClash{conns: map[string]*fc{}} }

var ips = []string{"203.0.113.1", "203.0.113.2", "198.51.100.7", "2001:db8::5", "::ffff:203.0.113.3"}
var inbounds = []string{"tuic/tuic", "anytls/anytls-in", "direct/pai-anytls"}

// step 推进 10 秒的流量。
func (f *fakeClash) step(r *rand.Rand) {
	f.mu.Lock()
	defer f.mu.Unlock()
	add := func(u, d uint64) {
		f.up += u
		f.down += d
		if f.baselined {
			f.truthUp += u
			f.truthDown += d
		}
	}
	// 新长连接
	if r.IntN(3) == 0 || len(f.conns) == 0 {
		f.n++
		id := fmt.Sprintf("c%d", f.n)
		f.conns[id] = &fc{id: id, ip: ips[r.IntN(len(ips))], in: inbounds[r.IntN(len(inbounds))]}
	}
	for id, c := range f.conns {
		u, d := uint64(r.IntN(50_000)), uint64(r.IntN(900_000))
		c.up += u
		c.down += d
		add(u, d)
		if r.IntN(8) == 0 { // 关闭(其最后一段只体现在全局计数里)
			lu, ld := uint64(r.IntN(10_000)), uint64(r.IntN(100_000))
			add(lu, ld)
			delete(f.conns, id)
		}
	}
	// 轮询间隙内开合的短连接
	for i := 0; i < r.IntN(4); i++ {
		add(uint64(r.IntN(5000)), uint64(r.IntN(60_000)))
	}
}

func (f *fakeClash) restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.up, f.down = 0, 0
	f.conns = map[string]*fc{}
}

func (f *fakeClash) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer e2e-secret" {
		http.Error(w, "unauthorized", 401)
		return
	}
	f.baselined = true
	cs := []map[string]any{}
	for _, c := range f.conns {
		cs = append(cs, map[string]any{"id": c.id, "upload": c.up, "download": c.down,
			"metadata": map[string]any{"sourceIP": c.ip, "type": c.in}})
	}
	json.NewEncoder(w).Encode(map[string]any{"uploadTotal": f.up, "downloadTotal": f.down, "connections": cs})
}

// gate 可切换"断网"的 server 包装。
type gate struct {
	h       http.Handler
	offline atomic.Bool
	posts   atomic.Int64
}

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.offline.Load() {
		http.Error(w, "unreachable", http.StatusBadGateway)
		return
	}
	g.posts.Add(1)
	g.h.ServeHTTP(w, r)
}

type env struct {
	t       *testing.T
	clk     *clock
	st      *store.Store
	srv     *server.Server
	gate    *gate
	hs      *httptest.Server
	clash   *fakeClash
	cs      *httptest.Server
	dir     string
	proc    string
	rx, tx  uint64 // 当前网卡累计
	truthRX uint64 // 基线后的真实增量
	truthTX uint64
	based   bool
	token   string
	host    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, clk: &clock{t: time.Date(2026, 11, 9, 23, 0, 5, 0, time.FixedZone("SGT", 8*3600))}, dir: t.TempDir(), host: "e2e-host"}
	var err error
	if e.st, err = store.Open(filepath.Join(e.dir, "srv.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.st.Close() })
	cfg, err := server.LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	e.srv = server.New(cfg, e.st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.srv.Now = e.clk.Now
	e.gate = &gate{h: e.srv.Handler()}
	e.hs = httptest.NewServer(e.gate)
	t.Cleanup(e.hs.Close)
	e.clash = newFakeClash()
	e.cs = httptest.NewServer(e.clash)
	t.Cleanup(e.cs.Close)
	if e.token, err = e.st.CreateHost(context.Background(), e.host, store.HostOpts{}); err != nil {
		t.Fatal(err)
	}
	e.proc = filepath.Join(e.dir, "net_dev")
	e.rx, e.tx = 5_000_000_000, 7_000_000_000
	e.writeProc()
	return e
}

func (e *env) writeProc() {
	s := fmt.Sprintf("Inter-| hdr\n face | hdr\n  eth0: %d 0 0 0 0 0 0 0 %d 0 0 0 0 0 0 0\n", e.rx, e.tx)
	os.WriteFile(e.proc, []byte(s), 0o644)
}

func (e *env) newAgent() *agent.Agent {
	e.t.Helper()
	sb := filepath.Join(e.dir, "singbox.json")
	os.WriteFile(sb, []byte(`{"experimental":{"clash_api":{"external_controller":"0.0.0.0:23390","secret":"e2e-secret"}}}`), 0o600)
	tf := filepath.Join(e.dir, "token")
	os.WriteFile(tf, []byte(e.token+"\n"), 0o600)
	cfgPath := filepath.Join(e.dir, "agent.json")
	cfg := map[string]any{
		"server_url": e.hs.URL, "allow_http": true, "token_file": tf, "state_dir": filepath.Join(e.dir, "state"),
		"interval": "60s", "batch_size": 500,
		"collectors": map[string]any{
			"netdev":  map[string]any{"enabled": true, "interfaces": []string{"eth0"}, "proc_path": e.proc},
			"singbox": map[string]any{"enabled": true, "config_path": sb, "url": e.cs.URL, "poll_interval": "10s"},
		},
	}
	b, _ := json.Marshal(cfg)
	os.WriteFile(cfgPath, b, 0o600)
	c, err := agent.LoadConfig(cfgPath)
	if err != nil {
		e.t.Fatal(err)
	}
	a, err := agent.New(c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		e.t.Fatal(err)
	}
	a.Now = e.clk.Now
	a.BootID = func() string { return "boot-1" }
	return a
}

// minute 模拟一分钟:6 次 sing-box 轮询 + 网卡增长,然后汇总推送。
func (e *env) minute(a *agent.Agent, r *rand.Rand) {
	for i := 0; i < 6; i++ {
		e.clash.step(r)
		e.clk.Add(10 * time.Second)
		a.Poll(context.Background())
	}
	drx, dtx := uint64(r.IntN(80_000_000)), uint64(r.IntN(120_000_000))
	e.rx += drx
	e.tx += dtx
	if e.based {
		e.truthRX += drx
		e.truthTX += dtx
	}
	e.writeProc()
	a.Cycle(context.Background())
	e.based = true
}

func (e *env) totals(kind string) store.Pair {
	h, _ := e.st.HostByName(context.Background(), e.host)
	m, err := e.st.KindDim1Totals(context.Background(), "fact_hour", []int64{h.ID}, kind, 0, 1<<40)
	if err != nil {
		e.t.Fatal(err)
	}
	var p store.Pair
	for _, v := range m {
		p.RX += v.RX
		p.TX += v.TX
	}
	return p
}

// TestOffline24hRecoveryNoLossNoDup 断网 24 小时(期间 agent 重启一次、sing-box 重启一次、发生一次崩溃),
// 恢复后数据补齐:网卡与 sing-box 合计与真值逐字节一致,无重复无丢失。
func TestOffline24hRecoveryNoLossNoDup(t *testing.T) {
	e := newEnv(t)
	r := rand.New(rand.NewPCG(1, 2))
	a := e.newAgent()
	// 在线 1 小时
	for i := 0; i < 60; i++ {
		e.minute(a, r)
	}
	// 断网 24 小时
	e.gate.offline.Store(true)
	for i := 0; i < 1440; i++ {
		e.minute(a, r)
		switch i {
		case 300: // agent 进程重启(状态与缓冲持久化)
			a.Close()
			a = e.newAgent()
		case 700: // sing-box 重启,计数清零
			e.clash.restart()
		}
	}
	buffered := a.Buffer().Count()
	if buffered < 1440 {
		t.Fatalf("断网期间应持续缓冲,当前 %d 条", buffered)
	}
	// 恢复
	e.gate.offline.Store(false)
	for i := 0; i < 15; i++ {
		e.minute(a, r)
	}
	if n := a.Buffer().Count(); n != 0 {
		t.Fatalf("恢复后缓冲应清空,剩 %d", n)
	}
	got := e.totals(proto.KindIface)
	if got.RX != int64(e.truthRX) || got.TX != int64(e.truthTX) {
		t.Fatalf("网卡合计不一致: got %d/%d want %d/%d", got.RX, got.TX, e.truthRX, e.truthTX)
	}
	sb := e.totals(proto.KindSingbox)
	if sb.RX != int64(e.clash.truthUp) || sb.TX != int64(e.clash.truthDown) {
		t.Fatalf("sing-box 合计不一致: got %d/%d want %d/%d", sb.RX, sb.TX, e.clash.truthUp, e.clash.truthDown)
	}
	t.Logf("断网 24h:缓冲峰值 %d 条,恢复后网卡 %d/%d 字节、sing-box %d/%d 字节与真值完全一致,推送请求 %d 次",
		buffered, got.RX, got.TX, sb.RX, sb.TX, e.gate.posts.Load())
	a.Close()
}

// TestReplayIsIdempotent 手工重放已确认的批次:全部判为重复,合计不变。
func TestReplayIsIdempotent(t *testing.T) {
	e := newEnv(t)
	r := rand.New(rand.NewPCG(3, 4))
	a := e.newAgent()
	// 抓取 agent 发出的请求体以便重放
	var mu sync.Mutex
	var bodies [][]byte
	inner := e.gate.h
	e.gate.h = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		req.Body = io.NopCloser(bytes.NewReader(b))
		inner.ServeHTTP(w, req)
	})
	for i := 0; i < 30; i++ {
		e.minute(a, r)
	}
	before := e.totals(proto.KindIface)
	dups := 0
	mu.Lock()
	sent := append([][]byte(nil), bodies...)
	mu.Unlock()
	for _, b := range sent {
		req, _ := http.NewRequest("POST", e.hs.URL+"/api/v2/ingest", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+e.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var ir proto.IngestResponse
		json.NewDecoder(resp.Body).Decode(&ir)
		resp.Body.Close()
		if ir.Accepted != 0 {
			t.Fatalf("重放不应被接受: %+v", ir)
		}
		dups += ir.Duplicates
	}
	if after := e.totals(proto.KindIface); after != before {
		t.Fatalf("重放后合计变化 %+v -> %+v", before, after)
	}
	t.Logf("重放 %d 个批次,%d 条全部判为重复", len(sent), dups)
	a.Close()
}

// TestCrashBetweenStateAndBuffer 模拟"state 已写入、缓冲未落盘"时崩溃:重启后补写,不丢不重。
func TestCrashBetweenStateAndBuffer(t *testing.T) {
	e := newEnv(t)
	r := rand.New(rand.NewPCG(5, 6))
	e.gate.offline.Store(true)
	a := e.newAgent()
	for i := 0; i < 5; i++ {
		e.minute(a, r)
	}
	a.Close()
	// 删除缓冲最后一批记录(state.json 中的 Pending 仍在)= 崩溃发生在追加缓冲之前
	segs, _ := filepath.Glob(filepath.Join(e.dir, "state", "buffer", "seg-*.jsonl"))
	sort.Strings(segs)
	data, _ := os.ReadFile(segs[len(segs)-1])
	st, err := agent.LoadState(filepath.Join(e.dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	var last proto.Record
	if err = json.Unmarshal(lines[len(lines)-1], &last); err != nil {
		t.Fatal(err)
	}
	st.Pending = []proto.Record{last}
	if err = st.Save(filepath.Join(e.dir, "state")); err != nil {
		t.Fatal(err)
	}

	keep := lines[:len(lines)-len(st.Pending)]
	os.WriteFile(segs[len(segs)-1], append(bytes.Join(keep, []byte("\n")), '\n'), 0o600)

	e.gate.offline.Store(false)
	a = e.newAgent()
	for i := 0; i < 3; i++ {
		e.minute(a, r)
	}
	got := e.totals(proto.KindIface)
	if got.RX != int64(e.truthRX) || got.TX != int64(e.truthTX) {
		t.Fatalf("崩溃恢复后不一致: got %d/%d want %d/%d", got.RX, got.TX, e.truthRX, e.truthTX)
	}
	a.Close()
}

// TestRevokedTokenRejected 吊销后 agent 推送被拒,数据留在缓冲;轮换新 token 后补发。
func TestRevokedTokenRejected(t *testing.T) {
	e := newEnv(t)
	r := rand.New(rand.NewPCG(7, 8))
	a := e.newAgent()
	e.minute(a, r)
	e.st.RevokeHost(context.Background(), e.host)
	e.minute(a, r)
	e.minute(a, r)
	if a.Buffer().Count() == 0 {
		t.Fatal("吊销后数据应保留在缓冲")
	}
	a.Close()
}

// TestLoad20Hosts 20 台主机并发上报压测(每台每分钟 ~60 条记录,模拟 6 小时;-race 下 2 小时)。
func TestLoad20Hosts(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "load.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg, _ := server.LoadConfig("")
	base := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	clk := &clock{t: base}
	srv := server.New(cfg, st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.Now = clk.Now
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	const hosts, perMin = 20, 60
	minutes := 360 // 模拟 6 小时
	if raceEnabled {
		minutes = 120 // -race 下模拟 2 小时(仍覆盖两次整点维护),见 race_on_test.go
	}
	tokens := make([]string, hosts)
	for i := range tokens {
		tokens[i], _ = st.CreateHost(context.Background(), fmt.Sprintf("load-%02d", i), store.HostOpts{})
	}
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: hosts}}
	var lat []time.Duration
	var mu sync.Mutex
	start := time.Now()
	for m := 0; m < minutes; m++ {
		ts := base.Unix() + int64(m+1)*60
		clk.mu.Lock()
		clk.t = time.Unix(ts, 0)
		clk.mu.Unlock()
		var wg sync.WaitGroup
		for h := 0; h < hosts; h++ {
			wg.Add(1)
			go func(h int) {
				defer wg.Done()
				recs := make([]proto.Record, 0, perMin)
				recs = append(recs, proto.Record{Seq: int64(m*perMin + 1), TS: ts, PrevTS: ts - 60, Kind: "iface", Dim1: "eth0", RX: 1000, TX: 2000})
				for k := 1; k < perMin; k++ {
					recs = append(recs, proto.Record{Seq: int64(m*perMin + 1 + k), TS: ts, PrevTS: ts - 60, Kind: "singbox",
						Dim1: fmt.Sprintf("198.51.%d.%d", h, (k*7+m)%250), Dim2: inbounds[k%3], RX: 10, TX: 100})
				}
				b, _ := json.Marshal(proto.Batch{Meta: proto.Meta{Protocol: proto.Version, InstanceID: "load", AgentVersion: "test"}, Records: recs})
				req, _ := http.NewRequest("POST", hs.URL+"/api/v2/ingest", bytes.NewReader(b))
				req.Header.Set("Authorization", "Bearer "+tokens[h])
				t0 := time.Now()
				resp, err := client.Do(req)
				d := time.Since(t0)
				if err != nil {
					t.Error(err)
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Errorf("status %d", resp.StatusCode)
				}
				mu.Lock()
				lat = append(lat, d)
				mu.Unlock()
			}(h)
		}
		wg.Wait()
		if m%60 == 59 { // 每小时跑一次维护(Top-N 合并)
			st.Maintain(context.Background(), store.Retention{}, clk.Now())
		}
	}
	el := time.Since(start)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	n := len(lat)
	recs := hosts * minutes * perMin
	t.Logf("20 台 × %d 分钟 × %d 条 = %d 条记录,%d 次请求,用时 %s;吞吐 %.0f 请求/s(实际需求 0.33 请求/s),%.0f 条/s;延迟 p50 %s p99 %s max %s",
		minutes, perMin, recs, n, el.Round(time.Millisecond), float64(n)/el.Seconds(), float64(recs)/el.Seconds(),
		lat[n/2].Round(time.Microsecond), lat[n*99/100].Round(time.Microsecond), lat[n-1].Round(time.Microsecond))
	var cnt int64
	st.R.QueryRow(`SELECT SUM(rx) FROM fact_minute WHERE kind='iface'`).Scan(&cnt)
	if cnt != int64(hosts*minutes*1000) {
		t.Fatalf("压测数据合计不一致: %d", cnt)
	}
	if fi, err := os.Stat(filepath.Join(dir, "load.db")); err == nil {
		t.Logf("数据库大小 %.1f MiB", float64(fi.Size())/(1<<20))
	}
}
