package cycle

import (
	"testing"
	"time"
)

func sgt(y int, m time.Month, d, h, mi, s int) time.Time { return time.Date(y, m, d, h, mi, s, 0, SGT) }

func TestBoundsResetDay9(t *testing.T) {
	cases := []struct {
		at         time.Time
		start, end time.Time
	}{
		{sgt(2026, 11, 9, 0, 0, 0), sgt(2026, 11, 9, 0, 0, 0), sgt(2026, 12, 9, 0, 0, 0)},
		{sgt(2026, 11, 8, 23, 59, 59), sgt(2026, 10, 9, 0, 0, 0), sgt(2026, 11, 9, 0, 0, 0)},
		{sgt(2026, 11, 11, 12, 0, 0), sgt(2026, 11, 9, 0, 0, 0), sgt(2026, 12, 9, 0, 0, 0)},
		{sgt(2027, 1, 5, 0, 0, 0), sgt(2026, 12, 9, 0, 0, 0), sgt(2027, 1, 9, 0, 0, 0)}, // 跨年
	}
	for _, c := range cases {
		s, e := Bounds(c.at, 9, SGT)
		if !s.Equal(c.start) || !e.Equal(c.end) {
			t.Errorf("Bounds(%v) = %v~%v, want %v~%v", c.at, s, e, c.start, c.end)
		}
	}
}

func TestBoundsUTCInstantVsSGT(t *testing.T) {
	// 2026-11-08 16:00:00 UTC == 2026-11-09 00:00 SGT:新周期第一秒
	at := time.Date(2026, 11, 8, 16, 0, 0, 0, time.UTC)
	s, _ := Bounds(at, 9, SGT)
	if s.Unix() != at.Unix() {
		t.Fatalf("周期起点 %v != %v", s, at)
	}
	s2, _ := Bounds(at.Add(-time.Second), 9, SGT)
	if s2.Unix() == at.Unix() {
		t.Fatal("前一秒应属上一周期")
	}
	// 同一时刻在 UTC 主机上仍属上一周期(UTC 11-08 16:00 < 11-09 00:00 UTC)
	s3, _ := Bounds(at, 9, time.UTC)
	if s3.Day() != 9 || s3.Month() != 10 {
		t.Fatalf("UTC 主机周期起点应为 10-09,得到 %v", s3)
	}
}

func TestResetDayBeyondMonthEnd(t *testing.T) {
	s, e := Bounds(sgt(2027, 2, 28, 12, 0, 0), 31, SGT)
	if !s.Equal(sgt(2027, 2, 28, 0, 0, 0)) || !e.Equal(sgt(2027, 3, 31, 0, 0, 0)) {
		t.Fatalf("got %v ~ %v", s, e)
	}
	s, _ = Bounds(sgt(2027, 2, 27, 12, 0, 0), 31, SGT)
	if !s.Equal(sgt(2027, 1, 31, 0, 0, 0)) {
		t.Fatalf("got %v", s)
	}
}

func TestOffset(t *testing.T) {
	now := sgt(2026, 11, 11, 9, 0, 0)
	s, e := Offset(now, 9, -1, SGT)
	if !s.Equal(sgt(2026, 10, 9, 0, 0, 0)) || !e.Equal(sgt(2026, 11, 9, 0, 0, 0)) {
		t.Fatalf("上期 %v ~ %v", s, e)
	}
	s, _ = Offset(now, 9, -12, SGT)
	if !s.Equal(sgt(2025, 11, 9, 0, 0, 0)) {
		t.Fatalf("12 期前 %v", s)
	}
	s, _ = Offset(now, 9, 1, SGT)
	if !s.Equal(sgt(2026, 12, 9, 0, 0, 0)) {
		t.Fatalf("下期 %v", s)
	}
}

func TestHourBucket(t *testing.T) {
	ts := sgt(2026, 11, 11, 10, 0, 0).Unix()
	if HourBucket(ts) != ts-3600 {
		t.Fatal("整点样本应归入前一小时")
	}
	if HourBucket(ts+1) != ts {
		t.Fatal("10:00:01 应归入 10 点")
	}
}
