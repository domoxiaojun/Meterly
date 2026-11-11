package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

// XrayConfig xray stats API 采集(可选,未探测到则关闭)。
// 通过 `xray api statsquery` 取数,不引入 gRPC 依赖;无来源 IP。
type XrayConfig struct {
	Enabled bool     `json:"enabled"`
	Bin     string   `json:"bin,omitempty"`    // 默认 xray
	Server  string   `json:"server,omitempty"` // 默认 127.0.0.1:62789
	Timeout Duration `json:"timeout,omitempty"`
}

func (c *XrayConfig) applyDefaults() {
	if c.Bin == "" {
		c.Bin = "xray"
	}
	if c.Server == "" {
		c.Server = "127.0.0.1:62789"
	}
	if c.Timeout.Duration == 0 {
		c.Timeout.Duration = 5 * time.Second
	}
}

// XrayAcc xray 计数累计(uplink=rx, downlink=tx)。
type XrayAcc struct {
	cfg  XrayConfig
	St   map[string]uint64 // stat name → 累计
	Exec ExecFunc
}

func NewXrayAcc(cfg XrayConfig, st map[string]uint64) *XrayAcc {
	cfg.applyDefaults()
	if st == nil {
		st = map[string]uint64{}
	}
	return &XrayAcc{cfg: cfg, St: st, Exec: defaultExec}
}

func (a *XrayAcc) Collect(ctx context.Context) ([]Counter, error) {
	cctx, cancel := context.WithTimeout(ctx, a.cfg.Timeout.Duration)
	defer cancel()
	raw, err := a.Exec(cctx, a.cfg.Bin, "api", "statsquery", "--server="+a.cfg.Server)
	if err != nil {
		return nil, fmt.Errorf("xray api statsquery: %w", err)
	}
	stats, err := parseXrayStats(raw)
	if err != nil {
		return nil, err
	}
	// name → 当前值;按 inbound/user 聚合成 Counter
	type key struct{ kind, dim1, which string } // which = up|down
	cur := map[string]uint64{}
	type agg struct {
		up, down, hadUp, hadDown bool
		u, d                     uint64
	}
	// 先算增量
	delta := map[string]uint64{}
	for name, v := range stats {
		cur[name] = v
		prev, had := a.St[name]
		if !had {
			continue
		}
		if v >= prev {
			delta[name] = v - prev
		} else {
			delta[name] = v // 重置
		}
	}
	for k, v := range cur {
		a.St[k] = v
	}
	// 丢掉消失的指标
	for k := range a.St {
		if _, ok := cur[k]; !ok {
			delete(a.St, k)
		}
	}
	type pair struct{ rx, tx uint64 }
	inbounds := map[string]*pair{}
	users := map[string]*pair{}
	add := func(m map[string]*pair, k string, up bool, n uint64) {
		p := m[k]
		if p == nil {
			p = &pair{}
			m[k] = p
		}
		if up {
			p.rx += n
		} else {
			p.tx += n
		}
	}
	for name, n := range delta {
		kind, tag, dir := splitXrayStat(name)
		if tag == "" || (dir != "uplink" && dir != "downlink") {
			continue
		}
		up := dir == "uplink"
		switch kind {
		case "inbound":
			add(inbounds, tag, up, n)
		case "user":
			add(users, tag, up, n)
		}
	}
	var out []Counter
	for k, p := range inbounds {
		if p.rx > 0 || p.tx > 0 {
			out = append(out, Counter{Kind: proto.KindXray, Key: k, RX: p.rx, TX: p.tx})
		}
	}
	for k, p := range users {
		if p.rx > 0 || p.tx > 0 {
			out = append(out, Counter{Kind: proto.KindXrayUser, Key: k, RX: p.rx, TX: p.tx})
		}
	}
	return out, nil
}

func splitXrayStat(name string) (kind, tag, dir string) {
	// inbound>>>tag>>>traffic>>>uplink
	// user>>>email>>>traffic>>>downlink
	ps := strings.Split(name, ">>>")
	if len(ps) < 4 {
		return "", "", ""
	}
	return ps[0], ps[1], ps[3]
}

func parseXrayStats(raw []byte) (map[string]uint64, error) {
	raw = bytesTrim(raw)
	var root struct {
		Stat []struct {
			Name  string      `json:"name"`
			Value interface{} `json:"value"`
		} `json:"stat"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("解析 xray statsquery: %w", err)
	}
	out := map[string]uint64{}
	for _, s := range root.Stat {
		if s.Name == "" {
			continue
		}
		var n uint64
		switch v := s.Value.(type) {
		case float64:
			n = uint64(v)
		case string:
			fmt.Sscanf(v, "%d", &n)
		case json.Number:
			i, _ := v.Int64()
			n = uint64(i)
		}
		out[s.Name] = n
	}
	return out, nil
}

func bytesTrim(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\r' || b[j-1] == '\t') {
		j--
	}
	return b[i:j]
}
