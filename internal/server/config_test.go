package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigPathEnvFallback(t *testing.T) {
	t.Setenv("METERLY_CONFIG", "/etc/meterly/server.json")
	t.Setenv("TRAFFICSTAT_CONFIG", "/etc/trafficstat/server.json")
	if g := ConfigPath(""); g != "/etc/meterly/server.json" {
		t.Fatalf("prefer new env: %s", g)
	}
	if g := ConfigPath("/explicit.json"); g != "/explicit.json" {
		t.Fatalf("flag wins: %s", g)
	}
	t.Setenv("METERLY_CONFIG", "")
	if g := ConfigPath(""); g != "/etc/trafficstat/server.json" {
		t.Fatalf("legacy env: %s", g)
	}
}

func TestFinishPanelTokenLegacyEnv(t *testing.T) {
	t.Setenv("METERLY_PANEL_TOKEN", "")
	t.Setenv("TRAFFICSTAT_PANEL_TOKEN", "legacy-panel-token-value")
	c := &Config{Listen: "0.0.0.0:8787", DBPath: filepath.Join(t.TempDir(), "x.db")}
	if err := c.finish(); err != nil {
		t.Fatal(err)
	}
	if c.panelToken != "legacy-panel-token-value" {
		t.Fatalf("panelToken=%q", c.panelToken)
	}
}

func TestFinishDefaultDBPath(t *testing.T) {
	t.Setenv("METERLY_PANEL_TOKEN", "")
	c := &Config{Listen: "127.0.0.1:8787"}
	if err := c.finish(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.DBPath, "meterly") && !strings.Contains(c.DBPath, "trafficstat.db") {
		t.Fatalf("db path %s", c.DBPath)
	}
}

func TestLoadConfigFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "server.json")
	if err := os.WriteFile(p, []byte(`{"listen":"127.0.0.1:9","db_path":"`+filepath.Join(dir, "s.db")+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9" {
		t.Fatalf("listen=%s", c.Listen)
	}
}
