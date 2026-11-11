package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// ImportHours 以"覆盖"语义写入某 host 的小时数据(幂等,可重复执行),并重算受影响自然日的天表。
// 仅用于回填 agent 上线前的历史(如 vnstat),不要覆盖已有 agent 数据的小时。
func (s *Store) ImportHours(ctx context.Context, hostID int64, loc *time.Location, kind, dim1 string, hours map[int64]Pair) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	days := map[int64]bool{}
	for ts, p := range hours {
		if _, err := tx.ExecContext(ctx, `INSERT INTO fact_hour(host_id,ts,kind,dim1,dim2,rx,tx) VALUES(?,?,?,?,'',?,?)
			ON CONFLICT(host_id,ts,kind,dim1,dim2) DO UPDATE SET rx=excluded.rx, tx=excluded.tx`, hostID, ts, kind, dim1, p.RX, p.TX); err != nil {
			return err
		}
		days[DayStart(ts, loc)] = true
	}
	for d := range days {
		end := time.Unix(d, 0).In(loc).AddDate(0, 0, 1).Unix()
		if _, err := tx.ExecContext(ctx, `DELETE FROM fact_day WHERE host_id=? AND ts=? AND kind=? AND dim1=? AND dim2=''`, hostID, d, kind, dim1); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO fact_day(host_id,ts,kind,dim1,dim2,rx,tx)
			SELECT ?, ?, kind, dim1, '', SUM(rx), SUM(tx) FROM fact_hour WHERE host_id=? AND kind=? AND dim1=? AND dim2='' AND ts>=? AND ts<?
			GROUP BY kind, dim1`, hostID, d, hostID, kind, dim1, d, end); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ParseVnstat 解析 `vnstat --json` 输出,返回 网卡 -> 小时桶。有小时数据的用小时数据;
// 更早只有日数据的日子按 24 小时均摊(会在 UI 曲线上表现为平台,仅作历史回填)。before>0 时只取该时间之前的桶。
func ParseVnstat(r io.Reader, ifaces []string, before int64) (map[string]map[int64]Pair, error) {
	var v struct {
		Interfaces []struct {
			Name    string `json:"name"`
			Traffic struct {
				Hour []struct {
					TS int64 `json:"timestamp"`
					RX int64 `json:"rx"`
					TX int64 `json:"tx"`
				} `json:"hour"`
				Day []struct {
					TS int64 `json:"timestamp"`
					RX int64 `json:"rx"`
					TX int64 `json:"tx"`
				} `json:"day"`
			} `json:"traffic"`
		} `json:"interfaces"`
	}
	if err := json.NewDecoder(r).Decode(&v); err != nil {
		return nil, fmt.Errorf("vnstat JSON 无效: %w", err)
	}
	want := map[string]bool{}
	for _, i := range ifaces {
		want[i] = true
	}
	out := map[string]map[int64]Pair{}
	for _, it := range v.Interfaces {
		if !want[it.Name] {
			continue
		}
		m := map[int64]Pair{}
		first := int64(1 << 62)
		for _, h := range it.Traffic.Hour {
			if before > 0 && h.TS+3600 > before {
				continue
			}
			m[h.TS/3600*3600] = Pair{h.RX, h.TX}
			first = min(first, h.TS)
		}
		for _, d := range it.Traffic.Day {
			if d.TS+86400 > first || (before > 0 && d.TS+86400 > before) {
				continue
			}
			var cr, ct int64
			for i := int64(0); i < 24; i++ {
				nr, nt := d.RX*(i+1)/24, d.TX*(i+1)/24
				m[(d.TS+i*3600)/3600*3600] = Pair{nr - cr, nt - ct}
				cr, ct = nr, nt
			}
		}
		out[it.Name] = m
	}
	return out, nil
}
