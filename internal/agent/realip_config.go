package agent

import (
	"net/netip"
	"strings"
)

// nginxNodes parses directive boundaries, ignoring braces/comments inside strings.
// It is deliberately bounded to configuration text; it never executes nginx or reloads it.
type nginxNode struct {
	name     string
	args     []string
	children []nginxNode
}

func nginxNodes(text string) []nginxNode {
	tokens := []string{}
	var word strings.Builder
	quote := byte(0)
	flush := func() {
		if word.Len() > 0 {
			tokens = append(tokens, word.String())
			word.Reset()
		}
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '\\' && i+1 < len(text) {
			i++
			word.WriteByte(text[i])
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				word.WriteByte(c)
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if c == '#' {
			flush()
			for i < len(text) && text[i] != '\n' {
				i++
			}
			continue
		}
		if strings.ContainsRune(" \t\r\n", rune(c)) {
			flush()
			continue
		}
		if c == '{' || c == '}' || c == ';' {
			flush()
			tokens = append(tokens, string(c))
			continue
		}
		word.WriteByte(c)
	}
	flush()
	at := 0
	var parse func(int) []nginxNode
	parse = func(depth int) []nginxNode {
		out := []nginxNode{}
		if depth > 32 {
			return out
		}
		for at < len(tokens) {
			if tokens[at] == "}" {
				at++
				return out
			}
			n := nginxNode{name: tokens[at]}
			at++
			for at < len(tokens) && tokens[at] != ";" && tokens[at] != "{" && tokens[at] != "}" {
				n.args = append(n.args, tokens[at])
				at++
			}
			if at < len(tokens) && tokens[at] == "{" {
				at++
				n.children = parse(depth + 1)
			} else if at < len(tokens) && tokens[at] == ";" {
				at++
			}
			out = append(out, n)
		}
		return out
	}
	return parse(0)
}
func realDirect(nodes []nginxNode) ([]string, string) {
	var cidrs []string
	header := ""
	for _, n := range nodes {
		if len(n.args) != 1 {
			continue
		}
		v := n.args[0]
		if n.name == "set_real_ip_from" {
			if p, e := netip.ParsePrefix(v); e == nil && p.Bits() > 0 {
				cidrs = append(cidrs, v)
			} else if _, e := netip.ParseAddr(v); e == nil {
				cidrs = append(cidrs, v)
			}
		}
		if n.name == "real_ip_header" {
			header = v
		}
	}
	return cidrs, header
}
func realIPConfig(text string) ([]string, string) { return realDirect(nginxNodes(text)) }
func serverBlocks(nodes []nginxNode, inherited []string, header string) []serverBlock {
	own, h := realDirect(nodes)
	if len(own) > 0 {
		inherited = own
	}
	if h != "" {
		header = h
	}
	out := []serverBlock{}
	for _, n := range nodes {
		if n.name == "http" {
			out = append(out, serverBlocks(n.children, inherited, header)...)
		}
		if n.name != "server" || len(n.children) == 0 {
			continue
		}
		trusted, head := realDirect(n.children)
		if len(trusted) == 0 {
			trusted = inherited
		}
		if head == "" {
			head = header
		}
		b := serverBlock{TrustedProxies: trusted, IPHeader: head}
		ambiguous := false
		var read func([]nginxNode, bool)
		read = func(children []nginxNode, nested bool) {
			for _, v := range children {
				if !nested && v.name == "server_name" {
					for _, name := range v.args {
						if name != "_" && name != "localhost" {
							b.Names = append(b.Names, name)
						}
					}
				}
				if nested && (v.name == "set_real_ip_from" || v.name == "real_ip_header") {
					ambiguous = true
				}
				if v.name == "access_log" {
					ref := parseAccessLogDirective(strings.Join(v.args, " "))
					if ref.Path != "" {
						b.Logs = append(b.Logs, ref)
					}
				}
				if len(v.children) > 0 {
					read(v.children, true)
				}
			}
		}
		read(n.children, false)
		// A location override cannot certify real-IP behavior for the entire shared log.
		if ambiguous {
			b.TrustedProxies = nil
			b.IPHeader = ""
		}
		if len(b.Logs) > 0 {
			out = append(out, b)
		}
	}
	return out
}
