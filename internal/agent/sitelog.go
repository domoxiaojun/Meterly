package agent

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

const defaultMaxReadBytes = 8 << 20
const maxSiteBuckets = 10000

type SiteConfig struct {
	Enabled        bool              `json:"enabled"`
	Sources        []SiteSource      `json:"sources,omitempty"`
	Formats        map[string]string `json:"formats,omitempty"`
	AllowDirs      []string          `json:"allow_dirs,omitempty"`
	TopIPs         int               `json:"-"` // no longer truncates data
	MaxReadBytes   int64             `json:"max_read_bytes,omitempty"`
	FromStart      bool              `json:"from_start,omitempty"`
	TrustedProxies []string          `json:"trusted_proxies,omitempty"`
	IPHeader       string            `json:"ip_header,omitempty"`
}

func (c *SiteConfig) applyDefaults() {
	if c.MaxReadBytes <= 0 {
		c.MaxReadBytes = defaultMaxReadBytes
	}
	if len(c.AllowDirs) == 0 {
		c.AllowDirs = []string{"/var/log/nginx", "/var/log/openresty", "/opt/1panel/www", "/opt/1panel/apps/openresty"}
	}
}

type LogOffset struct {
	Path        string `json:"path"`
	Inode       uint64 `json:"inode"`
	Offset      int64  `json:"offset"`
	Dev         uint64 `json:"dev,omitempty"`
	SourcePath  string `json:"source_path,omitempty"`
	Retired     bool   `json:"retired,omitempty"`
	EmptyReads  int    `json:"empty_reads,omitempty"`
	DiscardLine bool   `json:"discard_line,omitempty"`
}
type SiteAcc struct {
	cfg     SiteConfig
	offsets map[string]LogOffset
	formats map[string]*LogFormat
	files   map[string]*os.File
	now     func() time.Time
	Open    func(string) (*os.File, error)
	Stat    func(string) (os.FileInfo, error)
	Allow   []string
	next    int
}

func NewSiteAcc(cfg SiteConfig, offsets map[string]LogOffset) *SiteAcc {
	cfg.applyDefaults()
	if offsets == nil {
		offsets = map[string]LogOffset{}
	}
	fmts := map[string]*LogFormat{}
	for n, r := range FormatLibrary() {
		if f, e := CompileLogFormat(n, r); e == nil {
			fmts[n] = f
		}
	}
	for n, r := range cfg.Formats {
		if f, e := CompileLogFormat(n, r); e == nil {
			fmts[n] = f
		}
	}
	for _, s := range cfg.Sources {
		if s.FormatDef != "" {
			if f, e := CompileLogFormat(s.Format, s.FormatDef); e == nil {
				fmts["src:"+s.Path] = f
			}
		}
	}
	return &SiteAcc{cfg: cfg, offsets: offsets, formats: fmts, files: map[string]*os.File{}, now: time.Now, Open: os.Open, Stat: os.Stat, Allow: cfg.AllowDirs}
}
func (a *SiteAcc) Offsets() map[string]LogOffset { return a.offsets }
func (a *SiteAcc) Close() {
	for k, f := range a.files {
		f.Close()
		delete(a.files, k)
	}
}
func (a *SiteAcc) allowed(path string) (string, error) {
	abs, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	resolved, e := filepath.EvalSymlinks(abs)
	if e != nil {
		return "", e
	}
	for _, d := range a.Allow {
		d, _ = filepath.Abs(d)
		if r, e := filepath.EvalSymlinks(d); e == nil {
			d = r
		}
		if resolved == d || strings.HasPrefix(resolved, d+string(os.PathSeparator)) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("日志路径不在允许目录内: %s", path)
}

type siteTickStats struct {
	Files, Lines, Used, Failed, Skipped, Unresolved, Backlogged int
	Bytes                                                       int64
}
type siteKey struct {
	minute           int64
	site, ip, origin string
	status           int
	rxKnown          bool
}
type siteValue struct{ rx, tx, requests uint64 }

func (a *SiteAcc) resolveFormat(src SiteSource, sample string) *LogFormat {
	if f := a.formats["src:"+src.Path]; f != nil {
		return f
	}
	if f := a.formats[src.Format]; f != nil && f.Parse(sample) != nil {
		return f
	}
	if f := DetectFormat(BuiltinFormats(), sample); f != nil {
		return f
	}
	for _, f := range a.formats {
		if f.Parse(sample) != nil {
			return f
		}
	}
	return nil
}
func inodeOf(fi os.FileInfo) (uint64, uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), uint64(st.Ino)
	}
	return 0, 0
}
func SourceID(path string) string {
	return fmt.Sprintf("source-%x", sha256.Sum256([]byte(filepath.Clean(path))))[:23]
}
func (a *SiteAcc) oldFile(cursor LogOffset) *os.File {
	// Recover a renamed inode after an agent restart; bounded to the same allowlisted directory.
	entries, e := os.ReadDir(filepath.Dir(cursor.Path))
	if e != nil {
		return nil
	}
	for i, en := range entries {
		if i >= 2048 {
			break
		}
		if en.IsDir() {
			continue
		}
		p := filepath.Join(filepath.Dir(cursor.Path), en.Name())
		resolved, e := a.allowed(p)
		if e != nil {
			continue
		}
		fi, e := a.Stat(resolved)
		if e != nil || !fi.Mode().IsRegular() {
			continue
		}
		dev, ino := inodeOf(fi)
		if dev == cursor.Dev && ino == cursor.Inode {
			f, e := a.Open(resolved)
			if e == nil {
				return f
			}
		}
	}
	return nil
}
func (a *SiteAcc) Collect() (recs []Counter, stats siteTickStats, err error) {
	agg := map[siteKey]*siteValue{}
	budget := a.cfg.MaxReadBytes
	seen := map[[2]uint64]bool{}
	sources := a.cfg.Sources
	if len(sources) == 0 {
		return nil, stats, nil
	}
	start := a.next % len(sources)
	a.next = (start + 1) % len(sources)
	recordError := func(e error) {
		if e != nil && err == nil {
			err = e
		}
	}
	for i := 0; i < len(sources); i++ {
		src := sources[(start+i)%len(sources)]
		if budget <= 0 || len(agg) >= maxSiteBuckets {
			stats.Backlogged++
			continue
		}
		// Drain retired files first, including files still written briefly after rename.
		for key, c := range a.offsets {
			if !c.Retired || c.SourcePath != src.Path {
				continue
			}
			f := a.files[key]
			if f == nil {
				f = a.oldFile(c)
				if f != nil {
					a.files[key] = f
				}
			}
			if f == nil {
				recordError(fmt.Errorf("轮转日志尚有未确认断点但文件不可访问: %s", src.Path))
				continue
			}
			before := c.Offset
			e := a.readFile(src, f, &c, agg, &budget, &stats)
			recordError(e)
			if c.Offset == before && budget > 0 {
				c.EmptyReads++
			} else {
				c.EmptyReads = 0
			}
			a.offsets[key] = c
			if c.EmptyReads >= 2 {
				f.Close()
				delete(a.files, key)
				delete(a.offsets, key)
			}
		}
		resolved, e := a.allowed(src.Path)
		if e != nil {
			stats.Skipped++
			recordError(e)
			continue
		}
		f, e := a.Open(resolved)
		if e != nil {
			recordError(e)
			continue
		}
		fi, e := f.Stat()
		if e != nil || !fi.Mode().IsRegular() {
			f.Close()
			recordError(errors.New("日志必须为普通文件"))
			continue
		}
		dev, ino := inodeOf(fi)
		if seen[[2]uint64{dev, ino}] {
			f.Close()
			continue
		}
		seen[[2]uint64{dev, ino}] = true
		c, had := a.offsets[src.Path]
		if had && (c.Dev != dev || c.Inode != ino) {
			key := fmt.Sprintf("retired:%s:%d:%d", src.Path, c.Dev, c.Inode)
			c.Retired = true
			c.SourcePath = src.Path
			old := a.files[src.Path]
			if old == nil {
				old = a.oldFile(c)
			}
			if old != nil {
				a.files[key] = old
				recordError(a.readFile(src, old, &c, agg, &budget, &stats))
			} else {
				recordError(fmt.Errorf("无法补读轮转日志: %s", src.Path))
			}
			a.offsets[key] = c
			delete(a.files, src.Path)
			c = LogOffset{Path: resolved, SourcePath: src.Path, Dev: dev, Inode: ino}
		} else if !had {
			c = LogOffset{Path: resolved, SourcePath: src.Path, Dev: dev, Inode: ino}
			if !a.cfg.FromStart {
				c.Offset = fi.Size()
			}
		} else if fi.Size() < c.Offset {
			c.Offset = 0
			c.DiscardLine = false
		}
		if previous := a.files[src.Path]; previous != nil {
			previous.Close()
		}
		a.files[src.Path] = f
		stats.Files++
		recordError(a.readFile(src, f, &c, agg, &budget, &stats))
		a.offsets[src.Path] = c
		if fi, e := f.Stat(); e == nil && fi.Size() > c.Offset {
			stats.Backlogged++
		}
	}
	keys := make([]siteKey, 0, len(agg))
	for k := range agg {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.minute != b.minute {
			return a.minute < b.minute
		}
		if a.site != b.site {
			return a.site < b.site
		}
		if a.ip != b.ip {
			return a.ip < b.ip
		}
		if a.status != b.status {
			return a.status < b.status
		}
		if a.origin != b.origin {
			return a.origin < b.origin
		}
		return !a.rxKnown && b.rxKnown
	})
	for _, k := range keys {
		v := agg[k]
		recs = append(recs, Counter{Kind: proto.KindSite, Key: k.site, Key2: k.ip, TS: k.minute, RX: v.rx, TX: v.tx, Site: &proto.SiteMetrics{Status: k.status, Requests: v.requests, RXKnown: k.rxKnown, IPSource: k.origin}})
	}
	return recs, stats, err
}
func (a *SiteAcc) readFile(src SiteSource, f *os.File, c *LogOffset, agg map[siteKey]*siteValue, budget *int64, stats *siteTickStats) error {
	if *budget <= 0 {
		return nil
	}
	if _, e := f.Seek(c.Offset, io.SeekStart); e != nil {
		return e
	}
	r := bufio.NewReaderSize(io.LimitReader(f, *budget), 64<<10)
	for *budget > 0 && len(agg) < maxSiteBuckets {
		line, e := r.ReadSlice('\n')
		*budget -= int64(len(line))
		stats.Bytes += int64(len(line))
		if errors.Is(e, bufio.ErrBufferFull) {
			c.Offset += int64(len(line))
			if !c.DiscardLine {
				stats.Failed++
			}
			c.DiscardLine = true
			continue
		}
		if e != nil {
			if e == io.EOF {
				return nil
			}
			return e
		}
		c.Offset += int64(len(line))
		stats.Lines++
		if c.DiscardLine {
			c.DiscardLine = false
			continue
		}
		text := strings.TrimSpace(string(line))
		if text == "" {
			continue
		}
		var lg *LogLine
		if strings.HasPrefix(text, "{") {
			lg = parseJSONLog(text)
		} else if format := a.resolveFormat(src, text); format != nil {
			lg = format.Parse(text)
		}
		if lg == nil {
			stats.Failed++
			continue
		}
		site := src.Site
		if lg.HasHost && lg.Host != "" {
			site = lg.Host
		}
		site = NormalizeSite(site)
		if site == "" || site == "_default" {
			site = SourceID(src.Path)
		}
		ip, origin := ResolveClientIP(lg, src, a.cfg)
		if origin == "unresolved" {
			stats.Unresolved++
		}
		k := siteKey{minute: lg.Time.Unix() / 60 * 60, site: site, ip: ip, status: lg.Status, rxKnown: lg.HasRecv, origin: origin}
		v := agg[k]
		if v == nil {
			v = &siteValue{}
			agg[k] = v
		}
		v.rx += lg.Recv
		v.tx += lg.Sent
		v.requests++
		stats.Used++
	}
	return nil
}
func parseUnix(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }
func itoa(n int64) string               { return strconv.FormatInt(n, 10) }
func indexByte(b []byte, c byte) int    { return strings.IndexByte(string(b), c) }
