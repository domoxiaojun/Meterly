package agent

import (
	"encoding/json"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

func NormalizeSite(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if h, _, e := net.SplitHostPort(s); e == nil {
		s = h
	}
	s = strings.TrimSuffix(s, ".")
	if len(s) > 253 || strings.ContainsAny(s, "/\\ \t\r\n") {
		return ""
	}
	return s
}
func trustedIP(s string, cidrs []string) bool {
	a, e := netip.ParseAddr(s)
	if e != nil {
		return false
	}
	a = a.Unmap()
	for _, c := range cidrs {
		if p, e := netip.ParsePrefix(c); e == nil && p.Contains(a) {
			return true
		}
		if p, e := netip.ParseAddr(c); e == nil && p.Unmap() == a {
			return true
		}
	}
	return false
}
func validClient(s string) string {
	s = strings.TrimSpace(s)
	a, e := netip.ParseAddr(s)
	if e != nil {
		return ""
	}
	return a.Unmap().WithZone("").String()
}

// Only a confirmed immediate proxy may supply a replacement IP. Never trust the first XFF blindly.
func ResolveClientIP(l *LogLine, src SiteSource, cfg SiteConfig) (string, string) {
	remote := validClient(l.IP)
	if remote == "" {
		return "~unknown", "unresolved"
	}
	if src.RealIPApplied {
		return remote, "nginx_realip"
	}
	cidrs := cfg.TrustedProxies
	if len(src.TrustedProxies) > 0 {
		cidrs = src.TrustedProxies
	}
	header := cfg.IPHeader
	if src.IPHeader != "" {
		header = src.IPHeader
	}
	if header == "" || header == "remote_addr" || !trustedIP(remote, expandProxies(cidrs)) {
		return remote, "remote_addr"
	}
	key := "http_" + strings.ReplaceAll(strings.ToLower(header), "-", "_")
	raw := l.Headers[key]
	if strings.EqualFold(header, "X-Forwarded-For") {
		parts := strings.Split(raw, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := validClient(parts[i])
			if ip == "" {
				return remote, "unresolved"
			}
			if !trustedIP(ip, expandProxies(cidrs)) {
				return ip, "trusted_xff"
			}
		}
		return remote, "unresolved"
	}
	if strings.EqualFold(header, "CF-Connecting-IP") {
		if v := validClient(l.Headers["http_cf_connecting_ipv6"]); v != "" {
			return v, "cloudflare"
		}
	}
	if ip := validClient(raw); ip != "" {
		return ip, "trusted_header"
	}
	return remote, "unresolved"
}
func parseJSONLog(line string) *LogLine {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &m) != nil {
		return nil
	}
	val := func(keys ...string) string {
		for _, k := range keys {
			v, ok := m[k]
			if !ok {
				continue
			}
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s
			}
			return string(v)
		}
		return ""
	}
	ts := val("time_iso8601", "time", "timestamp", "time_local")
	var t time.Time
	var e error
	for _, layout := range []string{time.RFC3339Nano, "02/Jan/2006:15:04:05 -0700"} {
		t, e = time.Parse(layout, ts)
		if e == nil {
			break
		}
	}
	if e != nil || t.IsZero() {
		return nil
	}
	status, e := strconv.Atoi(val("status"))
	if e != nil {
		return nil
	}
	l := &LogLine{Time: t, Status: status, IP: val("remote_addr"), Host: val("host", "server_name"), Headers: map[string]string{}}
	l.HasHost = l.Host != ""
	l.Sent = parseUint(val("body_bytes_sent", "bytes_sent"))
	recv, recvErr := strconv.ParseUint(val("request_length"), 10, 64)
	l.Recv = recv
	l.HasRecv = recvErr == nil
	for _, h := range []string{"cf_connecting_ip", "cf_connecting_ipv6", "x_forwarded_for", "x_real_ip"} {
		l.Headers["http_"+h] = val("http_"+h, h)
	}
	return l
}
