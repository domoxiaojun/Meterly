package geo

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

// DefaultBaseURL DB-IP Lite 免费库下载地址,文件名形如 dbip-city-lite-2026-11.mmdb.gz。
const DefaultBaseURL = "https://download.db-ip.com/free"

// maxUncompressed 解压上限,防止异常文件写满磁盘(City Lite 当前约 130 MB)。
const maxUncompressed = 1 << 30

// Updater 首次启动下载 + 每月更新。下载失败只记录状态,不影响服务。
type Updater struct {
	DB      *DB
	BaseURL string
	Client  *http.Client
	Log     *slog.Logger
	Now     func() time.Time
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

// needs 本月(UTC)的库尚未就绪。DB-IP 每月初发布新库,库的 build 月份早于当前月即需更新。
func needs(build, now time.Time) bool {
	if build.IsZero() {
		return true
	}
	b, n := build.UTC(), now.UTC()
	return b.Year()*12+int(b.Month()) < n.Year()*12+int(n.Month())
}

// UpdateOnce 检查并下载需要更新的库。返回最后一个错误(任意一个库失败都不影响另一个)。
func (u *Updater) UpdateOnce(ctx context.Context) error {
	if u.BaseURL == "" {
		u.BaseURL = DefaultBaseURL
	}
	if u.Client == nil {
		u.Client = &http.Client{Timeout: 10 * time.Minute}
	}
	now := u.now()
	var lastErr error
	for _, kind := range []struct{ file, slug string }{{CityFile, "city"}, {ASNFile, "asn"}} {
		if !needs(u.DB.buildTime(kind.file), now) {
			continue
		}
		var err error
		// 当月库可能尚未发布(月初),回退到上月
		for _, m := range []time.Time{now.UTC(), now.UTC().AddDate(0, -1, 0)} {
			url := fmt.Sprintf("%s/dbip-%s-lite-%s.mmdb.gz", u.BaseURL, kind.slug, m.Format("2006-01"))
			if err = u.fetch(ctx, url, kind.file); err == nil {
				if u.Log != nil {
					u.Log.Info("归属地库已更新", "file", kind.file, "month", m.Format("2006-01"))
				}
				break
			}
			// 已有上月库时,当月 404 不必再回退下载同一份
			if !u.DB.buildTime(kind.file).IsZero() {
				break
			}
		}
		if err != nil {
			lastErr = errors.Join(lastErr, fmt.Errorf("%s: %w", kind.file, err))
		}
	}
	u.DB.smu.Lock()
	u.DB.status.LastCheck = now.Unix()
	if lastErr != nil {
		u.DB.status.LastErr = lastErr.Error()
	} else {
		u.DB.status.LastOK, u.DB.status.LastErr = now.Unix(), ""
	}
	u.DB.smu.Unlock()
	return lastErr
}

func (u *Updater) fetch(ctx context.Context, url, file string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "meterly-server (geo update)")
	resp, err := u.Client.Do(req)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(u.DB.dir, 0o700); err != nil {
		return err
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("不是有效的 gzip: %w", err)
	}
	tmp, err := os.CreateTemp(u.DB.dir, "."+file+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	n, err := io.Copy(tmp, io.LimitReader(zr, maxUncompressed+1))
	if err == nil && n > maxUncompressed {
		err = errors.New("解压后超过大小上限")
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	// 校验:能打开、结构完整、类型匹配,才替换
	r, err := maxminddb.Open(tmpName)
	if err != nil {
		return fmt.Errorf("库文件无效: %w", err)
	}
	if err := r.Verify(); err != nil {
		r.Close()
		return fmt.Errorf("库文件校验失败: %w", err)
	}
	r.Close()
	dst := filepath.Join(u.DB.dir, file)
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	nr, err := maxminddb.Open(dst)
	if err != nil {
		return err
	}
	u.DB.swap(file, nr)
	return nil
}

// Run 启动时立即检查一次,之后每 every 检查(默认 24h;只在跨月后真正下载)。
func (u *Updater) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 24 * time.Hour
	}
	u.DB.smu.Lock()
	u.DB.status.AutoUpdate = true
	u.DB.smu.Unlock()
	for {
		if err := u.UpdateOnce(ctx); err != nil && u.Log != nil && ctx.Err() == nil {
			u.Log.Warn("归属地库更新失败,继续使用现有库(或无归属地)", "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
