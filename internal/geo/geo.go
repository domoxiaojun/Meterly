// Package geo 离线 IP 归属地:DB-IP City Lite + ASN Lite(mmdb,CC BY 4.0)。
// 查询全部在本地完成,IP 绝不发往任何第三方;联网只用于从 DB-IP 下载库文件本身。
package geo

import (
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

const (
	CityFile = "dbip-city-lite.mmdb"
	ASNFile  = "dbip-asn-lite.mmdb"
	// Attribution DB-IP Lite 许可(CC BY 4.0)要求的署名。
	Attribution    = "IP Geolocation by DB-IP"
	AttributionURL = "https://db-ip.com"
)

// Info 一个 IP 的归属地。Scope 非空表示无需查库的特殊地址(内网 / CGNAT / 回环 / 保留)。
type Info struct {
	CC      string `json:"cc,omitempty"`
	Country string `json:"country,omitempty"` // 中文名优先,缺失时英文
	Region  string `json:"region,omitempty"`
	City    string `json:"city,omitempty"`
	ASN     uint   `json:"asn,omitempty"`
	Org     string `json:"org,omitempty"` // ASN 组织,作为"运营商"展示
	Scope   string `json:"scope,omitempty"`
}

// DBStatus 单个库文件状态。
type DBStatus struct {
	Loaded    bool   `json:"loaded"`
	Type      string `json:"type,omitempty"`
	BuildTime int64  `json:"build_time,omitempty"`
	Size      int64  `json:"size,omitempty"`
}

// Status 归属地子系统状态(面板展示)。
type Status struct {
	Dir        string   `json:"-"`
	City       DBStatus `json:"city"`
	ASN        DBStatus `json:"asn"`
	AutoUpdate bool     `json:"auto_update"`
	LastCheck  int64    `json:"last_check,omitempty"`
	LastOK     int64    `json:"last_ok,omitempty"`
	LastErr    string   `json:"last_err,omitempty"`
}

type cityRec struct {
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Country struct {
		ISO   string            `maxminddb:"iso_code"`
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
}

type asnRec struct {
	Number uint   `maxminddb:"autonomous_system_number"`
	Org    string `maxminddb:"autonomous_system_organization"`
}

// DB 可热替换的归属地库;未加载任何库时 Lookup 只返回特殊地址分类,不报错。
type DB struct {
	dir string

	mu   sync.RWMutex
	city *maxminddb.Reader
	asn  *maxminddb.Reader

	cmu   sync.Mutex
	cache map[netip.Addr]Info

	smu    sync.Mutex
	status Status
}

const cacheMax = 50000

// Open 打开 dir 下已有的库文件;文件不存在或损坏时对应库为空(降级为无归属地)。
func Open(dir string) *DB {
	d := &DB{dir: dir, cache: map[netip.Addr]Info{}}
	for _, name := range []string{CityFile, ASNFile} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		r, err := maxminddb.Open(p)
		if err != nil {
			d.setErr("打开 " + name + " 失败: " + err.Error())
			continue
		}
		d.swap(name, r)
	}
	return d
}

// Dir 库文件目录。
func (d *DB) Dir() string { return d.dir }

func (d *DB) swap(name string, r *maxminddb.Reader) {
	d.mu.Lock()
	var old *maxminddb.Reader
	if name == CityFile {
		old, d.city = d.city, r
	} else {
		old, d.asn = d.asn, r
	}
	d.mu.Unlock()
	if old != nil {
		old.Close()
	}
	d.cmu.Lock()
	d.cache = map[netip.Addr]Info{}
	d.cmu.Unlock()
}

// Close 关闭库文件。
func (d *DB) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.city != nil {
		d.city.Close()
		d.city = nil
	}
	if d.asn != nil {
		d.asn.Close()
		d.asn = nil
	}
}

var (
	cgnat    = netip.MustParsePrefix("100.64.0.0/10")
	reserved = []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8::/32"),
	}
)

// classify 特殊地址:返回中文类别;普通公网地址返回空串。文档保留网段只有在库里查不到时才标"保留"。
func classify(a netip.Addr) string {
	switch {
	case a.IsLoopback():
		return "回环"
	case a.IsPrivate():
		return "内网"
	case cgnat.Contains(a):
		return "CGNAT/NetBird"
	case a.IsLinkLocalUnicast(), a.IsUnspecified(), a.IsMulticast():
		return "保留"
	}
	return ""
}

// Lookup 查询归属地。非法 IP(如 "~other")返回空 Info。
func (d *DB) Lookup(ip string) Info {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return Info{}
	}
	a = a.Unmap()
	if s := classify(a); s != "" {
		return Info{Scope: s}
	}
	d.cmu.Lock()
	if v, ok := d.cache[a]; ok {
		d.cmu.Unlock()
		return v
	}
	d.cmu.Unlock()
	var out Info
	d.mu.RLock()
	if d.city != nil {
		var c cityRec
		if res := d.city.Lookup(a); res.Found() && res.Decode(&c) == nil {
			out.CC = c.Country.ISO
			out.Country = pick(c.Country.Names)
			out.City = pick(c.City.Names)
			if len(c.Subdivisions) > 0 {
				out.Region = pick(c.Subdivisions[0].Names)
			}
		}
	}
	if d.asn != nil {
		var r asnRec
		if res := d.asn.Lookup(a); res.Found() && res.Decode(&r) == nil {
			out.ASN, out.Org = r.Number, r.Org
		}
	}
	d.mu.RUnlock()
	if out.CC == "" && out.ASN == 0 {
		for _, p := range reserved {
			if p.Contains(a) {
				out.Scope = "保留"
			}
		}
	}
	d.cmu.Lock()
	if len(d.cache) >= cacheMax {
		d.cache = map[netip.Addr]Info{}
	}
	d.cache[a] = out
	d.cmu.Unlock()
	return out
}

func pick(names map[string]string) string {
	if v := names["zh-CN"]; v != "" {
		return v
	}
	return names["en"]
}

// Status 当前库与更新状态。
func (d *DB) Status() Status {
	d.smu.Lock()
	st := d.status
	d.smu.Unlock()
	st.Dir = d.dir
	d.mu.RLock()
	st.City = dbStatus(d.city, filepath.Join(d.dir, CityFile))
	st.ASN = dbStatus(d.asn, filepath.Join(d.dir, ASNFile))
	d.mu.RUnlock()
	return st
}

func dbStatus(r *maxminddb.Reader, path string) DBStatus {
	if r == nil {
		return DBStatus{}
	}
	s := DBStatus{Loaded: true, Type: r.Metadata.DatabaseType, BuildTime: r.Metadata.BuildTime().Unix()}
	if fi, err := os.Stat(path); err == nil {
		s.Size = fi.Size()
	}
	return s
}

func (d *DB) buildTime(name string) time.Time {
	d.mu.RLock()
	defer d.mu.RUnlock()
	r := d.asn
	if name == CityFile {
		r = d.city
	}
	if r == nil {
		return time.Time{}
	}
	return r.Metadata.BuildTime()
}

func (d *DB) setErr(msg string) {
	d.smu.Lock()
	d.status.LastErr = msg
	d.smu.Unlock()
}
