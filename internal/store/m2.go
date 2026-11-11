package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// Filter 序列筛选:Kind 必填;Dim1 / Dim2 为空表示不限。
type Filter struct {
	Kind, Dim1, Dim2 string
}

var seriesTables = map[string]bool{"fact_minute": true, "fact_hour": true, "fact_day": true}

// Series 返回 table 中 [from,to) 的时间序列(按 ts 汇总 ids)。table 为 fact_minute / fact_hour / fact_day。
func (s *Store) Series(ctx context.Context, table string, ids []int64, f Filter, from, to int64) ([]Point, error) {
	if !seriesTables[table] {
		return nil, fmt.Errorf("未知表 %s", table)
	}
	in, args := inList(ids)
	q := fmt.Sprintf(`SELECT ts, SUM(rx), SUM(tx) FROM %s WHERE kind=? AND host_id IN %s AND ts>=? AND ts<?`, table, in)
	a := append(append([]any{f.Kind}, args...), from, to)
	if f.Dim1 != "" {
		q += ` AND dim1=?`
		a = append(a, f.Dim1)
	}
	if f.Dim2 != "" {
		q += ` AND dim2=?`
		a = append(a, f.Dim2)
	}
	rows, err := s.R.QueryContext(ctx, q+` GROUP BY ts ORDER BY ts`, a...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.TS, &p.RX, &p.TX); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// IPInbounds 某来源 IP 在各入站上的流量(table 内 [from,to))。
func (s *Store) IPInbounds(ctx context.Context, table string, ids []int64, ip string, from, to int64) ([]InboundShare, error) {
	if !seriesTables[table] {
		return nil, fmt.Errorf("未知表 %s", table)
	}
	in, args := inList(ids)
	rows, err := s.R.QueryContext(ctx, fmt.Sprintf(`SELECT dim2, SUM(rx), SUM(tx) FROM %s WHERE kind='singbox' AND host_id IN %s AND dim1=? AND ts>=? AND ts<? GROUP BY dim2`, table, in),
		append(args, ip, from, to)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InboundShare{}
	for rows.Next() {
		var r InboundShare
		if err := rows.Scan(&r.Inbound, &r.RX, &r.TX); err != nil {
			return nil, err
		}
		r.Relay = IsRelay(r.Inbound)
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total() > out[j].Total() })
	return out, rows.Err()
}

// ---- 服务商数字手工录入 ----

// ProviderReading 服务商后台显示的某周期用量。
type ProviderReading struct {
	HostID     int64  `json:"-"`
	CycleStart string `json:"cycle_start"`
	Bytes      int64  `json:"bytes"`
	Note       string `json:"note"`
	UpdatedAt  int64  `json:"updated_at"`
}

// SetProviderReading 录入/覆盖。
func (s *Store) SetProviderReading(ctx context.Context, r ProviderReading) error {
	if _, err := time.Parse("2006-01-02", r.CycleStart); err != nil {
		return errors.New("cycle_start 必须是 YYYY-MM-DD")
	}
	if r.Bytes < 0 {
		return errors.New("用量不能为负")
	}
	if len([]rune(r.Note)) > 200 {
		return errors.New("备注过长(≤ 200 字)")
	}
	_, err := s.W.ExecContext(ctx, `INSERT INTO provider_readings(host_id,cycle_start,bytes,note,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(host_id,cycle_start) DO UPDATE SET bytes=excluded.bytes, note=excluded.note, updated_at=excluded.updated_at`,
		r.HostID, r.CycleStart, r.Bytes, r.Note, r.UpdatedAt)
	return err
}

// DeleteProviderReading 删除一条录入。
func (s *Store) DeleteProviderReading(ctx context.Context, hostID int64, cycleStart string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM provider_readings WHERE host_id=? AND cycle_start=?`, hostID, cycleStart)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ProviderReadingFor 某周期的录入;没有时返回 ErrNotFound。
func (s *Store) ProviderReadingFor(ctx context.Context, hostID int64, cycleStart string) (*ProviderReading, error) {
	r := &ProviderReading{HostID: hostID}
	err := s.R.QueryRowContext(ctx, `SELECT cycle_start,bytes,note,updated_at FROM provider_readings WHERE host_id=? AND cycle_start=?`, hostID, cycleStart).
		Scan(&r.CycleStart, &r.Bytes, &r.Note, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ProviderReadings 某 host 全部录入(新到旧)。
func (s *Store) ProviderReadings(ctx context.Context, hostID int64) ([]ProviderReading, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT cycle_start,bytes,note,updated_at FROM provider_readings WHERE host_id=? ORDER BY cycle_start DESC`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderReading{}
	for rows.Next() {
		r := ProviderReading{HostID: hostID}
		if err := rows.Scan(&r.CycleStart, &r.Bytes, &r.Note, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- vnstat 一致性自检 ----

// VnstatDay 一天的比对(仅统计双方都有数据的完整小时)。
type VnstatDay struct {
	Day    string  `json:"day"` // 主机时区日期
	Hours  int     `json:"hours"`
	Vnstat Pair    `json:"vnstat"`
	Ours   Pair    `json:"ours"`
	Diff   int64   `json:"diff"` // ours - vnstat(rx+tx)
	Pct    float64 `json:"pct"`  // diff / vnstat × 100
}

// VnstatCheck 一张网卡的比对结果。
type VnstatCheck struct {
	Iface      string      `json:"iface"`
	Days       []VnstatDay `json:"days"`
	Total      VnstatDay   `json:"total"`
	OnlyVnstat int         `json:"only_vnstat_hours"` // vnstat 有、我们无数据的小时(agent 未上线 / 断档)
	OnlyOurs   int         `json:"only_ours_hours"`   // 我们有、vnstat 无的小时
	MaxAbsPct  float64     `json:"max_abs_pct"`       // 单日最大偏差
}

func pctOf(diff, base int64) float64 {
	if base == 0 {
		return 0
	}
	return math.Round(float64(diff)*10000/float64(base)) / 100
}

// CompareVnstat 把 vnstat 小时桶与 fact_hour 的网卡数据比对。只比较已结束的小时(ts+3600 ≤ now),
// 跳过任何一方为 0 的小时(分别计入 only_*)。用于发现采集 bug:两者同源 /proc/net/dev,正常应几乎一致。
func (s *Store) CompareVnstat(ctx context.Context, hostID int64, loc *time.Location, data map[string]map[int64]Pair, now time.Time) ([]VnstatCheck, error) {
	var out []VnstatCheck
	ifaces := make([]string, 0, len(data))
	for k := range data {
		ifaces = append(ifaces, k)
	}
	sort.Strings(ifaces)
	for _, iface := range ifaces {
		hours := data[iface]
		if len(hours) == 0 {
			continue
		}
		var lo, hi int64 = math.MaxInt64, 0
		for ts := range hours {
			lo, hi = min(lo, ts), max(hi, ts)
		}
		pts, err := s.Series(ctx, "fact_hour", []int64{hostID}, Filter{Kind: "iface", Dim1: iface}, lo, hi+3600)
		if err != nil {
			return nil, err
		}
		ours := map[int64]Pair{}
		for _, p := range pts {
			ours[p.TS] = p.Pair
		}
		c := VnstatCheck{Iface: iface, Days: []VnstatDay{}}
		days := map[string]*VnstatDay{}
		for ts, v := range hours {
			if ts+3600 > now.Unix() {
				continue
			}
			o := ours[ts]
			vz, oz := v.Total() == 0, o.Total() == 0
			switch {
			case vz && oz:
				continue
			case oz:
				c.OnlyVnstat++
				continue
			case vz:
				c.OnlyOurs++
				continue
			}
			k := time.Unix(ts, 0).In(loc).Format("2006-01-02")
			d := days[k]
			if d == nil {
				d = &VnstatDay{Day: k}
				days[k] = d
			}
			d.Hours++
			d.Vnstat.RX += v.RX
			d.Vnstat.TX += v.TX
			d.Ours.RX += o.RX
			d.Ours.TX += o.TX
		}
		for ts, o := range ours {
			if _, ok := hours[ts]; !ok && o.Total() > 0 && ts+3600 <= now.Unix() {
				c.OnlyOurs++
			}
		}
		c.Total.Day = "合计"
		for _, d := range days {
			d.Diff = d.Ours.Total() - d.Vnstat.Total()
			d.Pct = pctOf(d.Diff, d.Vnstat.Total())
			c.MaxAbsPct = max(c.MaxAbsPct, math.Abs(d.Pct))
			c.Days = append(c.Days, *d)
			c.Total.Hours += d.Hours
			c.Total.Vnstat.RX += d.Vnstat.RX
			c.Total.Vnstat.TX += d.Vnstat.TX
			c.Total.Ours.RX += d.Ours.RX
			c.Total.Ours.TX += d.Ours.TX
		}
		c.Total.Diff = c.Total.Ours.Total() - c.Total.Vnstat.Total()
		c.Total.Pct = pctOf(c.Total.Diff, c.Total.Vnstat.Total())
		sort.Slice(c.Days, func(i, j int) bool { return c.Days[i].Day < c.Days[j].Day })
		out = append(out, c)
	}
	return out, nil
}

// PctOf 导出给 server 用:diff/base 百分比,保留两位小数。
func PctOf(diff, base int64) float64 { return pctOf(diff, base) }

// Backup 在线一致性备份(VACUUM INTO,不阻塞写入方太久;目标文件必须不存在)。
func (s *Store) Backup(ctx context.Context, dst string) error {
	_, err := s.W.ExecContext(ctx, `VACUUM INTO ?`, dst)
	return err
}

// Ping 健康检查。
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.R.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
}
