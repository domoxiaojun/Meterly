package proto

import (
	"errors"
	"net/netip"
	"strings"
)

func (p Policy) Validate() error {
	known := map[string]bool{"netdev": true, "singbox": true, "site": true, "netbird": true, "xray": true}
	for k := range p.Collectors {
		if !known[k] {
			return errors.New("未知采集器")
		}
	}
	if p.Interfaces != nil {
		if len(*p.Interfaces) > 64 {
			return errors.New("网卡数量过多")
		}
		for _, n := range *p.Interfaces {
			if n == "" || len(n) > 64 || strings.ContainsAny(n, "/\x00\r\n ") {
				return errors.New("网卡名无效")
			}
		}
	}
	if len(p.DisabledSources) > 1000 || len(p.TrustedProxies) > 256 {
		return errors.New("配置条目过多")
	}
	for _, c := range p.TrustedProxies {
		if c == "cloudflare" {
			continue
		}
		if _, e := netip.ParsePrefix(c); e != nil {
			if _, e := netip.ParseAddr(c); e != nil {
				return errors.New("可信代理需为 IP/CIDR 或 cloudflare")
			}
		}
	}
	switch p.IPHeader {
	case "", "remote_addr", "CF-Connecting-IP", "X-Forwarded-For", "X-Real-IP":
	default:
		return errors.New("不支持的客户端 IP 头")
	}
	return nil
}
