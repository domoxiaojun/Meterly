package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfigTokenEnvFallback(t *testing.T) {
	dir := t.TempDir()
	base := `{
  "server_url": "https://meterly.internal:8443",
  "state_dir": "` + dir + `",
  "interval": "60s",
  "collectors": {"netdev": {"enabled": false}, "singbox": {"enabled": false, "url": "http://127.0.0.1:9", "poll_interval": "10s"}}
}`
	// 新变量优先
	p := writeCfg(t, dir, "a.json", base)
	t.Setenv("METERLY_TOKEN", "new-env-token-xx")
	t.Setenv("TRAFFICSTAT_TOKEN", "old-env-token-xx")
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "new-env-token-xx" {
		t.Fatalf("token=%q", c.Token)
	}
	// 只有旧变量
	t.Setenv("METERLY_TOKEN", "")
	c, err = LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "old-env-token-xx" {
		t.Fatalf("legacy token=%q", c.Token)
	}
}

func TestLoadConfigTokenFileWinsOverEnv(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "agent.token")
	if err := os.WriteFile(tok, []byte("file-token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{
  "server_url": "https://meterly.internal:8443",
  "token_file": "` + tok + `",
  "state_dir": "` + dir + `",
  "interval": "60s",
  "collectors": {"netdev": {"enabled": false}, "singbox": {"enabled": false, "url": "http://127.0.0.1:9", "poll_interval": "10s"}}
}`
	p := writeCfg(t, dir, "b.json", body)
	t.Setenv("METERLY_TOKEN", "env-should-lose")
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "file-token-value" {
		t.Fatalf("token=%q", c.Token)
	}
}
