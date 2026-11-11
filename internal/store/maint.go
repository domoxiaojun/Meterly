package store

import (
	"context"
	"time"
)

// Retention 保留策略。
type Retention struct {
	MinuteDays int // 默认 2
	HourDays   int // 默认 365
	// MinuteDetailHours 分钟表里非网卡明细(sing-box / 站点 IP 等)保留小时数,默认 48
	MinuteDetailHours int
}

func (r *Retention) Defaults() {
	if r.MinuteDays == 0 {
		r.MinuteDays = 2
	}
	if r.HourDays == 0 {
		r.HourDays = 365
	}
	if r.MinuteDetailHours == 0 {
		r.MinuteDetailHours = 48
	}
}

// Maintain only expires detail after its configured retention; ranking never destroys IPs.
func (s *Store) Maintain(ctx context.Context, r Retention, now time.Time) (int64, error) {
	r.Defaults()
	for _, table := range []string{"fact_minute", "site_minute", "fact_hour", "site_hour"} {
		age := int64(r.MinuteDetailHours) * 3600
		if table == "fact_hour" || table == "site_hour" {
			age = int64(r.HourDays) * 86400
		}
		if _, err := s.W.ExecContext(ctx, "DELETE FROM "+table+" WHERE ts < ?", now.Unix()-age); err != nil {
			return 0, err
		}
	}
	return 0, nil
}
