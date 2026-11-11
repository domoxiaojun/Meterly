package agent

import (
	"strings"
	"testing"
)

const procSample = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000 10 0 0 0 0 0 0 1000 10 0 0 0 0 0 0
  eth0: 123456789 1000 0 0 0 0 0 0 987654321 900 0 0 0 0 0 0
   wt0: 42 1 0 0 0 0 0 0 43 1 0 0 0 0 0 0
`

func TestParseNetDev(t *testing.T) {
	m, err := ParseNetDev(strings.NewReader(procSample))
	if err != nil {
		t.Fatal(err)
	}
	if m["eth0"] != [2]uint64{123456789, 987654321} || m["wt0"] != [2]uint64{42, 43} {
		t.Fatalf("got %v", m)
	}
}

func TestCounterDelta(t *testing.T) {
	const max = 1 << 40
	cases := []struct {
		name      string
		prev, cur uint64
		bits      int
		want      uint64
		reason    DeltaReason
	}{
		{"正常", 100, 250, 64, 150, DeltaNormal},
		{"32 位回绕", 1<<32 - 10, 5, 32, 15, DeltaWrap},
		{"64 位回绕", ^uint64(0) - 9, 5, 64, 15, DeltaWrap},
		{"计数器重置", 5_000_000, 1000, 64, 1000, DeltaReset},
		{"超速率上限", 0, 1 << 41, 64, 0, DeltaClamp},
	}
	for _, c := range cases {
		got, r := CounterDelta(c.prev, c.cur, c.bits, max)
		if got != c.want || r != c.reason {
			t.Errorf("%s: got %d/%s want %d/%s", c.name, got, r, c.want, c.reason)
		}
	}
}
