package human

import "testing"

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]int64{"2TiB": 2 << 40, "500GB": 500e9, "1.5 T": 3 << 39, "1073741824": 1 << 30, "": 0, "10 gib": 10 << 30, "3K": 3072} {
		got, err := ParseBytes(in)
		if err != nil || got != want {
			t.Fatalf("%q: %d %v", in, got, err)
		}
	}
	for _, bad := range []string{"abc", "-1GB", "1.2.3TB", "-5"} {
		if _, err := ParseBytes(bad); err == nil {
			t.Fatalf("%q 应报错", bad)
		}
	}
}
