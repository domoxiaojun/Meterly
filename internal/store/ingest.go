package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/bits"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

type bucketKey struct {
	ts               int64
	kind, dim1, dim2 string
}

type IngestResult struct {
	Accepted   int   `json:"accepted"`
	Duplicates int   `json:"duplicates"`
	Rejected   int   `json:"rejected"`
	MaxSeq     int64 `json:"max_seq"`
}

// MinuteKinds 写入分钟表的 kind。网卡分钟数据保留 MinuteDays;sing-box 分钟明细(供"最近 1 小时"视图)
// 体积大,只保留 MinuteDetailHours(默认 25 小时),见 Maintain。
var MinuteKinds = map[string]bool{
	proto.KindIface: true, proto.KindSingbox: true, proto.KindSingboxUser: true,
	proto.KindSite: true, proto.KindSiteReq: true,
	proto.KindNetbird: true, proto.KindXray: true, proto.KindXrayUser: true,
}

// MaxSpread 网卡记录覆盖区间超过 2 分钟时按分钟摊分,最长摊 7 天;更长的按结束分钟归因。
const MaxSpread = 7 * 86400

// ValidRecord 基本校验,非法记录跳过(不让一条坏数据卡住整批重试)。
func ValidRecord(r *proto.Record, now int64) bool {
	if r.Seq <= 0 || r.TS < now-90*86400 || r.TS > now+600 || r.PrevTS > r.TS {
		return false
	}
	if len(r.Kind) == 0 || len(r.Kind) > 32 || len(r.Dim1) > 128 || len(r.Dim2) > 128 {
		return false
	}
	for _, c := range r.Kind {
		if !(c >= 'a' && c <= 'z' || c == '_') {
			return false
		}
	}
	// 单条增量上限 1 PiB,防止异常值污染
	return r.RX < 1<<50 && r.TX < 1<<50
}

// Ingest 幂等写入一批记录:同一事务内过滤高水位、upsert 分钟/小时/天三张表并推进高水位。
func (s *Store) Ingest(ctx context.Context, h *Host, loc *time.Location, meta proto.Meta, recs []proto.Record, clientIP string, now time.Time) (IngestResult, error) {
	var res IngestResult
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	inst := meta.InstanceID
	if inst == "" {
		inst = "default"
	}
	var hwm int64
	err = tx.QueryRowContext(ctx, `SELECT seq FROM ingest_hwm WHERE host_id=? AND instance=?`, h.ID, inst).Scan(&hwm)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return res, err
	}
	minute, hour, day := map[bucketKey]*[2]uint64{}, map[bucketKey]*[2]uint64{}, map[bucketKey]*[2]uint64{}
	add := func(m map[bucketKey]*[2]uint64, k bucketKey, rx, tx uint64) {
		v := m[k]
		if v == nil {
			v = &[2]uint64{}
			m[k] = v
		}
		v[0] += rx
		v[1] += tx
	}
	place := func(r *proto.Record, ts int64, rx, tx uint64) {
		if MinuteKinds[r.Kind] {
			add(minute, bucketKey{ts / 60 * 60, r.Kind, r.Dim1, r.Dim2}, rx, tx)
		}
		add(hour, bucketKey{ts / 3600 * 3600, r.Kind, r.Dim1, r.Dim2}, rx, tx)
		add(day, bucketKey{DayStart(ts, loc), r.Kind, r.Dim1, r.Dim2}, rx, tx)
	}
	maxSeq, maxTS := hwm, int64(0)
	nowU := now.Unix()
	for i := range recs {
		r := &recs[i]
		if r.Seq <= hwm {
			res.Duplicates++
			continue
		}
		if r.Seq > maxSeq {
			maxSeq, maxTS = r.Seq, r.TS
		}
		if !ValidRecord(r, nowU) {
			res.Rejected++
			continue
		}
		res.Accepted++
		span := r.TS - r.PrevTS
		if r.Kind == proto.KindIface && r.PrevTS > 0 && span > 120 && span <= MaxSpread {
			Spread(r.PrevTS, r.TS, r.RX, r.TX, func(ts int64, rx, tx uint64) { place(r, ts, rx, tx) })
		} else {
			place(r, r.TS-1, r.RX, r.TX) // 覆盖 (prev, ts],整点样本归入前一分钟
		}
	}
	for tbl, m := range map[string]map[bucketKey]*[2]uint64{"fact_minute": minute, "fact_hour": hour, "fact_day": day} {
		if len(m) == 0 {
			continue
		}
		st, err := tx.PrepareContext(ctx, `INSERT INTO `+tbl+`(host_id,ts,kind,dim1,dim2,rx,tx) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(host_id,ts,kind,dim1,dim2) DO UPDATE SET rx=rx+excluded.rx, tx=tx+excluded.tx`)
		if err != nil {
			return res, err
		}
		for k, v := range m {
			if _, err := st.ExecContext(ctx, h.ID, k.ts, k.kind, k.dim1, k.dim2, int64(v[0]), int64(v[1])); err != nil {
				st.Close()
				return res, err
			}
		}
		st.Close()
	}
	if maxSeq > hwm {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ingest_hwm(host_id,instance,seq,ts,updated) VALUES(?,?,?,?,?)
			ON CONFLICT(host_id,instance) DO UPDATE SET seq=excluded.seq, ts=excluded.ts, updated=excluded.updated`,
			h.ID, inst, maxSeq, maxTS, nowU); err != nil {
			return res, err
		}
	}
	mj, _ := json.Marshal(meta)
	if _, err := tx.ExecContext(ctx, `UPDATE hosts SET last_seen=?, last_ip=?, agent_version=?, meta_json=? WHERE id=?`,
		nowU, clientIP, truncate(meta.AgentVersion, 64), string(mj), h.ID); err != nil {
		return res, err
	}
	res.MaxSeq = maxSeq
	return res, tx.Commit()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// DayStart 返回 ts 在 loc 中所在日 00:00 的 UTC epoch。
func DayStart(ts int64, loc *time.Location) int64 {
	t := time.Unix(ts, 0).In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).Unix()
}

// Spread 把覆盖 (from, to] 的 rx/tx 按与每分钟的重叠秒数比例摊分,总量精确守恒。
// fn 收到的 ts 为该分钟内的某一秒(用于定位桶)。
func Spread(from, to int64, rx, tx uint64, fn func(ts int64, rx, tx uint64)) {
	span := uint64(to - from)
	var cum, prx, ptx uint64
	for m := from / 60 * 60; m < to; m += 60 {
		lo, hi := max(m, from), min(m+60, to)
		if hi <= lo {
			continue
		}
		cum += uint64(hi - lo)
		crx, ctx := mulDiv(rx, cum, span), mulDiv(tx, cum, span)
		if crx-prx > 0 || ctx-ptx > 0 {
			fn(lo, crx-prx, ctx-ptx)
		}
		prx, ptx = crx, ctx
	}
}

// mulDiv 计算 a*b/c(128 位中间值,不溢出)。
func mulDiv(a, b, c uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	q, _ := bits.Div64(hi, lo, c)
	return q
}
