package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/cycle"
	"github.com/domoxiaojun/Meterly/internal/proto"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func host(t *testing.T, s *Store, name string) *Host {
	t.Helper()
	tok, err := s.CreateHost(context.Background(), name, HostOpts{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.HostByToken(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func sumKind(t *testing.T, s *Store, table string, id int64, kind string) Pair {
	t.Helper()
	m, err := s.KindDim1Totals(context.Background(), table, []int64{id}, kind, 0, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	var p Pair
	for _, v := range m {
		p.RX += v.RX
		p.TX += v.TX
	}
	return p
}

func TestTokenHashRevokeRotate(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	tok, err := s.CreateHost(ctx, "host-a", HostOpts{QuotaBytes: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	s.R.QueryRow(`SELECT token_hash FROM hosts`).Scan(&stored)
	if stored == tok || stored != HashToken(tok) {
		t.Fatal("库中应只存 token 哈希")
	}
	if _, err := s.CreateHost(ctx, "host-a", HostOpts{}); err == nil {
		t.Fatal("重复主机应失败")
	}
	if err := s.RevokeHost(ctx, "host-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HostByToken(ctx, tok); err != ErrNotFound {
		t.Fatal("吊销后 token 应失效")
	}
	nt, _ := s.RotateToken(ctx, "host-a")
	if _, err := s.HostByToken(ctx, nt); err != nil {
		t.Fatal("新 token 应可用")
	}
	if _, err := s.HostByToken(ctx, tok); err == nil {
		t.Fatal("旧 token 不应复活")
	}
}

func TestIngestIdempotent(t *testing.T) {
	s := open(t)
	h := host(t, s, "a")
	ctx := context.Background()
	now := time.Unix(1794360000, 0)
	meta := proto.Meta{InstanceID: "i1"}
	batch := []proto.Record{
		{Seq: 1, TS: now.Unix() - 120, PrevTS: now.Unix() - 180, Kind: "iface", Dim1: "eth0", RX: 100, TX: 200},
		{Seq: 2, TS: now.Unix() - 60, PrevTS: now.Unix() - 120, Kind: "iface", Dim1: "eth0", RX: 10, TX: 20},
		{Seq: 3, TS: now.Unix() - 60, Kind: "singbox", Dim1: "203.0.113.1", Dim2: "tuic/tuic", RX: 1, TX: 2},
	}
	r1, err := s.Ingest(ctx, h, cycle.SGT, meta, batch, "127.0.0.1", now)
	if err != nil || r1.Accepted != 3 {
		t.Fatalf("%v %+v", err, r1)
	}
	// 重发(ack 丢失)+ 部分重叠
	r2, _ := s.Ingest(ctx, h, cycle.SGT, meta, append(batch[1:], proto.Record{Seq: 4, TS: now.Unix(), PrevTS: now.Unix() - 60, Kind: "iface", Dim1: "eth0", RX: 1, TX: 1}), "127.0.0.1", now)
	if r2.Accepted != 1 || r2.Duplicates != 2 {
		t.Fatalf("%+v", r2)
	}
	for _, tbl := range []string{"fact_minute", "fact_hour", "fact_day"} {
		if p := sumKind(t, s, tbl, h.ID, "iface"); p != (Pair{111, 221}) {
			t.Fatalf("%s 合计 %+v", tbl, p)
		}
	}
	// 新实例(agent 状态被清空)seq 从 1 重新开始,不应被误判重复
	r3, _ := s.Ingest(ctx, h, cycle.SGT, proto.Meta{InstanceID: "i2"}, batch[:1], "127.0.0.1", now)
	if r3.Accepted != 1 {
		t.Fatalf("新实例被误判重复: %+v", r3)
	}
	// 非法记录被跳过但高水位推进,不卡住重试
	r4, _ := s.Ingest(ctx, h, cycle.SGT, meta, []proto.Record{{Seq: 5, TS: now.Unix() + 86400, Kind: "iface", Dim1: "eth0", RX: 1}}, "", now)
	if r4.Rejected != 1 || r4.MaxSeq != 5 {
		t.Fatalf("%+v", r4)
	}
}

func TestSpreadConserves(t *testing.T) {
	var rx, tx uint64
	n := 0
	Spread(1794000017, 1794000017+3*3600+5, 1_000_000_007, 999, func(ts int64, r, x uint64) { rx += r; tx += x; n++ })
	if rx != 1_000_000_007 || tx != 999 {
		t.Fatalf("摊分不守恒 %d %d", rx, tx)
	}
	if n < 180 {
		t.Fatalf("应摊到 ~181 个分钟桶,得到 %d", n)
	}
	// 超大值不溢出
	rx = 0
	Spread(0, 7*86400, 1<<49, 0, func(ts int64, r, x uint64) { rx += r })
	if rx != 1<<49 {
		t.Fatal("大值摊分溢出")
	}
}

func TestIngestSpreadsAfterAgentDowntime(t *testing.T) {
	s := open(t)
	h := host(t, s, "a")
	now := time.Unix(1794360000/3600*3600, 0)
	// agent 停机 3 小时后续算:一条覆盖 3h 的网卡增量应摊到 3 个小时桶
	_, err := s.Ingest(context.Background(), h, cycle.SGT, proto.Meta{InstanceID: "x"},
		[]proto.Record{{Seq: 1, TS: now.Unix(), PrevTS: now.Unix() - 3*3600, Kind: "iface", Dim1: "eth0", RX: 3600, TX: 0}}, "", now)
	if err != nil {
		t.Fatal(err)
	}
	pts, _ := s.HourSeries(context.Background(), []int64{h.ID}, "iface", "eth0", 0, 1<<40)
	if len(pts) != 3 || pts[0].RX != 1200 || pts[2].RX != 1200 {
		t.Fatalf("%+v", pts)
	}
}

func TestTopNCompactionKeepsTotals(t *testing.T) {
	s := open(t)
	h := host(t, s, "a")
	ctx := context.Background()
	hourTS := int64(1794300000) / 3600 * 3600
	var recs []proto.Record
	var want Pair
	for i := 0; i < 120; i++ {
		r := proto.Record{Seq: int64(i + 1), TS: hourTS + 1800, Kind: "singbox", Dim1: "198.51.100." + itoa(i), Dim2: "tuic/tuic", RX: uint64(i + 1), TX: uint64(10 * (i + 1))}
		recs = append(recs, r)
		want.RX += int64(r.RX)
		want.TX += int64(r.TX)
	}
	recs = append(recs, proto.Record{Seq: 999, TS: hourTS + 1800, Kind: "singbox", Dim1: proto.DimUnattributed, RX: 5, TX: 5})
	want.RX += 5
	want.TX += 5
	if _, err := s.Ingest(ctx, h, cycle.SGT, proto.Meta{InstanceID: "x"}, recs, "", time.Unix(hourTS+3600, 0)); err != nil {
		t.Fatal(err)
	}
	merged, err := s.Maintain(ctx, Retention{TopNHour: 50, TopNDay: 100}, time.Unix(hourTS+3*86400, 0))
	if err != nil {
		t.Fatal(err)
	}
	if merged != 70+20 {
		t.Fatalf("合并行数 %d", merged)
	}
	for _, tbl := range []string{"fact_hour", "fact_day"} {
		if p := sumKind(t, s, tbl, h.ID, "singbox"); p != want {
			t.Fatalf("%s 合并后合计变化 %+v != %+v", tbl, p, want)
		}
	}
	rows, total, _ := s.TopIPs(ctx, "fact_hour", []int64{h.ID}, 0, 1<<40, 10)
	if total != want {
		t.Fatal("排行合计应等于总量")
	}
	var sum Pair
	for _, r := range rows {
		sum.RX += r.RX
		sum.TX += r.TX
	}
	if sum != want || rows[0].IP != "198.51.100.119" {
		t.Fatalf("排行(含其他/未归因)之和应等于总量: %+v first=%s", sum, rows[0].IP)
	}
	// 幂等:再跑一次不再合并
	if n, _ := s.Maintain(ctx, Retention{TopNHour: 50, TopNDay: 100}, time.Unix(hourTS+3*86400, 0)); n != 0 {
		t.Fatalf("重复合并 %d", n)
	}
}

func itoa(i int) string {
	return string(rune('0'+i/100)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10))
}

func TestParseVnstat(t *testing.T) {
	js := `{"interfaces":[{"name":"eth0","traffic":{"hour":[{"timestamp":1794330000,"rx":10,"tx":20}],
	"day":[{"timestamp":1794153600,"rx":240,"tx":48},{"timestamp":1794326400,"rx":1,"tx":1}]}}]}`
	m, err := ParseVnstat(stringsReader(js), []string{"eth0"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	e := m["eth0"]
	if e[1794330000] != (Pair{10, 20}) {
		t.Fatal("小时数据缺失")
	}
	var r int64
	for ts, p := range e {
		if ts >= 1794153600 && ts < 1794153600+86400 {
			r += p.RX
		}
	}
	if r != 240 || len(e) != 25 {
		t.Fatalf("日数据均摊错误 r=%d n=%d", r, len(e))
	}
}
