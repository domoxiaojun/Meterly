package compat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFirstEnv(t *testing.T) {
	t.Setenv("METERLY_TOKEN", "new-token-value")
	t.Setenv("TRAFFICSTAT_TOKEN", "old-token-value")
	if g := FirstEnv("METERLY_TOKEN", "TRAFFICSTAT_TOKEN"); g != "new-token-value" {
		t.Fatalf("prefer new: %q", g)
	}
	t.Setenv("METERLY_TOKEN", "  ")
	if g := FirstEnv("METERLY_TOKEN", "TRAFFICSTAT_TOKEN"); g != "old-token-value" {
		t.Fatalf("fallback old: %q", g)
	}
	t.Setenv("METERLY_TOKEN", "")
	t.Setenv("TRAFFICSTAT_TOKEN", "")
	if g := FirstEnv("METERLY_TOKEN", "TRAFFICSTAT_TOKEN"); g != "" {
		t.Fatalf("empty: %q", g)
	}
}

func TestFirstExisting(t *testing.T) {
	dir := t.TempDir()
	newp := filepath.Join(dir, "new.json")
	oldp := filepath.Join(dir, "old.json")
	if g := FirstExisting(newp, newp, oldp); g != newp {
		t.Fatalf("neither exists: %s", g)
	}
	if err := os.WriteFile(oldp, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if g := FirstExisting(newp, newp, oldp); g != oldp {
		t.Fatalf("old only: %s", g)
	}
	if err := os.WriteFile(newp, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if g := FirstExisting(newp, newp, oldp); g != newp {
		t.Fatalf("prefer new: %s", g)
	}
}
