package agent

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// Both the bearer token and enrollment body are credentials. Keep every hop on
// the original origin, even when Go would forward headers to a subdomain.
func credentialRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("中心重定向次数过多")
	}
	if len(via) == 0 {
		return nil
	}
	origin := func(u *url.URL) string {
		port := u.Port()
		if port == "" {
			if u.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		return strings.ToLower(u.Scheme+"://"+u.Hostname()) + ":" + port
	}
	if req.URL.User != nil || origin(req.URL) != origin(via[0].URL) {
		return errors.New("拒绝中心跨来源或降级重定向")
	}
	return nil
}
