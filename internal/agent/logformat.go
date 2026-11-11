package agent

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// 常见 nginx/OpenResty log_format。1Panel 的 main 是 combined 再加 $http_x_forwarded_for。
const (
	FormatCombined = `$remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent"`
	FormatMainXFF  = `$remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent" "$http_x_forwarded_for"`
	FormatCommon   = `$remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent`
)

// FormatLibrary 名称 → 格式串。detect 时会用配置里解析到的 log_format 覆盖/补充。
func FormatLibrary() map[string]string {
	return map[string]string{
		"combined": FormatCombined,
		"main":     FormatMainXFF, // 1Panel / 许多发行版把 main 定义为 combined+$xff;若实际是 combined,自动探测会回退
		"common":   FormatCommon,
		"":         FormatCombined,
	}
}

type tokKind int

const (
	tokLit tokKind = iota
	tokVar
)

type fmtTok struct {
	kind tokKind
	lit  string // 字面量,或变量名(不含 $)
	quot byte   // 变量外侧的引号/括号: ", [, 或 0
}

// LogFormat 编译后的 access_log 格式。
type LogFormat struct {
	Name   string
	Raw    string
	tokens []fmtTok
}

// CompileLogFormat 把 nginx log_format 编译成扫描器。相邻引号串会被拼接(nginx 写法)。
func CompileLogFormat(name, raw string) (*LogFormat, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("空的 log_format")
	}
	var toks []fmtTok
	s := raw
	for len(s) > 0 {
		switch s[0] {
		case '\'', '"':
			q := s[0]
			i := 1
			var b strings.Builder
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
			s = s[i:]
			// 把引号内的内容再拆成 字面量 / $var
			toks = append(toks, tokenizeInner(b.String(), q)...)
		default:
			// 直到下一个引号
			i := strings.IndexAny(s, `"'`)
			if i < 0 {
				toks = append(toks, tokenizeInner(s, 0)...)
				s = ""
			} else {
				toks = append(toks, tokenizeInner(s[:i], 0)...)
				s = s[i:]
			}
		}
	}
	// 合并相邻字面量
	var out []fmtTok
	for _, t := range toks {
		if t.kind == tokLit && t.lit == "" {
			continue
		}
		if t.kind == tokLit && len(out) > 0 && out[len(out)-1].kind == tokLit && out[len(out)-1].quot == 0 && t.quot == 0 {
			out[len(out)-1].lit += t.lit
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("log_format 没有字段")
	}
	return &LogFormat{Name: name, Raw: raw, tokens: out}, nil
}

func tokenizeInner(s string, quot byte) []fmtTok {
	var out []fmtTok
	// 括号包着的 $time_local: [$time_local]
	i := 0
	for i < len(s) {
		if s[i] == '$' {
			j := i + 1
			for j < len(s) && isVarChar(s[j]) {
				j++
			}
			name := s[i+1 : j]
			q := quot
			// 形如 [$time_local] 且当前不在引号内
			if quot == 0 && i > 0 && s[i-1] == '[' && j < len(s) && s[j] == ']' {
				// 把前面的 [ 从已输出字面量里剥掉,把 ] 吃掉
				if len(out) > 0 && out[len(out)-1].kind == tokLit && strings.HasSuffix(out[len(out)-1].lit, "[") {
					out[len(out)-1].lit = strings.TrimSuffix(out[len(out)-1].lit, "[")
				}
				q = '['
				j++ // skip ]
			}
			out = append(out, fmtTok{kind: tokVar, lit: name, quot: q})
			i = j
			continue
		}
		j := i + 1
		for j < len(s) && s[j] != '$' {
			j++
		}
		out = append(out, fmtTok{kind: tokLit, lit: s[i:j], quot: 0})
		i = j
	}
	return out
}

func isVarChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// LogLine 解析后我们关心的字段。
type LogLine struct {
	IP      string
	Time    time.Time
	Status  int
	Sent    uint64 // body_bytes_sent 或 bytes_sent
	Recv    uint64 // request_length, 0=日志没有
	Host    string // $host / $http_host / $server_name
	HasRecv bool
	HasHost bool
}

// Parse 按编译格式扫描一行。失败返回 nil。
func (f *LogFormat) Parse(line string) *LogLine {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil
	}
	pos := 0
	vals := map[string]string{}
	for ti, t := range f.tokens {
		if t.kind == tokLit {
			if !strings.HasPrefix(line[pos:], t.lit) {
				return nil
			}
			pos += len(t.lit)
			continue
		}
		var val string
		switch t.quot {
		case '"':
			v, n, ok := readQuoted(line[pos:], '"')
			if !ok {
				return nil
			}
			val, pos = v, pos+n
		case '[':
			if pos >= len(line) || line[pos] != '[' {
				return nil
			}
			v, n, ok := readUntil(line[pos+1:], ']')
			if !ok {
				return nil
			}
			val, pos = v, pos+1+n
		default:
			// 读到下一个字面量,或空白分隔的下一个字段
			end := len(line)
			if ti+1 < len(f.tokens) && f.tokens[ti+1].kind == tokLit {
				lit := f.tokens[ti+1].lit
				i := strings.Index(line[pos:], lit)
				if i < 0 {
					return nil
				}
				end = pos + i
			} else if ti+1 < len(f.tokens) && f.tokens[ti+1].quot != 0 {
				// 下一个是带引号/括号的变量,中间通常有空格
				for end > pos && end == len(line) {
					// find quote
				}
				sep := " "
				if f.tokens[ti+1].quot == '"' {
					i := strings.Index(line[pos:], " \"")
					if i < 0 {
						i = strings.Index(line[pos:], `"`)
						if i < 0 {
							return nil
						}
						end = pos + i
					} else {
						end = pos + i
					}
				} else if f.tokens[ti+1].quot == '[' {
					i := strings.Index(line[pos:], " [")
					if i < 0 {
						i = strings.Index(line[pos:], "[")
						if i < 0 {
							return nil
						}
						end = pos + i
					} else {
						end = pos + i
					}
				}
				_ = sep
			} else {
				// 最后一个未加引号字段:读到空白或行尾
				i := pos
				for i < len(line) && line[i] != ' ' {
					i++
				}
				end = i
			}
			val = line[pos:end]
			pos = end
		}
		vals[t.lit] = val
	}
	out := &LogLine{}
	if ip := vals["remote_addr"]; ip != "" && ip != "-" {
		if n := normalizeIP(ip); n != "" {
			out.IP = n
		} else {
			out.IP = ip
		}
	}
	if ts := vals["time_local"]; ts != "" {
		t, err := time.Parse("02/Jan/2006:15:04:05 -0700", ts)
		if err != nil {
			t, err = time.Parse("02/Jan/2006:15:04:05", ts)
			if err != nil {
				return nil
			}
			t = t.UTC()
		}
		out.Time = t
	} else if ts := vals["time_iso8601"]; ts != "" {
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return nil
		}
		out.Time = t
	} else {
		return nil
	}
	if st := vals["status"]; st != "" && st != "-" {
		n, err := strconv.Atoi(st)
		if err != nil {
			return nil
		}
		out.Status = n
	}
	for _, k := range []string{"body_bytes_sent", "bytes_sent"} {
		if v, ok := vals[k]; ok {
			out.Sent = parseUint(v)
			break
		}
	}
	if v, ok := vals["request_length"]; ok {
		out.Recv = parseUint(v)
		out.HasRecv = true
	}
	for _, k := range []string{"host", "http_host", "server_name"} {
		if v, ok := vals[k]; ok && v != "" && v != "-" {
			out.Host = v
			out.HasHost = true
			break
		}
	}
	return out
}

func parseUint(s string) uint64 {
	if s == "" || s == "-" {
		return 0
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func readQuoted(s string, q byte) (string, int, bool) {
	if len(s) == 0 || s[0] != q {
		return "", 0, false
	}
	var b strings.Builder
	i := 1
	for i < len(s) {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		if c == q {
			return b.String(), i + 1, true
		}
		b.WriteByte(c)
		i++
	}
	return "", 0, false
}

func readUntil(s string, end byte) (string, int, bool) {
	i := strings.IndexByte(s, end)
	if i < 0 {
		return "", 0, false
	}
	return s[:i], i + 1, true
}

func normalizeIP(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "::ffff:") {
		s = s[7:]
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

// DetectFormat 用样例行从候选格式里挑一个能完整解析的。
func DetectFormat(formats []*LogFormat, sample string) *LogFormat {
	for _, f := range formats {
		if f != nil && f.Parse(sample) != nil {
			return f
		}
	}
	return nil
}

// BuiltinFormats 按常见程度排列的内置格式。
func BuiltinFormats() []*LogFormat {
	var out []*LogFormat
	for _, name := range []string{"main", "combined", "common"} {
		f, err := CompileLogFormat(name, FormatLibrary()[name])
		if err == nil {
			out = append(out, f)
		}
	}
	return out
}

// StatusClass 2xx/3xx/4xx/5xx/other。
func StatusClass(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500 && code < 600:
		return "5xx"
	default:
		return "other"
	}
}
