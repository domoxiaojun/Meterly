// demoseed 向本机 server 推送合成演示数据(截图/联调用)。来源 IP 是知名公共 DNS / anycast 地址与文档保留网段,
// 不是任何真实客户;token 从文件读取,不打印。用法:
//
//	demoseed -url http://127.0.0.1:18790 -token-file box.token -hours 72 -scale 1
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

type src struct {
	ip, inbound string
	weight      float64
	phase       float64
}

var sources = []src{
	{"114.114.114.114", "tuic/tuic-in", 9, 0},
	{"223.5.5.5", "anytls/anytls-in", 6, 1},
	{"180.76.76.76", "tuic/tuic-in", 4, 2},
	{"1.1.1.1", "hysteria2/hy2-in", 3, 0.5},
	{"8.8.8.8", "anytls/anytls-in", 2.5, 3},
	{"168.126.63.1", "tuic/tuic-in", 2, 4},
	{"101.101.101.101", "vless/reality-in", 1.6, 1.5},
	{"9.9.9.9", "hysteria2/hy2-in", 1.1, 2.5},
	{"208.67.222.222", "vless/reality-in", 0.8, 5},
	{"2001:4860:4860::8888", "tuic/tuic-in", 0.7, 3.3},
	{"100.64.0.12", "direct/relay-in", 3.5, 0},
	{"203.0.113.24", "anytls/anytls-in", 0.4, 1},
}

func main() {
	url := flag.String("url", "http://127.0.0.1:18790", "server 地址(仅本机)")
	tokFile := flag.String("token-file", "", "host token 文件")
	hours := flag.Int("hours", 72, "回填小时数")
	scale := flag.Float64("scale", 1, "流量倍数")
	seed := flag.Uint64("seed", 1, "随机种子")
	flag.Parse()
	b, err := os.ReadFile(*tokFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 token 文件失败")
		os.Exit(1)
	}
	tok := strings.TrimSpace(string(b))
	rng := rand.New(rand.NewPCG(*seed, 7))
	now := time.Now().Truncate(time.Minute)
	start := now.Add(-time.Duration(*hours) * time.Hour)
	inst := fmt.Sprintf("demo-%d-%d", *seed, now.Unix())
	var recs []proto.Record
	seq := int64(0)
	add := func(r proto.Record) { seq++; r.Seq = seq; recs = append(recs, r) }
	const mib = 1 << 20
	for t := start.Add(time.Minute); !t.After(now); t = t.Add(time.Minute) {
		ts := t.Unix()
		h := float64(t.In(time.FixedZone("SGT", 8*3600)).Hour()) + float64(t.Minute())/60
		diurnal := 0.35 + 0.65*math.Pow(math.Max(0, math.Sin((h-9)/24*2*math.Pi)), 1.6) // 晚上高峰
		var sbRX, sbTX float64
		for _, s := range sources {
			act := diurnal * (0.6 + 0.4*math.Sin(h/3+s.phase)) * (0.7 + 0.6*rng.Float64())
			if act < 0.05 {
				continue
			}
			tx := s.weight * act * 2.2 * mib * *scale
			rx := tx * (0.04 + 0.05*rng.Float64())
			sbRX += rx
			sbTX += tx
			add(proto.Record{TS: ts, Kind: proto.KindSingbox, Dim1: s.ip, Dim2: s.inbound, RX: uint64(rx), TX: uint64(tx)})
		}
		un := (sbRX + sbTX) * 0.03
		add(proto.Record{TS: ts, Kind: proto.KindSingbox, Dim1: proto.DimUnattributed, RX: uint64(un * 0.3), TX: uint64(un * 0.7)})
		// eth0 ≈ sing-box 载荷 ×2 + 协议开销 + 其他(站点/系统)
		other := (6 + 10*diurnal*rng.Float64()) * mib * *scale
		eRX := (sbRX+sbTX)*1.03 + other*0.2
		eTX := (sbRX+sbTX)*1.05 + other
		add(proto.Record{TS: ts, PrevTS: ts - 60, Kind: proto.KindIface, Dim1: "eth0", RX: uint64(eRX), TX: uint64(eTX)})
		add(proto.Record{TS: ts, PrevTS: ts - 60, Kind: proto.KindIface, Dim1: "wt0", RX: uint64(other * 0.5), TX: uint64(other * 0.15)})
		// 站点(仅下行)与 NetBird,演示用
		fun := other * (0.8 + 0.4*rng.Float64())
		add(proto.Record{TS: ts, PrevTS: ts - 60, Kind: proto.KindSite, Dim1: "media.example.com", Dim2: "203.0.113.80", TX: uint64(fun * 0.7)})
		add(proto.Record{TS: ts, PrevTS: ts - 60, Kind: proto.KindSite, Dim1: "git.example.com", Dim2: "203.0.113.81", TX: uint64(fun * 0.2)})
		add(proto.Record{TS: ts, PrevTS: ts - 60, Kind: proto.KindSiteReq, Dim1: "media.example.com", Dim2: "2xx", RX: uint64(20 + 40*diurnal)})
		add(proto.Record{TS: ts, PrevTS: ts - 60, Kind: proto.KindSiteReq, Dim1: "git.example.com", Dim2: "2xx", RX: uint64(5 + 8*diurnal)})
		add(proto.Record{TS: ts, PrevTS: ts - 60, Kind: proto.KindNetbird, Dim1: "peer-a.example.net", Dim2: "100.64.0.10", RX: uint64(other * 0.35), TX: uint64(other * 0.04)})
		add(proto.Record{TS: ts, PrevTS: ts - 60, Kind: proto.KindNetbird, Dim1: "peer-b.example.net", Dim2: "100.64.0.11", RX: uint64(other * 0.25), TX: uint64(other * 0.03)})
		if rng.Float64() < 0.3 {
			add(proto.Record{TS: ts, PrevTS: ts - 60, Kind: proto.KindXray, Dim1: "inbound-20383", RX: uint64(0.05 * mib * *scale), TX: uint64(0.2 * mib * *scale)})
		}
	}
	cl := &http.Client{Timeout: 60 * time.Second}
	for i := 0; i < len(recs); i += 4000 {
		batch := proto.Batch{Meta: proto.Meta{Hostname: "demo", AgentVersion: "demo-m3", InstanceID: inst, Timezone: "Asia/Singapore", SentAt: time.Now().Unix(),
			Collectors: map[string]proto.CollectorHealth{
				"netdev":  {Enabled: true, LastOK: now.Unix()},
				"singbox": {Enabled: true, LastOK: now.Unix(), Note: "demo"},
				"site":    {Enabled: true, LastOK: now.Unix(), Note: "按请求结束时刻归因"},
				"netbird": {Enabled: true, LastOK: now.Unix(), Note: "对等端 2"},
				"xray":    {Enabled: true, LastOK: now.Unix(), Note: "无来源 IP"},
			}},
			Records: recs[i:min(i+4000, len(recs))]}
		body, _ := json.Marshal(batch)
		req, _ := http.NewRequest("POST", *url+"/api/v2/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := cl.Do(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, "推送失败:", err)
			os.Exit(1)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			fmt.Fprintln(os.Stderr, "推送失败: HTTP", resp.StatusCode)
			os.Exit(1)
		}
		time.Sleep(250 * time.Millisecond) // ingest 限流 5 次/s
	}
	fmt.Fprintf(os.Stderr, "已推送 %d 条演示记录(%d 小时)\n", len(recs), *hours)
}
