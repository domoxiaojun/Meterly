package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/store"
	"net/http/httptest"
	"testing"
	"time"
)

func TestShanghaiMidnightEmptyCurrentDay(t *testing.T) {
	s, st, _, token := setup(t, "127.0.0.1:8787")
	defer s.Close()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.TZ, s.cfg.loc = "Asia/Shanghai", loc
	midnight := time.Date(2026, 11, 12, 0, 0, 0, 0, loc)
	host, err := st.HostByToken(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	records := []proto.Record{
		{Seq: 1, TS: midnight.Unix(), Kind: "site", Dim1: "yesterday.test", Dim2: "192.0.2.1", TX: 999, Site: &proto.SiteMetrics{Status: 200, Requests: 1}},
		{Seq: 2, TS: midnight.Add(time.Minute).Unix(), Kind: "site", Dim1: "today.test", Dim2: "192.0.2.1", TX: 200, Site: &proto.SiteMetrics{Status: 200, Requests: 1}},
	}
	if _, err := st.Ingest(context.Background(), host, loc, proto.Meta{InstanceID: "midnight", Protocol: 2}, records, "", midnight.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, seconds := range []int{0, 37, 60} {
		t.Run(fmt.Sprintf("00:%02d:%02d", seconds/60, seconds%60), func(t *testing.T) {
			now := midnight.Add(time.Duration(seconds) * time.Second)
			s.Now = func() time.Time { return now.UTC() }
			wantTo := now.Truncate(time.Minute).Unix()
			wantTX := uint64(0)
			if seconds >= 60 {
				wantTX = 200
			}
			for _, query := range []string{"period=today", "period=day&start=2026-11-12", "period=custom&start=2026-11-12&end=2026-11-12"} {
				from, to, err := s.analysisPeriod(httptest.NewRequest("GET", "/?"+query, nil), now.UTC())
				if err != nil || from != midnight.Unix() || to != wantTo {
					t.Fatalf("%s range=%d..%d err=%v", query, from, to, err)
				}
				for _, endpoint := range []string{"/api/v2/analytics/rank", "/api/v2/analytics/series", "/api/v2/overview", "/api/v2/series"} {
					w := do(s.Handler(), "GET", endpoint+"?"+query+"&kind=site&gran=hour", panelTok, nil)
					if w.Code != 200 {
						t.Fatalf("%s %s HTTP%d %s", endpoint, query, w.Code, w.Body.String())
					}
					var body struct {
						Meta struct {
							From, To int64
							TZ       string
							Live     bool
						}
						Period       struct{ From, To int64 }
						Totals, Site *store.Metrics
						Rows         []store.AnalysisRow
						Points       []struct {
							TS int64
							TX uint64
						}
					}
					if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if endpoint == "/api/v2/series" {
						if body.Period.From != from || body.Period.To != to {
							t.Fatalf("legacy period: %+v", body.Period)
						}
					} else if body.Meta.From != from || body.Meta.To != to || body.Meta.TZ != "Asia/Shanghai" || !body.Meta.Live {
						t.Fatalf("metadata lost live empty-day bounds: %+v", body.Meta)
					}
					for _, totals := range []*store.Metrics{body.Totals, body.Site} {
						if totals != nil && uint64(totals.TX) != wantTX {
							t.Fatalf("%s totals=%d want=%d", endpoint, totals.TX, wantTX)
						}
					}
					for _, row := range body.Rows {
						if row.Key == "yesterday.test" {
							t.Fatal("today query included yesterday")
						}
					}
					var seriesTX uint64
					for _, point := range body.Points {
						if point.TS < midnight.Unix() {
							t.Fatal("series included yesterday")
						}
						seriesTX += point.TX
					}
					if endpoint == "/api/v2/analytics/series" || endpoint == "/api/v2/series" {
						if seriesTX != wantTX {
							t.Fatalf("%s seriesTX=%d want=%d body=%s", endpoint, seriesTX, wantTX, w.Body.String())
						}
					}
					if seconds < 60 && (len(body.Rows) != 0 || len(body.Points) != 0) {
						t.Fatalf("empty minute included records: %s", w.Body.String())
					}
				}
			}
			for _, query := range []string{
				"period=day&start=2026-11-13",
				"period=custom&start=2026-11-13&end=2026-11-13",
				"period=custom&start=2026-11-13&end=2026-11-12",
				fmt.Sprintf("from=%d&to=%d", midnight.Unix(), midnight.Unix()),
				fmt.Sprintf("from=%d&to=%d", midnight.Unix()+60, midnight.Unix()),
			} {
				w := do(s.Handler(), "GET", "/api/v2/analytics/rank?"+query, panelTok, nil)
				if w.Code != 400 {
					t.Fatalf("invalid/future %s accepted: HTTP%d %s", query, w.Code, w.Body.String())
				}
			}
		})
	}
}
