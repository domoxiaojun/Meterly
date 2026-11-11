package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const releaseRepo = "domoxiaojun/Meterly"

var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

type releaseAsset struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

func releaseGet(ctx context.Context, url string) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", url, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("User-Agent", "Meterly")
	res, e := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if e != nil {
		return nil, errors.New("无法连接版本服务器")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("版本服务器 HTTP %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 1<<20))
}
func (s *Server) releaseFor(ctx context.Context, tag, arch string) (releaseAsset, error) {
	out := releaseAsset{Version: tag}
	if !releaseTag.MatchString(tag) || (arch != "amd64" && arch != "arm64") {
		return out, errors.New("只支持正式版本及 amd64/arm64")
	}
	data, e := s.ReleaseGet(ctx, "https://api.github.com/repos/"+releaseRepo+"/releases/tags/"+tag)
	if e != nil {
		return out, e
	}
	var r struct {
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Tag        string `json:"tag_name"`
	}
	if json.Unmarshal(data, &r) != nil || r.Draft || r.Prerelease || r.Tag != tag {
		return out, errors.New("版本不是已发布的正式版本")
	}
	base := "https://github.com/" + releaseRepo + "/releases/download/" + tag + "/"
	sum, e := s.ReleaseGet(ctx, base+"SHA256SUMS")
	if e != nil {
		return out, e
	}
	name := "meterly-agent-linux-" + arch
	for _, line := range strings.Split(string(sum), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name && len(fields[0]) == 64 {
			out.URL = base + name
			out.SHA256 = fields[0]
			return out, nil
		}
	}
	return out, errors.New("该版本缺少对应平台二进制或校验和")
}
func (s *Server) checkRelease(ctx context.Context) error {
	b, e := s.ReleaseGet(ctx, "https://api.github.com/repos/"+releaseRepo+"/releases/latest")
	if e != nil {
		return e
	}
	var r struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if json.Unmarshal(b, &r) != nil || r.Draft || r.Prerelease || !releaseTag.MatchString(r.Tag) {
		return errors.New("无可用正式版本")
	}
	out, _ := json.Marshal(map[string]any{"version": r.Tag, "checked_at": s.Now().Unix(), "url": "https://github.com/" + releaseRepo + "/releases/tag/" + r.Tag})
	_, e = s.st.W.ExecContext(ctx, `INSERT INTO settings(k,v) VALUES('latest_release',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, string(out))
	return e
}
func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	var raw string
	e := s.st.R.QueryRowContext(r.Context(), `SELECT v FROM settings WHERE k='latest_release'`).Scan(&raw)
	if e != nil {
		writeJSON(w, 200, map[string]any{"version": nil, "message": "尚未完成版本检查，请查看维护任务"})
		return
	}
	var v map[string]any
	if json.Unmarshal([]byte(raw), &v) != nil {
		writeErr(w, 500, "版本缓存无效")
		return
	}
	writeJSON(w, 200, v)
}
