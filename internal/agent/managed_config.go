package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// WriteManagedConfig avoids persisting discovery as explicit local overrides.
// Reinstallation preserves user edits and existing credentials.
func WriteManagedConfig(path string, c *Config, only, skip []string) error {
	obj := map[string]any{"server_url": c.ServerURL, "token_file": c.TokenFile, "state_dir": c.StateDir, "discovery": true, "interval": "60s"}
	if c.CAFile != "" {
		obj["ca_file"] = c.CAFile
	}
	if c.AllowHTTP {
		obj["allow_http"] = true
	}
	if b, e := os.ReadFile(path); e == nil {
		var old map[string]any
		if json.Unmarshal(b, &old) != nil {
			return errors.New("现有配置无效，请修正后重装")
		}
		if old["server_url"] != c.ServerURL {
			return errors.New("现有配置属于其他中心；请先单独审核中心与 token 切换")
		}
		obj = old
	} else if !os.IsNotExist(e) {
		return e
	}
	if len(only) > 0 || len(skip) > 0 {
		overrides, _ := obj["overrides"].(map[string]any)
		if overrides == nil {
			overrides = map[string]any{}
		}
		cols, _ := overrides["collectors"].(map[string]any)
		if cols == nil {
			cols = map[string]any{}
		}
		contains := func(v []string, k string) bool {
			for _, s := range v {
				if s == k {
					return true
				}
			}
			return false
		}
		for _, k := range []string{"netdev", "site", "singbox", "netbird", "xray"} {
			if len(only) > 0 {
				cols[k] = contains(only, k)
			}
			if contains(skip, k) {
				cols[k] = false
			}
		}
		overrides["collectors"] = cols
		obj["overrides"] = overrides
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(obj, "", "  ")
	if e != nil {
		return e
	}
	return writeFileAtomic(path, append(b, '\n'))
}
