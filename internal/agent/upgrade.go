package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Upgrade 下载新二进制 -> 校验 sha256 -> 原子替换 target。不自动重启(由调用方 systemctl restart)。
func Upgrade(ctx context.Context, url, wantSHA, target string) error {
	wantSHA = strings.ToLower(strings.TrimSpace(wantSHA))
	if len(wantSHA) != 64 {
		return errors.New("必须提供 64 位十六进制 sha256")
	}
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "file://") {
		return errors.New("升级地址必须为 https:// 或 file://")
	}
	var r io.ReadCloser
	if p, ok := strings.CutPrefix(url, "file://"); ok {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		r = f
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
		if err != nil {
			return fmt.Errorf("下载失败: %s", netErrKind(err))
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
		}
		r = resp.Body
	}
	defer r.Close()
	tmp, err := os.CreateTemp(filepath.Dir(target), ".meterly-agent-upgrade-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, 200<<20)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
		return fmt.Errorf("sha256 不匹配: 期望 %s 实际 %s,已放弃升级", wantSHA, got)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}
