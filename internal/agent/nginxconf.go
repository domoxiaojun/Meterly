package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SiteSource 一条要跟踪的 access_log。
type SiteSource struct {
	Path           string   `json:"path"`
	Site           string   `json:"site"`                 // server_name,或从路径推断
	Format         string   `json:"format,omitempty"`     // log_format 名,空则自动探测
	FormatDef      string   `json:"format_def,omitempty"` // 可选:直接给出格式串
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
	IPHeader       string   `json:"ip_header,omitempty"`
	RealIPApplied  bool     `json:"real_ip_applied,omitempty"`
}

var reServer = regexp.MustCompile(`(?s)server\s*\{`)
var reLogFormat = regexp.MustCompile(`(?is)log_format\s+(\S+)\s+([^;]+);`)
var reAccessLog = regexp.MustCompile(`(?is)access_log\s+([^;]+);`)
var reServerName = regexp.MustCompile(`(?is)server_name\s+([^;]+);`)
var reCComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
var reHashComment = regexp.MustCompile(`(?m)^\s*#.*$`)

func stripNginxComments(s string) string {
	s = reCComment.ReplaceAllString(s, " ")
	s = reHashComment.ReplaceAllString(s, " ")
	return s
}

// ParseLogFormats 从 nginx 配置文本提取 log_format 名 → 定义(已拼接相邻引号串)。
func ParseLogFormats(text string) map[string]string {
	text = stripNginxComments(text)
	out := map[string]string{}
	for _, m := range reLogFormat.FindAllStringSubmatch(text, -1) {
		name, def := m[1], strings.TrimSpace(m[2])
		out[name] = joinNginxStrings(def)
	}
	return out
}

// joinNginxStrings 把 'a' "b" 拼成 ab;未加引号的部分原样保留。
func joinNginxStrings(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	i := 0
	for i < len(s) {
		for i < len(s) && s[i] == ' ' || i < len(s) && (s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			// 引号外空白:若已有内容,可能是格式中的字面空格,也可能是拼接间隔。
			// nginx 只拼接相邻引号串;引号外的空白属于格式本身。为简单起见,
			// 引号之间的空白丢掉(标准的多行 log_format 写法),其它空白保留。
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == '\'' || s[i] == '"' {
			q := s[i]
			i++
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) {
					b.WriteByte(s[i+1])
					i += 2
					continue
				}
				if s[i] == q {
					i++
					break
				}
				b.WriteByte(s[i])
				i++
			}
			continue
		}
		j := i
		for j < len(s) && s[j] != '\'' && s[j] != '"' {
			j++
		}
		b.WriteString(s[i:j])
		i = j
	}
	return b.String()
}

type serverBlock struct {
	Names          []string
	Logs           []accessLogRef
	TrustedProxies []string
	IPHeader       string
}

type accessLogRef struct {
	Path, Format string
}

func parseServerBlocks(text string) []serverBlock { return serverBlocks(nginxNodes(text), nil, "") }

func parseAccessLogDirective(s string) accessLogRef {
	fs := strings.Fields(strings.TrimSpace(s))
	if len(fs) == 0 {
		return accessLogRef{}
	}
	p := fs[0]
	if p == "off" || p == "/dev/stdout" || p == "/dev/stderr" || p == "syslog:" || strings.HasPrefix(p, "syslog:") {
		return accessLogRef{}
	}
	fmtName := ""
	if len(fs) > 1 && !strings.HasPrefix(fs[1], "buffer=") && !strings.HasPrefix(fs[1], "flush=") && fs[1] != "if=" {
		fmtName = fs[1]
	}
	return accessLogRef{Path: p, Format: fmtName}
}

// MapContainerLogPath 把 1Panel OpenResty 容器内路径映射到宿主机路径。
func MapContainerLogPath(p string) []string {
	cands := []string{p}
	if strings.HasPrefix(p, "/www/") {
		cands = append(cands, "/opt/1panel/www/"+strings.TrimPrefix(p, "/www/"))
	}
	if strings.HasPrefix(p, "/var/log/nginx/") {
		cands = append(cands, "/opt/1panel/apps/openresty/openresty/log/"+filepath.Base(p))
	}
	return cands
}

func inferSiteFromPath(p string) string {
	parts := strings.Split(filepath.ToSlash(p), "/")
	for i, x := range parts {
		if x == "sites" && i+1 < len(parts) && parts[i+1] != "" {
			return parts[i+1]
		}
	}
	base := filepath.Base(p)
	for _, suf := range []string{".log", ".access.log"} {
		if strings.HasSuffix(base, suf) && base != "access.log" && base != "error.log" && !strings.Contains(base, "stream") {
			return strings.TrimSuffix(base, suf)
		}
	}
	if base != "access.log" && strings.HasSuffix(base, ".log") && !strings.Contains(base, "error") && !strings.Contains(base, "stream") {
		return strings.TrimSuffix(base, ".log")
	}
	return ""
}

func isSkippableLog(p string) bool {
	b := strings.ToLower(filepath.Base(p))
	if strings.Contains(b, "error") || strings.Contains(b, "stream") {
		return true
	}
	return false
}

// WalkNginxConfs 在 roots 下找 .conf / nginx.conf,跳过备份与远程资源树。
func WalkNginxConfs(roots []string, readFile func(string) ([]byte, error), walk func(root string, fn func(path string, isDir bool) error) error) (confs []string, formats map[string]string, sources []SiteSource) {
	formats = map[string]string{}
	seen := map[string]SiteSource{}
	texts := map[string]string{}
	skipFrag := []string{"/resource/apps/remote/", "/backup/", "/node_modules/", "/.git/", "/cache/"}
	for _, root := range roots {
		_ = walk(root, func(path string, isDir bool) error {
			rel := filepath.ToSlash(path)
			for _, s := range skipFrag {
				if strings.Contains(rel, s) {
					if isDir {
						return filepath.SkipDir
					}
					return nil
				}
			}
			if isDir {
				base := filepath.Base(path)
				if base == ".git" || base == "node_modules" || base == "cache" || base == "backup" {
					return filepath.SkipDir
				}
				return nil
			}
			name := filepath.Base(path)
			if name != "nginx.conf" && !strings.HasSuffix(name, ".conf") {
				return nil
			}
			confs = append(confs, path)
			b, err := readFile(path)
			if err != nil {
				return nil
			}
			text := string(b)
			texts[path] = text
			for k, v := range ParseLogFormats(text) {
				formats[k] = v
			}

			return nil
		})
	}

	// Expand only discovered configuration files. No shell, nginx reload or arbitrary file reads.
	used := map[string]bool{}
	var expand func([]nginxNode, string, map[string]bool, int) []nginxNode
	expand = func(nodes []nginxNode, base string, stack map[string]bool, depth int) []nginxNode {
		if depth > 16 {
			return nil
		}
		out := []nginxNode{}
		for _, n := range nodes {
			if n.name == "include" && len(n.args) == 1 {
				pattern := n.args[0]
				if !filepath.IsAbs(pattern) {
					pattern = filepath.Join(base, pattern)
				}
				for _, file := range confs {
					match, _ := filepath.Match(pattern, file)
					if !match || stack[file] {
						continue
					}
					used[file] = true
					stack[file] = true
					out = append(out, expand(nginxNodes(texts[file]), base, stack, depth+1)...)
					delete(stack, file)
				}
				continue
			}
			if len(n.children) > 0 {
				n.children = expand(n.children, base, stack, depth+1)
			}
			out = append(out, n)
		}
		return out
	}
	sort.SliceStable(confs, func(i, j int) bool {
		return filepath.Base(confs[i]) == "nginx.conf" && filepath.Base(confs[j]) != "nginx.conf"
	})
	for _, path := range confs {
		if used[path] {
			continue
		}
		text := texts[path]
		for _, bl := range serverBlocks(expand(nginxNodes(text), filepath.Dir(path), map[string]bool{path: true}, 0), nil, "") {
			site := ""
			if len(bl.Names) > 0 {
				site = bl.Names[0]
			}
			for _, lg := range bl.Logs {
				srcSite := site
				inferred := srcSite == ""
				if srcSite == "" {
					srcSite = inferSiteFromPath(lg.Path)
				}
				if srcSite == "" {
					srcSite = "_default"
				}
				for _, cand := range MapContainerLogPath(lg.Path) {
					if _, ok := seen[cand]; ok && !inferred {
						s := seen[cand]
						if s.Site == "_default" || s.Site == "" {
							s.Site, s.Format = srcSite, lg.Format
							seen[cand] = s
						}
						continue
					}
					if _, ok := seen[cand]; ok {
						continue
					}
					seen[cand] = SiteSource{Path: cand, Site: srcSite, Format: lg.Format,
						TrustedProxies: bl.TrustedProxies, IPHeader: bl.IPHeader, RealIPApplied: len(bl.TrustedProxies) > 0 && bl.IPHeader != ""}
				}
			}
		}
	}
	for _, s := range seen {
		sources = append(sources, s)
	}
	return confs, formats, sources
}

// FileExists 用于把候选路径筛成真实文件(可注入)。
func ExistingPaths(paths []string, stat func(string) (os.FileInfo, error)) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		if stat == nil {
			st, err := os.Stat(p)
			if err != nil || st.IsDir() {
				continue
			}
		} else {
			st, err := stat(p)
			if err != nil || st.IsDir() {
				continue
			}
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
