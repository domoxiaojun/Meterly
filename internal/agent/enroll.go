package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/domoxiaojun/Meterly/internal/version"
)

// EnrollRequest / EnrollResponse 与中心 POST /api/v1/enroll/redeem 对应。
type EnrollRequest struct {
	Code     string `json:"code"`
	Hostname string `json:"hostname,omitempty"`
}

type EnrollResponse struct {
	Token string `json:"token"`
	Host  string `json:"host"`
}

// RedeemEnroll 用一次性注册码向中心换取 host token。code 不写日志。
func RedeemEnroll(ctx context.Context, serverURL, caFile string, allowHTTP bool, code, hostname string, timeout time.Duration) (*EnrollResponse, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("注册码为空")
	}
	u, err := url.Parse(strings.TrimRight(serverURL, "/"))
	if err != nil || u.Host == "" {
		return nil, errors.New("中心地址无效")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !allowHTTP) {
		return nil, errors.New("中心地址必须是 https(内网明文需 --allow-http)")
	}
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	tr := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, errors.New("读取 ca_file 失败")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("ca_file 无有效证书")
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	body, _ := json.Marshal(EnrollRequest{Code: code, Hostname: hostname})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+"/api/v1/enroll/redeem", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "meterly-agent/"+version.Version)
	client := &http.Client{Transport: tr, Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("注册失败: %s", netErrKind(err))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, errors.New("中心限流,请稍后重试")
	}
	if resp.StatusCode/100 != 2 {
		var er struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &er)
		if er.Error != "" {
			return nil, fmt.Errorf("注册失败: %s", er.Error)
		}
		return nil, fmt.Errorf("注册失败: HTTP %d", resp.StatusCode)
	}
	var out EnrollResponse
	if err := json.Unmarshal(data, &out); err != nil || out.Token == "" {
		return nil, errors.New("注册响应无效")
	}
	return &out, nil
}

// WriteTokenFile 原子写入 0600 的 token 文件。
func WriteTokenFile(path, token string) error {
	if err := os.MkdirAll(filepathDir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(strings.TrimSpace(token)+"\n"))
}

func filepathDir(p string) string {
	i := strings.LastIndexAny(p, "/\\")
	if i < 0 {
		return "."
	}
	if i == 0 {
		return "/"
	}
	return p[:i]
}
