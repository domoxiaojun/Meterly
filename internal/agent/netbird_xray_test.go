package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

func TestNetbirdJSONDeltasAndReset(t *testing.T) {
	payload := func(rx, tx int) []byte {
		return []byte(`{"fqdn":"domo-lax2.io.lo","peers":{"details":[
			{"fqdn":"peer-a.example.net","netbirdIp":"100.64.0.10/16","transferReceived":` + itoa(int64(rx)) + `,"transferSent":` + itoa(int64(tx)) + `}
		]}}`)
	}
	acc := NewNetbirdAcc(NetbirdConfig{Enabled: true}, nil)
	acc.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("unset")
	}
	var stage int
	acc.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "netbird" {
			t.Fatalf("cmd %s", name)
		}
		switch stage {
		case 0:
			return payload(1000, 10), nil
		case 1:
			return payload(1500, 40), nil
		case 2:
			return payload(20, 1), nil // 重启
		}
		return nil, errors.New("eof")
	}
	ctx := context.Background()
	got, err := acc.Collect(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("baseline: %v %+v", err, got)
	}
	if acc.Self != "domo-lax2.io.lo" {
		t.Fatalf("self %s", acc.Self)
	}
	stage = 1
	got, err = acc.Collect(ctx)
	if err != nil || len(got) != 1 || got[0].Key != "peer-a.example.net" || got[0].RX != 500 || got[0].TX != 30 {
		t.Fatalf("delta: %v %+v", err, got)
	}
	if got[0].Kind != proto.KindNetbird || got[0].Key2 != "100.64.0.10" {
		t.Fatalf("kind/ip %+v", got[0])
	}
	stage = 2
	got, err = acc.Collect(ctx)
	if err != nil || len(got) != 1 || got[0].RX != 20 || got[0].TX != 1 {
		t.Fatalf("reset: %v %+v", err, got)
	}
}

func TestNetbirdWGFallback(t *testing.T) {
	acc := NewNetbirdAcc(NetbirdConfig{Enabled: true}, nil)
	acc.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "netbird" {
			return nil, errors.New("no netbird")
		}
		if name == "wg" {
			return []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcd 100 5\n"), nil
		}
		return nil, errors.New(name)
	}
	got, err := acc.Collect(context.Background())
	if err != nil || len(got) != 0 { // baseline
		t.Fatalf("wg baseline %v %+v", err, got)
	}
	got, err = acc.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 { // same counters, no delta
		t.Fatalf("expected 0 delta, got %+v", got)
	}
	// 密钥不得完整出现
	for k := range acc.St {
		if len(k) > 16 || !strings.HasPrefix(k, "wg:") {
			t.Fatalf("key too revealing: %q", k)
		}
	}
}

func TestXrayStatsQuery(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"stat": []map[string]any{
			{"name": "inbound>>>api>>>traffic>>>uplink", "value": "10"},
			{"name": "inbound>>>inbound-20383>>>traffic>>>uplink", "value": "1000"},
			{"name": "inbound>>>inbound-20383>>>traffic>>>downlink", "value": "5000"},
			{"name": "user>>>demo@x>>>traffic>>>uplink", "value": "1000"},
			{"name": "user>>>demo@x>>>traffic>>>downlink", "value": "5000"},
		},
	})
	acc := NewXrayAcc(XrayConfig{Enabled: true, Bin: "/fake/xray", Server: "127.0.0.1:62789"}, nil)
	ncall := 0
	acc.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ncall++
		if name != "/fake/xray" || args[0] != "api" || !strings.Contains(args[2], "127.0.0.1:62789") {
			t.Fatalf("cmd %s %v", name, args)
		}
		if ncall == 1 {
			return body, nil
		}
		// 增量
		b2, _ := json.Marshal(map[string]any{
			"stat": []map[string]any{
				{"name": "inbound>>>inbound-20383>>>traffic>>>uplink", "value": "1100"},
				{"name": "inbound>>>inbound-20383>>>traffic>>>downlink", "value": "5300"},
				{"name": "user>>>demo@x>>>traffic>>>uplink", "value": "1100"},
				{"name": "user>>>demo@x>>>traffic>>>downlink", "value": "5300"},
			},
		})
		return b2, nil
	}
	got, err := acc.Collect(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("baseline %v %+v", err, got)
	}
	got, err = acc.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var inb, user Counter
	for _, c := range got {
		if c.Kind == proto.KindXray && c.Key == "inbound-20383" {
			inb = c
		}
		if c.Kind == proto.KindXrayUser && c.Key == "demo@x" {
			user = c
		}
	}
	if inb.RX != 100 || inb.TX != 300 || user.RX != 100 || user.TX != 300 {
		t.Fatalf("deltas inb=%+v user=%+v all=%+v", inb, user, got)
	}
}
