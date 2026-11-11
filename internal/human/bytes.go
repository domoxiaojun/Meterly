// Package human 容量字符串解析。
package human

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseBytes 解析 "2TiB" / "500GB" / "1.5 T" / "1073741824"。二进制单位(KiB/MiB/GiB/TiB/PiB 及简写 K/M/G/T)按 1024 进制,
// KB/MB/GB/TB/PB 按 1000 进制(服务商后台常用 GB=10^9,录入时请照抄后台单位)。
func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" || s == "0" {
		return 0, nil
	}
	units := []struct {
		suf string
		mul float64
	}{{"PIB", 1 << 50}, {"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10},
		{"PB", 1e15}, {"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3},
		{"P", 1 << 50}, {"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}}
	for _, u := range units {
		if n, ok := strings.CutSuffix(s, u.suf); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
			if err != nil || f < 0 || f > 1e20 {
				return 0, fmt.Errorf("无法解析容量: %s", s)
			}
			return int64(f * u.mul), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("无法解析容量: %s", s)
	}
	return n, nil
}
