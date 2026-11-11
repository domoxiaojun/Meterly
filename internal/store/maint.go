package store

import (
	"context"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

// Retention 保留策略。
type Retention struct {
	MinuteDays int // 默认 14
	HourDays   int // 默认 400
	TopNHour   int // 每 (host, 小时) 保留的 IP×入站 条数,默认 50
	TopNDay    int // 每 (host, 天) 默认 200
	// MinuteDetailHours 分钟表里非网卡明细(sing-box / 站点 IP 等)保留小时数,默认 25
	MinuteDetailHours int
}

func (r *Retention) Defaults() {
	if r.MinuteDays == 0 {
		r.MinuteDays = 14
	}
	if r.HourDays == 0 {
		r.HourDays = 400
	}
	if r.TopNHour == 0 {
		r.TopNHour = 50
	}
	if r.TopNDay == 0 {
		r.TopNDay = 200
	}
	if r.MinuteDetailHours == 0 {
		r.MinuteDetailHours = 25
	}
}

// Maintain 清理过期分钟/小时数据,并对已结束的小时/天做 Top-N + "其他"合并。返回合并掉的行数。
func (s *Store) Maintain(ctx context.Context, r Retention, now time.Time) (int64, error) {
	r.Defaults()
	nu := now.Unix()
	if _, err := s.W.ExecContext(ctx, `DELETE FROM fact_minute WHERE ts < ?`, nu-int64(r.MinuteDays)*86400); err != nil {
		return 0, err
	}
	if _, err := s.W.ExecContext(ctx, `DELETE FROM fact_minute WHERE kind <> 'iface' AND ts < ?`, nu-int64(r.MinuteDetailHours)*3600); err != nil {
		return 0, err
	}
	if _, err := s.W.ExecContext(ctx, `DELETE FROM fact_hour WHERE ts < ?`, nu-int64(r.HourDays)*86400); err != nil {
		return 0, err
	}
	// 小时结束 10 分钟后、天结束 1 小时后再合并,迟到数据会在下一轮再次合并
	n1, err := s.compact(ctx, "fact_hour", r.TopNHour, nu/3600*3600-600)
	if err != nil {
		return n1, err
	}
	n2, err := s.compact(ctx, "fact_day", r.TopNDay, nu-86400-3600)
	if err != nil {
		return n1 + n2, err
	}
	n3, err := s.compactSite(ctx, "fact_hour", r.TopNHour, nu/3600*3600-600)
	if err != nil {
		return n1 + n2 + n3, err
	}
	n4, err := s.compactSite(ctx, "fact_day", r.TopNDay, nu-86400-3600)
	return n1 + n2 + n3 + n4, err
}

func (s *Store) compact(ctx context.Context, table string, topN int, before int64) (int64, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT host_id, ts FROM `+table+` WHERE kind=? AND ts < ? AND dim1 NOT LIKE '~%'
		GROUP BY host_id, ts HAVING COUNT(*) > ?`, proto.KindSingbox, before, topN)
	if err != nil {
		return 0, err
	}
	type g struct{ host, ts int64 }
	var groups []g
	for rows.Next() {
		var x g
		if err := rows.Scan(&x.host, &x.ts); err != nil {
			rows.Close()
			return 0, err
		}
		groups = append(groups, x)
	}
	rows.Close()
	var merged int64
	for _, x := range groups {
		n, err := s.compactGroup(ctx, table, topN, x.host, x.ts)
		if err != nil {
			return merged, err
		}
		merged += n
	}
	return merged, nil
}

func (s *Store) compactGroup(ctx context.Context, table string, topN int, host, ts int64) (int64, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// 排名第 topN 之后的行(按 rx+tx 降序)
	q := `SELECT dim1, dim2, rx, tx FROM ` + table + ` WHERE host_id=? AND ts=? AND kind=? AND dim1 NOT LIKE '~%'
		ORDER BY rx+tx DESC, dim1, dim2 LIMIT -1 OFFSET ?`
	rows, err := tx.QueryContext(ctx, q, host, ts, proto.KindSingbox, topN)
	if err != nil {
		return 0, err
	}
	type row struct {
		d1, d2 string
		rx, tx int64
	}
	var rest []row
	var srx, stx int64
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.d1, &r.d2, &r.rx, &r.tx); err != nil {
			rows.Close()
			return 0, err
		}
		rest = append(rest, r)
		srx += r.rx
		stx += r.tx
	}
	rows.Close()
	if len(rest) == 0 {
		return 0, nil
	}
	del, err := tx.PrepareContext(ctx, `DELETE FROM `+table+` WHERE host_id=? AND ts=? AND kind=? AND dim1=? AND dim2=?`)
	if err != nil {
		return 0, err
	}
	defer del.Close()
	for _, r := range rest {
		if _, err := del.ExecContext(ctx, host, ts, proto.KindSingbox, r.d1, r.d2); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+`(host_id,ts,kind,dim1,dim2,rx,tx) VALUES(?,?,?,?,'',?,?)
		ON CONFLICT(host_id,ts,kind,dim1,dim2) DO UPDATE SET rx=rx+excluded.rx, tx=tx+excluded.tx`,
		host, ts, proto.KindSingbox, proto.DimOther, srx, stx); err != nil {
		return 0, err
	}
	return int64(len(rest)), tx.Commit()
}

// compactSite 对 kind=site 按 (host, ts, 站点 dim1) 保留流量前 topN 的客户端 IP(dim2),其余并入 dim2=~other。
func (s *Store) compactSite(ctx context.Context, table string, topN int, before int64) (int64, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT host_id, ts, dim1 FROM `+table+` WHERE kind=? AND ts < ? AND dim2 NOT LIKE '~%' AND dim2 <> ''
		GROUP BY host_id, ts, dim1 HAVING COUNT(*) > ?`, proto.KindSite, before, topN)
	if err != nil {
		return 0, err
	}
	type g struct {
		host, ts int64
		site     string
	}
	var groups []g
	for rows.Next() {
		var x g
		if err := rows.Scan(&x.host, &x.ts, &x.site); err != nil {
			rows.Close()
			return 0, err
		}
		groups = append(groups, x)
	}
	rows.Close()
	var merged int64
	for _, x := range groups {
		n, err := s.compactSiteGroup(ctx, table, topN, x.host, x.ts, x.site)
		if err != nil {
			return merged, err
		}
		merged += n
	}
	return merged, nil
}

func (s *Store) compactSiteGroup(ctx context.Context, table string, topN int, host, ts int64, site string) (int64, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q := `SELECT dim2, rx, tx FROM ` + table + ` WHERE host_id=? AND ts=? AND kind=? AND dim1=? AND dim2 NOT LIKE '~%' AND dim2 <> ''
		ORDER BY rx+tx DESC, dim2 LIMIT -1 OFFSET ?`
	rows, err := tx.QueryContext(ctx, q, host, ts, proto.KindSite, site, topN)
	if err != nil {
		return 0, err
	}
	type row struct {
		d2     string
		rx, tx int64
	}
	var rest []row
	var srx, stx int64
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.d2, &r.rx, &r.tx); err != nil {
			rows.Close()
			return 0, err
		}
		rest = append(rest, r)
		srx += r.rx
		stx += r.tx
	}
	rows.Close()
	if len(rest) == 0 {
		return 0, nil
	}
	del, err := tx.PrepareContext(ctx, `DELETE FROM `+table+` WHERE host_id=? AND ts=? AND kind=? AND dim1=? AND dim2=?`)
	if err != nil {
		return 0, err
	}
	defer del.Close()
	for _, r := range rest {
		if _, err := del.ExecContext(ctx, host, ts, proto.KindSite, site, r.d2); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+`(host_id,ts,kind,dim1,dim2,rx,tx) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(host_id,ts,kind,dim1,dim2) DO UPDATE SET rx=rx+excluded.rx, tx=tx+excluded.tx`,
		host, ts, proto.KindSite, site, proto.DimOther, srx, stx); err != nil {
		return 0, err
	}
	return int64(len(rest)), tx.Commit()
}
