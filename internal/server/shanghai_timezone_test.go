package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/domoxiaojun/Meterly/internal/store"
)

func TestShanghaiDefaultsPreserveExplicitTimezone(t *testing.T) {
	s, st, _, _ := setup(t, "127.0.0.1:8787")
	defer s.Close()
	if s.cfg.TZ != "Asia/Shanghai" || s.cfg.loc.String() != "Asia/Shanghai" {
		t.Fatalf("server default timezone: %s/%s", s.cfg.TZ, s.cfg.loc)
	}
	host, err := st.HostByName(context.Background(), "h1")
	if err != nil {
		t.Fatal(err)
	}
	if host.TZ != "Asia/Shanghai" {
		t.Fatalf("new host default timezone: %s", host.TZ)
	}
	explicit := &Config{TZ: "America/New_York"}
	if err := explicit.finish(); err != nil {
		t.Fatal(err)
	}
	if explicit.TZ != "America/New_York" || explicit.loc.String() != "America/New_York" {
		t.Fatal("explicit server zone overwritten")
	}
	if _, err := st.CreateHost(context.Background(), "explicit-zone", store.HostOpts{TZ: "America/New_York"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateHost(context.Background(), "explicit-zone", store.HostOpts{QuotaBytes: 100}); err != nil {
		t.Fatal(err)
	}
	host, err = st.HostByName(context.Background(), "explicit-zone")
	if err != nil {
		t.Fatal(err)
	}
	if host.TZ != "America/New_York" {
		t.Fatalf("existing explicit host zone overwritten: %s", host.TZ)
	}
}

func TestShanghaiMidnightAnalysisDates(t *testing.T) {
	s, _, _, _ := setup(t, "127.0.0.1:8787")
	defer s.Close()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.TZ, s.cfg.loc = "Asia/Shanghai", loc
	midnight := time.Date(2026, 11, 12, 0, 0, 0, 0, loc)
	cases := []struct {
		name, query string
		now         time.Time
		from, to    time.Time
		live        bool
	}{
		{"before midnight today", "period=today", midnight.Add(-23 * time.Second), midnight.AddDate(0, 0, -1), midnight.Add(-time.Minute), true},
		{"before midnight calendar today", "period=custom&start=2026-11-11&end=2026-11-11", midnight.Add(-23 * time.Second), midnight.AddDate(0, 0, -1), midnight.Add(-time.Minute), true},
		{"after midnight today", "period=today", midnight.Add(97 * time.Second), midnight, midnight.Add(time.Minute), true},
		{"after midnight calendar today", "period=day&start=2026-11-12", midnight.Add(97 * time.Second), midnight, midnight.Add(time.Minute), true},
		{"after midnight yesterday", "period=yesterday", midnight.Add(97 * time.Second), midnight.AddDate(0, 0, -1), midnight, false},
		{"after midnight old calendar date", "period=custom&start=2026-11-11&end=2026-11-11", midnight.Add(97 * time.Second), midnight.AddDate(0, 0, -1), midnight, false},
		{"after midnight spanning today", "period=custom&start=2026-11-11&end=2026-11-12", midnight.Add(97 * time.Second), midnight.AddDate(0, 0, -1), midnight.Add(time.Minute), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Supply UTC instants: the analysis day must come from the configured Shanghai zone.
			now := tc.now.UTC()
			s.Now = func() time.Time { return now }
			from, to, err := s.analysisPeriod(httptest.NewRequest("GET", "/?"+tc.query, nil), now)
			if err != nil || from != tc.from.Unix() || to != tc.to.Unix() {
				t.Fatalf("range=%d..%d want=%d..%d err=%v", from, to, tc.from.Unix(), tc.to.Unix(), err)
			}
			w := do(s.Handler(), "GET", "/api/v2/analytics/rank?"+tc.query, panelTok, nil)
			if w.Code != 200 {
				t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
			}
			var body struct {
				Meta struct {
					TZ       string `json:"tz"`
					Live     bool   `json:"live"`
					From, To int64
				} `json:"meta"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Meta.TZ != "Asia/Shanghai" || body.Meta.Live != tc.live || body.Meta.From != from || body.Meta.To != to {
				t.Fatalf("unexpected metadata: %+v", body.Meta)
			}
		})
	}
}
