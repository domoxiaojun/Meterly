package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/cycle"
	"github.com/domoxiaojun/Meterly/internal/proto"
)

func TestSeriesFiltersAndIPInbounds(t *testing.T) {
	s := open(t)
	h := host(t, s, "a")
	ctx := context.Background()
	base := int64(1794360000) / 3600 * 3600
	recs := []proto.Record{
		{Seq: 1, TS: base + 30, Kind: "singbox", Dim1: "198.51.100.7", Dim2: "tuic/tuic-in", RX: 10, TX: 100},
		{Seq: 2, TS: base + 90, Kind: "singbox", Dim1: "198.51.100.7", Dim2: "anytls/anytls-in", RX: 1, TX: 2},
		{Seq: 3, TS: base + 3600 + 30, Kind: "singbox", Dim1: "198.51.100.7", Dim2: "tuic/tuic-in", RX: 5, TX: 50},
		{Seq: 4, TS: base + 3600 + 30, Kind: "singbox", Dim1: "203.0.113.9", Dim2: "tuic/tuic-in", RX: 7, TX: 7},
	}
	if _, err := s.Ingest(ctx, h, cycle.SGT, proto.Meta{InstanceID: "i"}, recs, "", time.Unix(base+7200, 0)); err != nil {
		t.Fatal(err)
	}
	ids := []int64{h.ID}
	// 某 IP 小时曲线(跨入站求和)
	pts, _ := s.Series(ctx, "fact_hour", ids, Filter{Kind: "singbox", Dim1: "198.51.100.7"}, base, base+7200)
	if len(pts) != 2 || pts[0].Pair != (Pair{11, 102}) || pts[1].Pair != (Pair{5, 50}) {
		t.Fatalf("IP 曲线 %+v", pts)
	}
	// 入站小时曲线(跨 IP 求和)
	pts, _ = s.Series(ctx, "fact_hour", ids, Filter{Kind: "singbox", Dim2: "tuic/tuic-in"}, base, base+7200)
	if len(pts) != 2 || pts[0].Pair != (Pair{10, 100}) || pts[1].Pair != (Pair{12, 57}) {
		t.Fatalf("入站曲线 %+v", pts)
	}
	// 分钟表
	pts, _ = s.Series(ctx, "fact_minute", ids, Filter{Kind: "singbox", Dim1: "198.51.100.7"}, base, base+120)
	if len(pts) != 2 || pts[0].TS != base || pts[1].TS != base+60 {
		t.Fatalf("分钟曲线 %+v", pts)
	}
	if _, err := s.Series(ctx, "hosts; DROP TABLE x", ids, Filter{Kind: "iface"}, 0, 1); err == nil {
		t.Fatal("非法表名应拒绝")
	}
	ib, _ := s.IPInbounds(ctx, "fact_hour", ids, "198.51.100.7", base, base+7200)
	if len(ib) != 2 || ib[0].Inbound != "tuic/tuic-in" || ib[0].Pair != (Pair{15, 150}) {
		t.Fatalf("IP 入站 %+v", ib)
	}
}

func TestProviderReadings(t *testing.T) {
	s := open(t)
	h := host(t, s, "a")
	ctx := context.Background()
	if err := s.SetProviderReading(ctx, ProviderReading{HostID: h.ID, CycleStart: "2026-11-09", Bytes: 100, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProviderReading(ctx, ProviderReading{HostID: h.ID, CycleStart: "2026-11-09", Bytes: 200, Note: "后台截图", UpdatedAt: 2}); err != nil {
		t.Fatal(err)
	}
	r, err := s.ProviderReadingFor(ctx, h.ID, "2026-11-09")
	if err != nil || r.Bytes != 200 || r.Note != "后台截图" {
		t.Fatalf("覆盖语义 %+v %v", r, err)
	}
	if err := s.SetProviderReading(ctx, ProviderReading{HostID: h.ID, CycleStart: "11/09", Bytes: 1}); err == nil {
		t.Fatal("日期格式应校验")
	}
	if err := s.SetProviderReading(ctx, ProviderReading{HostID: h.ID, CycleStart: "2026-10-09", Bytes: -1}); err == nil {
		t.Fatal("负数应拒绝")
	}
	if l, _ := s.ProviderReadings(ctx, h.ID); len(l) != 1 {
		t.Fatalf("list %+v", l)
	}
	if err := s.DeleteProviderReading(ctx, h.ID, "2026-11-09"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProviderReadingFor(ctx, h.ID, "2026-11-09"); err != ErrNotFound {
		t.Fatal("删除后应不存在")
	}
	if err := s.DeleteProviderReading(ctx, h.ID, "2026-11-09"); err != ErrNotFound {
		t.Fatal("重复删除应 ErrNotFound")
	}
}

func TestCompareVnstat(t *testing.T) {
	s := open(t)
	h := host(t, s, "a")
	ctx := context.Background()
	base := int64(1794355200) // 2026-11-10 00:00 SGT? 仅需整小时
	base = base / 3600 * 3600
	now := time.Unix(base+5*3600+120, 0)
	var recs []proto.Record
	seq := int64(0)
	// agent 数据:第 1~4 小时,每小时 rx=1000 tx=2000(第 4 小时 tx 多 1%)
	for hr := int64(1); hr <= 4; hr++ {
		seq++
		tx := uint64(2000)
		if hr == 4 {
			tx = 2030
		}
		recs = append(recs, proto.Record{Seq: seq, TS: base + hr*3600 + 1800, Kind: "iface", Dim1: "eth0", RX: 1000, TX: tx})
	}
	// 当前未结束的第 5 小时(应被忽略)
	recs = append(recs, proto.Record{Seq: seq + 1, TS: base + 5*3600 + 60, Kind: "iface", Dim1: "eth0", RX: 9, TX: 9})
	if _, err := s.Ingest(ctx, h, cycle.SGT, proto.Meta{InstanceID: "i"}, recs, "", now); err != nil {
		t.Fatal(err)
	}
	vn := map[string]map[int64]Pair{"eth0": {}}
	for hr := int64(0); hr <= 5; hr++ { // 第 0 小时 agent 未上线
		vn["eth0"][base+hr*3600] = Pair{1000, 2000}
	}
	res, err := s.CompareVnstat(ctx, h.ID, cycle.SGT, vn, now)
	if err != nil || len(res) != 1 {
		t.Fatal(err, res)
	}
	c := res[0]
	if c.OnlyVnstat != 1 || c.OnlyOurs != 0 || c.Total.Hours != 4 {
		t.Fatalf("覆盖统计 %+v", c)
	}
	if c.Total.Vnstat.Total() != 12000 || c.Total.Ours.Total() != 12030 || c.Total.Diff != 30 || c.Total.Pct != 0.25 {
		t.Fatalf("合计 %+v", c.Total)
	}
}

func TestBackupOnline(t *testing.T) {
	s := open(t)
	h := host(t, s, "a")
	ctx := context.Background()
	now := time.Unix(1794360000, 0)
	s.Ingest(ctx, h, cycle.SGT, proto.Meta{InstanceID: "i"}, []proto.Record{{Seq: 1, TS: now.Unix(), Kind: "iface", Dim1: "eth0", RX: 5, TX: 6}}, "", now)
	dst := filepath.Join(t.TempDir(), "bk.db")
	if err := s.Backup(ctx, dst); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if p := sumKind(t, b, "fact_hour", h.ID, "iface"); p != (Pair{5, 6}) {
		t.Fatalf("备份内容 %+v", p)
	}
	if err := s.Backup(ctx, dst); err == nil {
		t.Fatal("目标已存在应失败(不覆盖)")
	}
}

func TestMinuteDetailRetention(t *testing.T) {
	s := open(t)
	h := host(t, s, "a")
	ctx := context.Background()
	now := time.Unix(1794360000/3600*3600, 0)
	old := now.Unix() - 30*3600
	recs := []proto.Record{
		{Seq: 1, TS: old, Kind: "singbox", Dim1: "198.51.100.1", Dim2: "tuic/tuic-in", RX: 1, TX: 1},
		{Seq: 2, TS: old, Kind: "iface", Dim1: "eth0", RX: 1, TX: 1},
		{Seq: 3, TS: now.Unix() - 120, Kind: "singbox", Dim1: "198.51.100.1", Dim2: "tuic/tuic-in", RX: 1, TX: 1},
	}
	if _, err := s.Ingest(ctx, h, cycle.SGT, proto.Meta{InstanceID: "i"}, recs, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Maintain(ctx, Retention{}, now); err != nil {
		t.Fatal(err)
	}
	var sb, ifc int
	s.R.QueryRow(`SELECT COUNT(*) FROM fact_minute WHERE kind='singbox'`).Scan(&sb)
	s.R.QueryRow(`SELECT COUNT(*) FROM fact_minute WHERE kind='iface'`).Scan(&ifc)
	if sb != 1 || ifc != 1 {
		t.Fatalf("sing-box 分钟明细应只留 25h 内,网卡保留: sb=%d iface=%d", sb, ifc)
	}
}
