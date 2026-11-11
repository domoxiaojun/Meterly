package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

func mk(id, ip, in string, up, down uint64) clashConn {
	return clashConn{ID: id, Metadata: map[string]any{"sourceIP": ip, "type": in}, Upload: up, Download: down}
}

func snapOf(up, down uint64, cs ...clashConn) *clashSnapshot {
	return &clashSnapshot{UploadTotal: up, DownloadTotal: down, Connections: cs}
}

func sumBy(cs []Counter) map[string][2]uint64 {
	m := map[string][2]uint64{}
	for _, c := range cs {
		if c.Kind != proto.KindSingbox {
			continue
		}
		k := c.Key + "|" + c.Key2
		v := m[k]
		v[0] += c.RX
		v[1] += c.TX
		m[k] = v
	}
	return m
}

func total(cs []Counter) (rx, tx uint64) {
	for _, c := range cs {
		if c.Kind == proto.KindSingbox {
			rx += c.RX
			tx += c.TX
		}
	}
	return
}

func TestSingboxBaselineAndIncrements(t *testing.T) {
	s := NewSingboxAcc(SBState{})
	// 首次:只建立基线,进程生命周期累计不计入
	s.Apply(snapOf(1000, 9000, mk("a", "203.0.113.1", "tuic/tuic", 100, 900)))
	if got := s.Drain(); len(got) != 0 {
		t.Fatalf("基线不应产生增量: %v", got)
	}
	// 连接 a 增长,新连接 b 出现(全量计入)
	s.Apply(snapOf(1000+50+10, 9000+500+70,
		mk("a", "203.0.113.1", "tuic/tuic", 150, 1400),
		mk("b", "2001:db8::1", "anytls/anytls-in", 10, 70)))
	got := sumBy(s.Drain())
	if got["203.0.113.1|tuic/tuic"] != [2]uint64{50, 500} || got["2001:db8::1|anytls/anytls-in"] != [2]uint64{10, 70} {
		t.Fatalf("增量错误: %v", got)
	}
	if _, ok := got[proto.DimUnattributed+"|"]; ok {
		t.Fatal("全局增量与逐连接增量一致时不应有未归因")
	}
}

func TestSingboxShortAndClosedConnections(t *testing.T) {
	s := NewSingboxAcc(SBState{})
	s.Apply(snapOf(100, 1000, mk("a", "203.0.113.1", "tuic/tuic", 100, 1000)))
	// 两次轮询之间:a 又传了 20/200 后关闭(消失);一个短连接 c 开合传了 5/50
	s.Apply(snapOf(100+20+5, 1000+200+50))
	cs := s.Drain()
	got := sumBy(cs)
	if got[proto.DimUnattributed+"|"] != [2]uint64{25, 250} {
		t.Fatalf("最后一段 + 短连接应进入未归因桶: %v", got)
	}
	rx, tx := total(cs)
	if rx != 25 || tx != 250 {
		t.Fatalf("合计应等于全局增量: %d/%d", rx, tx)
	}
}

func TestSingboxRestart(t *testing.T) {
	s := NewSingboxAcc(SBState{})
	restarts := 0
	s.Restart = func() { restarts++ }
	s.Apply(snapOf(5000, 50000, mk("a", "203.0.113.1", "tuic/tuic", 5000, 50000)))
	// sing-box 重启:全局计数清零后又涨到 30/300,连接 id 重新从头
	s.Apply(snapOf(30, 300, mk("x", "203.0.113.2", "tuic/tuic", 30, 300)))
	if restarts != 1 || s.St.Restarts != 1 {
		t.Fatalf("应检测到 1 次重启,got %d", restarts)
	}
	got := sumBy(s.Drain())
	if got["203.0.113.2|tuic/tuic"] != [2]uint64{30, 300} || len(got) != 1 {
		t.Fatalf("重启后计数应全部计入且无负数: %v", got)
	}
	// 重启后旧 id 若复用,也按新基线计算
	s.Apply(snapOf(60, 600, mk("x", "203.0.113.2", "tuic/tuic", 60, 600)))
	if got := sumBy(s.Drain()); got["203.0.113.2|tuic/tuic"] != [2]uint64{30, 300} {
		t.Fatalf("got %v", got)
	}
}

func TestSingboxDuplicateIDsAndIPv6(t *testing.T) {
	s := NewSingboxAcc(SBState{})
	s.Apply(snapOf(0, 0))
	s.Apply(snapOf(10, 100,
		mk("d", "::ffff:203.0.113.9", "tuic/tuic", 10, 100),
		mk("d", "::ffff:203.0.113.9", "tuic/tuic", 10, 100), // 重复 id 只算一次
	))
	got := sumBy(s.Drain())
	if got["203.0.113.9|tuic/tuic"] != [2]uint64{10, 100} {
		t.Fatalf("IPv4-mapped 应归一且去重: %v", got)
	}
	for in, want := range map[string]string{"2001:DB8:0:0::1": "2001:db8::1", "[2001:db8::2]": "2001:db8::2", "fe80::1%eth0": "fe80::1", "": "?"} {
		if NormIP(in) != want {
			t.Errorf("NormIP(%q)=%q want %q", in, NormIP(in), want)
		}
	}
}

func TestSingboxUserDimension(t *testing.T) {
	s := NewSingboxAcc(SBState{})
	s.Apply(snapOf(0, 0))
	c := mk("u1", "203.0.113.1", "vmess/vmess", 3, 30)
	c.Metadata["inboundUser"] = "domo"
	s.Apply(snapOf(3, 30, c))
	var user *Counter
	for _, x := range s.Drain() {
		if x.Kind == proto.KindSingboxUser {
			x := x
			user = &x
		}
	}
	if user == nil || user.Key != "domo" || user.Key2 != "vmess/vmess" || user.RX != 3 {
		t.Fatalf("user 维度缺失: %+v", user)
	}
}

// mockClash 本地 clash_api 模拟:校验 Bearer secret,返回可编程快照。
type mockClash struct {
	mu     sync.Mutex
	secret string
	snap   clashSnapshot
	hits   int
	fail   bool
}

func (m *mockClash) set(s *clashSnapshot) { m.mu.Lock(); m.snap = *s; m.mu.Unlock() }

func (m *mockClash) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hits++
	if m.fail {
		http.Error(w, "down", 502)
		return
	}
	if r.URL.Path != "/connections" || r.Header.Get("Authorization") != "Bearer "+m.secret {
		http.Error(w, `{"message":"Unauthorized"}`, 401)
		return
	}
	json.NewEncoder(w).Encode(m.snap)
}

func TestClashClientReadsSecretFromSingboxConfig(t *testing.T) {
	m := &mockClash{secret: "test-secret-not-real"}
	srv := httptest.NewServer(m)
	defer srv.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	os.WriteFile(cfg, []byte(`{"log":{},"experimental":{"clash_api":{"external_controller":"0.0.0.0:23390","secret":"test-secret-not-real"}}}`), 0o600)

	// 只读 config 推导地址:0.0.0.0 -> 127.0.0.1,端口不变(只是访问方式,不改绑定)
	c := NewClashClient(SingboxConfig{ConfigPath: cfg})
	if err := c.loadEndpoint(); err != nil {
		t.Fatal(err)
	}
	if c.url != "http://127.0.0.1:23390/connections" {
		t.Fatalf("url=%s", c.url)
	}
	// URL 覆盖 + secret 来自 config
	c = NewClashClient(SingboxConfig{ConfigPath: cfg, URL: srv.URL})
	m.set(snapOf(1, 2, mk("a", "203.0.113.1", "tuic/tuic", 1, 2)))
	snap, err := c.Fetch(context.Background())
	if err != nil || snap.UploadTotal != 1 || len(snap.Connections) != 1 {
		t.Fatalf("fetch: %v %+v", err, snap)
	}
	// secret 错误时错误信息不包含 secret
	m.mu.Lock()
	m.secret = "rotated"
	m.mu.Unlock()
	_, err = c.Fetch(context.Background())
	if err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("应失败且不泄露 secret: %v", err)
	}
}
