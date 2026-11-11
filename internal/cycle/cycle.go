// Package cycle 计费周期与时区换算。所有时间存储为 UTC epoch,
// 周期/日/小时的划分按 Asia/Singapore(可配置)计算。
package cycle

import (
	"fmt"
	"time"
	_ "time/tzdata" // 静态二进制/scratch 容器中也能加载时区
)

const DefaultResetDay = 9

var SGT = mustLoad("Asia/Singapore")

func mustLoad(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func daysIn(y int, m time.Month, loc *time.Location) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, loc).Day()
}

// startIn 返回 y 年 m 月的周期起点(重置日超过当月天数时取当月最后一天)。
func startIn(y int, m time.Month, day int, loc *time.Location) time.Time {
	d := day
	if n := daysIn(y, m, loc); d > n {
		d = n
	}
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// Bounds 返回 t 所在计费周期 [start, end)。
func Bounds(t time.Time, resetDay int, loc *time.Location) (time.Time, time.Time) {
	if resetDay < 1 || resetDay > 31 {
		resetDay = DefaultResetDay
	}
	lt := t.In(loc)
	s := startIn(lt.Year(), lt.Month(), resetDay, loc)
	if lt.Before(s) {
		pm := time.Date(lt.Year(), lt.Month()-1, 1, 0, 0, 0, 0, loc)
		s = startIn(pm.Year(), pm.Month(), resetDay, loc)
	}
	nm := time.Date(s.Year(), s.Month()+1, 1, 0, 0, 0, 0, loc)
	return s, startIn(nm.Year(), nm.Month(), resetDay, loc)
}

// Offset 返回相对 t 所在周期偏移 off 个周期(0=当前,-1=上一个)的 [start,end)。
func Offset(t time.Time, resetDay, off int, loc *time.Location) (time.Time, time.Time) {
	s, e := Bounds(t, resetDay, loc)
	for ; off < 0; off++ {
		s, e = Bounds(s.Add(-time.Second), resetDay, loc)
	}
	for ; off > 0; off-- {
		s, e = Bounds(e, resetDay, loc)
	}
	return s, e
}

// HourBucket 把一条覆盖 (prev, ts] 的增量归入小时桶:以 ts-1 所在小时为准,
// 这样整点 10:00:00 的样本(统计 09:59~10:00)落在 09 点桶。
func HourBucket(ts int64) int64 { return ((ts - 1) / 3600) * 3600 }

// ParseDay 解析 YYYY-MM-DD 为 loc 当天 00:00。
func ParseDay(s string, loc *time.Location) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("日期格式应为 YYYY-MM-DD: %q", s)
	}
	return t, nil
}
