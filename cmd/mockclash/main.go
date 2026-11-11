// mockclash:本地模拟 sing-box clash_api /connections(仅用于测试/演示,只监听 127.0.0.1)。
// 来源 IP 使用文档保留网段(203.0.113.0/24、198.51.100.0/24、2001:db8::/32),不含任何真实数据。
// 发送 SIGHUP 模拟 sing-box 重启(计数清零)。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type conn struct {
	ID       string            `json:"id"`
	Metadata map[string]string `json:"metadata"`
	Upload   uint64            `json:"upload"`
	Download uint64            `json:"download"`
	Start    string            `json:"start"`
	Chains   []string          `json:"chains"`
	rateU    uint64
	rateD    uint64
	until    time.Time
}

type sim struct {
	mu       sync.Mutex
	conns    []*conn
	up, down uint64
	last     time.Time
	n        int
	ips      []string
	inbounds []string
	scale    float64
}

func newSim(scale float64) *sim {
	s := &sim{last: time.Now(), scale: scale,
		inbounds: []string{"tuic/tuic", "anytls/anytls-in", "anytls/any-2-jiakuan-us", "shadowsocks/shadowsocks-in", "direct/pai-anytls", "direct/vx-shom-anytls-ccus", "vless/vless"}}
	for i := 0; i < 14; i++ {
		s.ips = append(s.ips, fmt.Sprintf("203.0.113.%d", 10+i*13))
	}
	for i := 0; i < 6; i++ {
		s.ips = append(s.ips, fmt.Sprintf("198.51.100.%d", 20+i*31))
	}
	s.ips = append(s.ips, "2001:db8:85a3::8a2e:370:7334", "::ffff:203.0.113.200", "2001:db8:1::42")
	return s
}

func (s *sim) spawn(now time.Time) {
	// 少数 IP 很重(幂律),入站与 IP 绑定偏好
	i := int(float64(len(s.ips)) * rand.Float64() * rand.Float64())
	ip := s.ips[i]
	in := s.inbounds[(i+rand.IntN(3))%len(s.inbounds)]
	s.n++
	life := time.Duration(1+rand.ExpFloat64()*40) * time.Second // 大量短连接
	if rand.IntN(10) == 0 {
		life = time.Duration(5+rand.IntN(30)) * time.Minute
	}
	w := 1.0 / float64(1+i)
	c := &conn{ID: fmt.Sprintf("c%08d-%04x", s.n, rand.IntN(65536)), Start: now.UTC().Format(time.RFC3339Nano),
		Metadata: map[string]string{"network": "tcp", "type": in, "sourceIP": ip, "sourcePort": fmt.Sprint(20000 + rand.IntN(40000)),
			"destinationPort": "443", "host": "example.com"},
		Chains: []string{"direct"}, until: now.Add(life),
		rateU: uint64(s.scale * w * (20e3 + rand.Float64()*80e3)), rateD: uint64(s.scale * w * (200e3 + rand.Float64()*1.5e6))}
	if strings.HasPrefix(in, "udp") || rand.IntN(4) == 0 {
		c.Metadata["network"] = "udp"
	}
	s.conns = append(s.conns, c)
}

func (s *sim) step(now time.Time) {
	dt := now.Sub(s.last).Seconds()
	if dt <= 0 {
		return
	}
	s.last = now
	for k := 0; k < int(dt*3)+rand.IntN(2); k++ {
		s.spawn(now)
	}
	alive := s.conns[:0]
	for _, c := range s.conns {
		u, d := uint64(float64(c.rateU)*dt), uint64(float64(c.rateD)*dt)
		c.Upload += u
		c.Download += d
		s.up += u
		s.down += d
		if now.Before(c.until) {
			alive = append(alive, c)
		}
	}
	s.conns = alive
}

func main() {
	listen := flag.String("listen", "127.0.0.1:19090", "监听地址(只允许回环)")
	secretFile := flag.String("secret-file", "", "secret 文件(可选,不打印)")
	scale := flag.Float64("scale", 1, "流量倍率")
	flag.Parse()
	host, _, _ := net.SplitHostPort(*listen)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		log.Fatal("mockclash 只允许监听回环地址")
	}
	secret := ""
	if *secretFile != "" {
		b, err := os.ReadFile(*secretFile)
		if err != nil {
			log.Fatal("读取 secret 失败")
		}
		secret = strings.TrimSpace(string(b))
	}
	s := newSim(*scale)
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			s.mu.Lock()
			s.conns, s.up, s.down = nil, 0, 0
			s.mu.Unlock()
			log.Print("模拟 sing-box 重启:计数清零")
		}
	}()
	http.HandleFunc("GET /connections", func(w http.ResponseWriter, r *http.Request) {
		if secret != "" && r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		s.step(time.Now())
		out := map[string]any{"uploadTotal": s.up, "downloadTotal": s.down, "connections": s.conns, "memory": 12345678}
		b, _ := json.Marshal(out)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	log.Printf("mockclash 监听 %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, nil))
}
