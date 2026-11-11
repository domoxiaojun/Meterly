package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

// NetbirdConfig NetBird 对等端流量采集。
type NetbirdConfig struct {
	Enabled     bool     `json:"enabled"`
	Command     string   `json:"command,omitempty"`      // 默认 netbird
	WGInterface string   `json:"wg_interface,omitempty"` // 回退: wg show <iface> transfer,默认 wt0
	Timeout     Duration `json:"timeout,omitempty"`
}

func (c *NetbirdConfig) applyDefaults() {
	if c.Command == "" {
		c.Command = "netbird"
	}
	if c.WGInterface == "" {
		c.WGInterface = "wt0"
	}
	if c.Timeout.Duration == 0 {
		c.Timeout.Duration = 8 * time.Second
	}
}

// PeerState 一个对等端的累计计数基线。
type PeerState struct {
	RX uint64 `json:"rx"`
	TX uint64 `json:"tx"`
	IP string `json:"ip,omitempty"`
}

type netbirdPeerSnap struct {
	FQDN, IP string
	RX, TX   uint64
}

// ExecFunc 可注入的外部命令(测试用)。stderr 丢弃,超时由 ctx 控制。
type ExecFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

func defaultExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = nil
	return cmd.Output()
}

// NetbirdAcc NetBird 累计器:快照计数,重启清零时按新值计入。
type NetbirdAcc struct {
	cfg  NetbirdConfig
	St   map[string]PeerState // fqdn → 基线
	Exec ExecFunc
	Self string
}

func NewNetbirdAcc(cfg NetbirdConfig, st map[string]PeerState) *NetbirdAcc {
	cfg.applyDefaults()
	if st == nil {
		st = map[string]PeerState{}
	}
	return &NetbirdAcc{cfg: cfg, St: st, Exec: defaultExec}
}

func (a *NetbirdAcc) Collect(ctx context.Context) ([]Counter, error) {
	snaps, self, err := a.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	a.Self = self
	seen := map[string]bool{}
	var out []Counter
	for _, p := range snaps {
		if p.FQDN == "" {
			continue
		}
		seen[p.FQDN] = true
		prev, had := a.St[p.FQDN]
		a.St[p.FQDN] = PeerState{RX: p.RX, TX: p.TX, IP: p.IP}
		if !had {
			continue // 首次只建基线
		}
		var rx, tx uint64
		if p.RX >= prev.RX && p.TX >= prev.TX {
			rx, tx = p.RX-prev.RX, p.TX-prev.TX
		} else {
			// 进程重启,计数清零后重新累计
			rx, tx = p.RX, p.TX
		}
		if rx > 0 || tx > 0 {
			out = append(out, Counter{Kind: proto.KindNetbird, Key: p.FQDN, Key2: p.IP, RX: rx, TX: tx})
		}
	}
	for k := range a.St {
		if !seen[k] {
			delete(a.St, k)
		}
	}
	return out, nil
}

func (a *NetbirdAcc) snapshot(ctx context.Context) ([]netbirdPeerSnap, string, error) {
	cctx, cancel := context.WithTimeout(ctx, a.cfg.Timeout.Duration)
	defer cancel()
	raw, err := a.Exec(cctx, a.cfg.Command, "status", "--json")
	if err == nil {
		return parseNetbirdJSON(raw)
	}
	// 回退 wg show
	wraw, werr := a.Exec(cctx, "wg", "show", a.cfg.WGInterface, "transfer")
	if werr != nil {
		return nil, "", fmt.Errorf("netbird: %w", err)
	}
	snaps, _ := parseWGTransfer(wraw)
	return snaps, "", nil
}

func parseNetbirdJSON(raw []byte) ([]netbirdPeerSnap, string, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, "", fmt.Errorf("解析 netbird status --json: %w", err)
	}
	self, _ := root["fqdn"].(string)
	var details []any
	switch p := root["peers"].(type) {
	case map[string]any:
		if d, ok := p["details"].([]any); ok {
			details = d
		}
	case []any:
		details = p
	}
	var out []netbirdPeerSnap
	for _, it := range details {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		fqdn, _ := m["fqdn"].(string)
		if fqdn == "" {
			fqdn, _ = m["hostname"].(string)
		}
		if fqdn == "" {
			continue
		}
		ip := jsonString(m, "netbirdIp", "ip", "netbird_ip")
		if i := strings.IndexByte(ip, '/'); i > 0 {
			ip = ip[:i]
		}
		rx := jsonUint(m, "transferReceived", "transfer_received", "rx")
		tx := jsonUint(m, "transferSent", "transfer_sent", "tx")
		out = append(out, netbirdPeerSnap{FQDN: fqdn, IP: ip, RX: rx, TX: tx})
	}
	return out, self, nil
}

func jsonString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		}
	}
	return ""
}

func jsonUint(m map[string]any, keys ...string) uint64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			if v > 0 {
				return uint64(v)
			}
			return uint64(v)
		case json.Number:
			n, _ := v.Int64()
			return uint64(n)
		case string:
			var n uint64
			fmt.Sscanf(v, "%d", &n)
			return n
		case int:
			return uint64(v)
		case int64:
			return uint64(v)
		}
	}
	return 0
}

func parseWGTransfer(raw []byte) ([]netbirdPeerSnap, error) {
	// 每行: <public-key> <rx> <tx>  —— 不把公钥记入 Key(用短指纹),避免把密钥材料上报。
	var out []netbirdPeerSnap
	for _, line := range bytes.Split(raw, []byte("\n")) {
		fs := strings.Fields(string(line))
		if len(fs) < 3 {
			continue
		}
		var rx, tx uint64
		fmt.Sscanf(fs[1], "%d", &rx)
		fmt.Sscanf(fs[2], "%d", &tx)
		key := fs[0]
		if len(key) > 12 {
			key = "wg:" + key[:8]
		} else {
			key = "wg:" + key
		}
		out = append(out, netbirdPeerSnap{FQDN: key, RX: rx, TX: tx})
	}
	return out, nil
}
