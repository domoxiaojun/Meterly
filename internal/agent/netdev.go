package agent

import (
	"bufio"
	"fmt"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"io"
	"os"
	"strconv"
	"strings"
)

// Counter 采集器给出的累计计数(由 agent 统一换算成增量)。
type Counter struct {
	Kind string
	Key  string
	Key2 string
	RX   uint64
	TX   uint64
	TS   int64 // 非 0 时作为记录时间(站点日志按请求结束时刻归因);0 表示用 Tick 当前时间
	Site *proto.SiteMetrics
}

// NetDev 读取 /proc/net/dev 中指定网卡的 rx/tx 累计字节。
type NetDev struct {
	Path   string
	Ifaces []string
}

func (n *NetDev) Name() string { return "netdev" }

func (n *NetDev) Collect() ([]Counter, error) {
	f, err := os.Open(n.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	all, err := ParseNetDev(f)
	if err != nil {
		return nil, err
	}
	out := make([]Counter, 0, len(n.Ifaces))
	var missing []string
	for _, name := range n.Ifaces {
		c, ok := all[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		out = append(out, Counter{Kind: "iface", Key: name, RX: c[0], TX: c[1]})
	}
	if len(missing) > 0 {
		return out, fmt.Errorf("网卡不存在: %s", strings.Join(missing, ","))
	}
	return out, nil
}

// ParseNetDev 解析 /proc/net/dev,返回 网卡 -> [rx_bytes, tx_bytes]。
func ParseNetDev(r io.Reader) (map[string][2]uint64, error) {
	sc := bufio.NewScanner(r)
	res := map[string][2]uint64{}
	for sc.Scan() {
		line := sc.Text()
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		f := strings.Fields(line[i+1:])
		if len(f) < 16 {
			continue
		}
		rx, err1 := strconv.ParseUint(f[0], 10, 64)
		tx, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("解析 %s 失败", name)
		}
		res[name] = [2]uint64{rx, tx}
	}
	return res, sc.Err()
}

// DeltaReason 说明增量是如何得出的,便于测试与日志。
type DeltaReason string

const (
	DeltaNormal DeltaReason = "normal"
	DeltaWrap   DeltaReason = "wrap"   // 计数器回绕
	DeltaReset  DeltaReason = "reset"  // 计数器被重置(网卡重建等)
	DeltaReboot DeltaReason = "reboot" // boot_id 变化
	DeltaClamp  DeltaReason = "clamp"  // 超过速率上限,判定为异常
)

// CounterDelta 计算单个累计计数器的增量。
//   - 同一次开机且 cur>=prev:正常相减;
//   - cur<prev:32 位计数器视为回绕;64 位仅在接近上限时视为回绕,否则视为重置(取 cur);
//   - 结果超过 maxDelta(速率上限×间隔)判定异常,视为重置并再次校验。
func CounterDelta(prev, cur uint64, bits int, maxDelta uint64) (uint64, DeltaReason) {
	var d uint64
	reason := DeltaNormal
	if cur >= prev {
		d = cur - prev
	} else {
		var limit uint64 = ^uint64(0)
		if bits == 32 {
			limit = 1<<32 - 1
		}
		if bits == 32 && prev <= limit {
			d = limit - prev + cur + 1
			reason = DeltaWrap
		} else if bits == 64 && prev > limit-(1<<40) {
			d = limit - prev + cur + 1
			reason = DeltaWrap
		} else {
			d = cur
			reason = DeltaReset
		}
	}
	if maxDelta > 0 && d > maxDelta {
		if cur <= maxDelta {
			return cur, DeltaClamp
		}
		return 0, DeltaClamp
	}
	return d, reason
}
