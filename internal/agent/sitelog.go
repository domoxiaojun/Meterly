package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

const (
	defaultSiteTopIPs   = 100
	defaultMaxReadBytes = 32 << 20 // 每轮每个文件最多读 32 MiB,其余下一分钟继续
)

// SiteConfig 站点日志采集。
type SiteConfig struct {
	Enabled      bool              `json:"enabled"`
	Sources      []SiteSource      `json:"sources,omitempty"`
	Formats      map[string]string `json:"formats,omitempty"` // 名 → log_format 定义
	AllowDirs    []string          `json:"allow_dirs,omitempty"`
	TopIPs       int               `json:"top_ips,omitempty"` // 每站点每分钟最多上报的客户端 IP,其余进 ~other
	MaxReadBytes int64             `json:"max_read_bytes,omitempty"`
	FromStart    bool              `json:"from_start,omitempty"` // 测试用:首次从文件头读;默认从末尾(只采新流量)
}

func (c *SiteConfig) applyDefaults() {
	if c.TopIPs == 0 {
		c.TopIPs = defaultSiteTopIPs
	}
	if c.MaxReadBytes == 0 {
		c.MaxReadBytes = defaultMaxReadBytes
	}
	if len(c.AllowDirs) == 0 {
		c.AllowDirs = []string{
			"/var/log/nginx",
			"/var/log/openresty",
			"/opt/1panel/www",
			"/opt/1panel/apps/openresty",
		}
	}
}

// LogOffset 一个日志文件的断点。
type LogOffset struct {
	Path   string `json:"path"`
	Inode  uint64 `json:"inode"`
	Offset int64  `json:"offset"`
	Dev    uint64 `json:"dev,omitempty"`
}

// SiteAcc 站点日志累计器。
type SiteAcc struct {
	cfg     SiteConfig
	offsets map[string]LogOffset
	formats map[string]*LogFormat // 名或 path → 编译格式
	now     func() time.Time
	// 可注入
	Open  func(path string) (*os.File, error)
	Stat  func(path string) (os.FileInfo, error)
	Allow []string
}

func NewSiteAcc(cfg SiteConfig, offsets map[string]LogOffset) *SiteAcc {
	cfg.applyDefaults()
	if offsets == nil {
		offsets = map[string]LogOffset{}
	}
	fmts := map[string]*LogFormat{}
	for name, raw := range FormatLibrary() {
		if f, err := CompileLogFormat(name, raw); err == nil {
			fmts[name] = f
		}
	}
	for name, raw := range cfg.Formats {
		if f, err := CompileLogFormat(name, raw); err == nil {
			fmts[name] = f
		}
	}
	for i, s := range cfg.Sources {
		if s.FormatDef != "" {
			if f, err := CompileLogFormat(s.Format, s.FormatDef); err == nil {
				fmts["src:"+s.Path] = f
				cfg.Sources[i].Format = "src:" + s.Path
			}
		}
	}
	return &SiteAcc{
		cfg:     cfg,
		offsets: offsets,
		formats: fmts,
		now:     time.Now,
		Open:    os.Open,
		Stat:    os.Stat,
		Allow:   cfg.AllowDirs,
	}
}

func (a *SiteAcc) Offsets() map[string]LogOffset { return a.offsets }

// allowed 解析符号链接后路径必须落在 AllowDirs 内,拒绝逃逸。
func (a *SiteAcc) allowed(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// 文件尚不存在时允许用绝对路径做前缀检查(下一轮再读)
		resolved = abs
	}
	resolved = filepath.Clean(resolved)
	for _, dir := range a.Allow {
		d, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if r, err := filepath.EvalSymlinks(d); err == nil {
			d = r
		}
		d = filepath.Clean(d)
		if resolved == d || strings.HasPrefix(resolved, d+string(os.PathSeparator)) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("日志路径不在允许目录内: %s", path)
}

type siteAggKey struct {
	ts, site, dim2 string // dim2 = IP 或 状态类; kind 分开存
}

type siteBucket struct {
	rx, tx, req uint64
}

type siteTickStats struct {
	Files, Lines, Used, Failed, Skipped int
	Bytes                               int64
}

// Collect 从断点续读各日志,按日志时间戳的分钟聚合。首次见到的文件默认从末尾开始(不回放历史)。
func (a *SiteAcc) Collect() (recs []Counter, stats siteTickStats, err error) {
	type acc struct {
		// per (minute, site, ip) bytes
		ip map[[3]string]*[2]uint64 // ts|site|ip → rx,tx  (ts as string of unix)
		// per (minute, site, class) requests
		req map[[3]string]uint64
	}
	ip := map[[3]string]*[2]uint64{}
	req := map[[3]string]uint64{}

	for _, src := range a.cfg.Sources {
		if src.Path == "" || isSkippableLog(src.Path) {
			continue
		}
		resolved, aerr := a.allowed(src.Path)
		if aerr != nil {
			stats.Skipped++
			continue
		}
		n, u, f, ferr := a.readOne(src, resolved, ip, req)
		stats.Files++
		stats.Lines += n
		stats.Used += u
		stats.Failed += f
		if ferr != nil && !os.IsNotExist(ferr) {
			// 单个文件失败不中断其它
			if err == nil {
				err = ferr
			}
		}
	}

	// 每站点每分钟截断 Top-N IP
	type siteMin struct{ site, ts string }
	totals := map[siteMin][]struct {
		ip     string
		rx, tx uint64
	}{}
	for k, v := range ip {
		sm := siteMin{site: k[1], ts: k[0]}
		totals[sm] = append(totals[sm], struct {
			ip     string
			rx, tx uint64
		}{k[2], v[0], v[1]})
	}
	emitIP := map[[3]string]*[2]uint64{}
	topN := a.cfg.TopIPs
	for sm, list := range totals {
		// 简单选择排序取前 N
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				if list[j].rx+list[j].tx > list[i].rx+list[i].tx ||
					(list[j].rx+list[j].tx == list[i].rx+list[i].tx && list[j].ip < list[i].ip) {
					list[i], list[j] = list[j], list[i]
				}
			}
		}
		var orx, otx uint64
		for i, it := range list {
			if i < topN {
				emitIP[[3]string{sm.ts, sm.site, it.ip}] = &[2]uint64{it.rx, it.tx}
			} else {
				orx += it.rx
				otx += it.tx
			}
		}
		if orx > 0 || otx > 0 {
			emitIP[[3]string{sm.ts, sm.site, proto.DimOther}] = &[2]uint64{orx, otx}
		}
	}

	add := func(kind, ts, d1, d2 string, rx, tx uint64) {
		sec, _ := time.Parse(time.RFC3339, ts)
		// ts 存 unix 秒字符串更稳
		u, _ := parseUnix(ts)
		if u == 0 && !sec.IsZero() {
			u = sec.Unix()
		}
		recs = append(recs, Counter{Kind: kind, Key: d1, Key2: d2, RX: rx, TX: tx, TS: u})
	}
	_ = add
	for k, v := range emitIP {
		u, _ := parseUnix(k[0])
		recs = append(recs, Counter{Kind: proto.KindSite, Key: k[1], Key2: k[2], RX: v[0], TX: v[1], TS: u})
	}
	for k, n := range req {
		u, _ := parseUnix(k[0])
		recs = append(recs, Counter{Kind: proto.KindSiteReq, Key: k[1], Key2: k[2], RX: n, TX: 0, TS: u})
	}
	return recs, stats, err
}

func parseUnix(s string) (int64, error) {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not unix")
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

func inodeOf(fi os.FileInfo) (dev, ino uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), uint64(st.Ino)
	}
	return 0, 0
}

func (a *SiteAcc) resolveFormat(src SiteSource, sample string) *LogFormat {
	if f := a.formats["src:"+src.Path]; f != nil {
		return f
	}
	if src.Format != "" {
		if f := a.formats[src.Format]; f != nil {
			if sample == "" || f.Parse(sample) != nil {
				return f
			}
		}
		if raw, ok := a.cfg.Formats[src.Format]; ok {
			if f, err := CompileLogFormat(src.Format, raw); err == nil {
				a.formats[src.Format] = f
				return f
			}
		}
	}
	var cands []*LogFormat
	if src.Format != "" && a.formats[src.Format] != nil {
		cands = append(cands, a.formats[src.Format])
	}
	// main 可能是 combined 或 combined+$xff,两种都试
	cands = append(cands, BuiltinFormats()...)
	for _, f := range a.formats {
		cands = append(cands, f)
	}
	if sample != "" {
		if f := DetectFormat(cands, sample); f != nil {
			a.formats[src.Path] = f
			return f
		}
	}
	if f := a.formats["combined"]; f != nil {
		return f
	}
	return nil
}

func (a *SiteAcc) readOne(src SiteSource, path string, ip map[[3]string]*[2]uint64, req map[[3]string]uint64) (lines, used, failed int, err error) {
	fi, err := a.Stat(path)
	if err != nil {
		return 0, 0, 0, err
	}
	dev, ino := inodeOf(fi)
	size := fi.Size()
	prev, had := a.offsets[src.Path]
	var offset int64
	switch {
	case !had:
		if a.cfg.FromStart {
			offset = 0
		} else {
			offset = size // 首次:从末尾开始,不回放历史
		}
	case prev.Inode == ino && prev.Dev == dev && size >= prev.Offset:
		offset = prev.Offset
	case prev.Inode == ino && prev.Dev == dev && size < prev.Offset:
		offset = 0 // 截断(copytruncate)
	default:
		offset = 0 // inode 变了:新文件从头读(旧内容若已读过不会再出现在此路径)
	}
	f, err := a.Open(path)
	if err != nil {
		return 0, 0, 0, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, 0, 0, err
	}

	var fmt *LogFormat
	budget := a.cfg.MaxReadBytes
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for budget > 0 {
		n, rerr := f.Read(tmp)
		if n > 0 {
			budget -= int64(n)
			buf = append(buf, tmp[:n]...)
			for {
				i := indexByte(buf, '\n')
				if i < 0 {
					break
				}
				line := string(buf[:i])
				buf = buf[i+1:]
				offset += int64(i + 1)
				if line == "" {
					continue
				}
				lines++
				if fmt == nil {
					fmt = a.resolveFormat(src, line)
				}
				if fmt == nil {
					failed++
					continue
				}
				lg := fmt.Parse(line)
				if lg == nil {
					failed++
					continue
				}
				site := src.Site
				if lg.HasHost && lg.Host != "" {
					site = lg.Host
				}
				if site == "" {
					site = "_default"
				}
				minute := lg.Time.UTC().Unix() / 60 * 60
				ts := itoa(minute)
				ipk := [3]string{ts, site, lg.IP}
				if lg.IP == "" {
					ipk[2] = "~unknown"
				}
				v := ip[ipk]
				if v == nil {
					v = &[2]uint64{}
					ip[ipk] = v
				}
				v[0] += lg.Recv
				v[1] += lg.Sent
				req[[3]string{ts, site, StatusClass(lg.Status)}]++
				used++
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			err = rerr
			break
		}
		if n == 0 {
			break
		}
	}
	// 不把不完整行计入 offset(下一轮重读)
	a.offsets[src.Path] = LogOffset{Path: src.Path, Inode: ino, Dev: dev, Offset: offset}
	return lines, used, failed, err
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var neg bool
	if n < 0 {
		neg, n = true, -n
	}
	var b [32]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
