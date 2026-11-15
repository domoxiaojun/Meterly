package server

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestLivePeriodMinuteBoundary(t *testing.T) {
	s, _, _, _ := setup(t, "127.0.0.1:8787")
	defer s.Close()
	now := time.Date(2026, 11, 12, 12, 30, 37, 0, s.cfg.loc)
	s.Now = func() time.Time { return now }
	want := now.Truncate(time.Minute).Unix()
	tests := []struct{ name, query string }{
		{"today", "period=today"},
		{"day", "period=day&start=2026-11-12"},
		{"custom", "period=custom&start=2026-11-12&end=2026-11-12"},
		{"customSpan", "period=custom&start=2026-11-11&end=2026-11-12"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, to, err := s.analysisPeriod(httptest.NewRequest("GET", "/?"+tc.query, nil), now)
			if err != nil || to != want {
				t.Fatalf("to=%d want=%d err=%v", to, want, err)
			}
			if rec := do(s.Handler(), "GET", "/api/v2/series?"+tc.query+"&gran=hour", panelTok, nil); rec.Code != 200 {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestFrozenHistoricalAndFuturePeriods(t *testing.T) {
	s, _, _, _ := setup(t, "127.0.0.1:8787")
	defer s.Close()
	now := time.Date(2026, 11, 12, 12, 30, 37, 0, s.cfg.loc)
	s.Now = func() time.Time { return now }
	loc := s.cfg.loc
	mid := func(day int) int64 {
		return time.Date(2026, 11, day, 0, 0, 0, 0, loc).Unix()
	}
	tests := []struct {
		name, query string
		from, to    int64
		wantErr     bool
	}{
		{"yesterday", "period=yesterday", mid(11), mid(12), false},
		{"historicalDay", "period=day&start=2026-11-11", mid(11), mid(12), false},
		{"historicalCustom", "period=custom&start=2026-11-10&end=2026-11-11", mid(10), mid(12), false},
		{"futureDay", "period=day&start=2026-11-13", 0, 0, true},
		{"futureCustom", "period=custom&start=2026-11-13&end=2026-11-13", 0, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			from, to, err := s.analysisPeriod(httptest.NewRequest("GET", "/?"+tc.query, nil), now)
			if tc.wantErr {
				if err == nil || err.Error() != "查询日期尚未开始" {
					t.Fatalf("err=%v from=%d to=%d", err, from, to)
				}
				return
			}
			if err != nil || from != tc.from || to != tc.to {
				t.Fatalf("from=%d to=%d want %d..%d err=%v", from, to, tc.from, tc.to, err)
			}
			if rec := do(s.Handler(), "GET", "/api/v2/series?"+tc.query+"&gran=hour", panelTok, nil); rec.Code != 200 {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
