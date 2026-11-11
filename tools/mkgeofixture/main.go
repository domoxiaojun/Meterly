// mkgeofixture 生成 internal/geo/testdata 下的小型测试 mmdb(结构同 DB-IP City/ASN Lite)。
// 独立 module,mmdbwriter 不进入主程序依赖。用法:cd tools/mkgeofixture && go run . ../../internal/geo/testdata
// 只使用文档保留网段(RFC 5737 / RFC 3849)与少量公开 anycast 地址,数据为虚构。
package main

import (
	"log"
	"net"
	"os"
	"path/filepath"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
)

type ent struct {
	cidr, cc, zh, en, region, city string
	asn                            uint32
	org                            string
}

var ents = []ent{
	{"192.0.2.0/24", "JP", "日本", "Japan", "Tokyo", "Tokyo", 64500, "Example Telecom JP"},
	{"198.51.100.0/24", "CN", "中国", "China", "Guangdong", "Shenzhen", 64501, "Example Mobile CN"},
	{"203.0.113.0/25", "US", "美国", "United States", "California", "Los Angeles", 64502, "Example Cloud US"},
	{"203.0.113.128/25", "SG", "新加坡", "Singapore", "Singapore", "Singapore", 64503, "Example Net SG"},
	{"2001:db8::/32", "DE", "德国", "Germany", "Hesse", "Frankfurt am Main", 64504, "Example IPv6 DE"},
}

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	city, err := mmdbwriter.New(mmdbwriter.Options{DatabaseType: "DBIP-City-Lite", RecordSize: 24, IncludeReservedNetworks: true,
		Description: map[string]string{"en": "Meterly test fixture (fictional data)"}})
	must(err)
	asn, err := mmdbwriter.New(mmdbwriter.Options{DatabaseType: "DBIP-ASN-Lite (compat=GeoLite2-ASN)", RecordSize: 24, IncludeReservedNetworks: true,
		Description: map[string]string{"en": "Meterly test fixture (fictional data)"}})
	must(err)
	for _, e := range ents {
		_, n, err := net.ParseCIDR(e.cidr)
		must(err)
		must(city.Insert(n, mmdbtype.Map{
			"city":         mmdbtype.Map{"names": mmdbtype.Map{"en": mmdbtype.String(e.city)}},
			"country":      mmdbtype.Map{"iso_code": mmdbtype.String(e.cc), "names": mmdbtype.Map{"en": mmdbtype.String(e.en), "zh-CN": mmdbtype.String(e.zh)}},
			"subdivisions": mmdbtype.Slice{mmdbtype.Map{"names": mmdbtype.Map{"en": mmdbtype.String(e.region)}}},
		}))
		must(asn.Insert(n, mmdbtype.Map{
			"autonomous_system_number":       mmdbtype.Uint32(e.asn),
			"autonomous_system_organization": mmdbtype.String(e.org),
		}))
	}
	for name, w := range map[string]*mmdbwriter.Tree{"dbip-city-lite.mmdb": city, "dbip-asn-lite.mmdb": asn} {
		f, err := os.Create(filepath.Join(dir, name))
		must(err)
		_, err = w.WriteTo(f)
		must(err)
		must(f.Close())
	}
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
