package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func TestDetectFixtureTree(t *testing.T) {
	root := t.TempDir()
	// 默认路由 eth0; wt0 在 dev 里
	write(t, root, "proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n"+
		"eth0\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"+
		"eth0\t0000A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n", 0o644)
	write(t, root, "proc/net/dev", "Inter-|   Receive                                                |  Transmit\n"+
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"+
		"    lo: 100 0 0 0 0 0 0 0 100 0 0 0 0 0 0 0\n"+
		"  eth0: 1000 0 0 0 0 0 0 0 2000 0 0 0 0 0 0 0\n"+
		"docker0: 1 0 0 0 0 0 0 0 1 0 0 0 0 0 0 0\n"+
		"  vethAB: 1 0 0 0 0 0 0 0 1 0 0 0 0 0 0 0\n"+
		"   wt0: 500 0 0 0 0 0 0 0 40 0 0 0 0 0 0 0\n", 0o644)

	write(t, root, "etc/sing-box/config.json", `{
		"experimental": {"clash_api": {"external_controller": "0.0.0.0:23390", "secret": "super-secret-do-not-leak"}}
	}`, 0o600)
	write(t, root, "etc/systemd/system/sing-box.service", "[Unit]\nDescription=sb\n", 0o644)

	write(t, root, "etc/nginx/nginx.conf", `
http {
    log_format main '$remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent" "$http_x_forwarded_for"';
    include /etc/nginx/conf.d/*.conf;
}
`, 0o644)
	write(t, root, "etc/nginx/conf.d/media-lax.example.com.conf", `
server {
    server_name media-lax.example.com;
    access_log /var/log/nginx/media-lax.example.com.log main;
}
`, 0o644)
	write(t, root, "var/log/nginx/media-lax.example.com.log",
		`203.0.113.80 - - [10/Nov/2026:00:09:50 +0000] "GET / HTTP/1.1" 302 145 "-" "ua" "-"\n`, 0o644)

	write(t, root, "opt/1panel/www/conf.d/git.example.com.conf", `
server {
    server_name git.example.com;
    access_log /www/sites/git.example.com/log/access.log main;
}
`, 0o644)
	write(t, root, "opt/1panel/www/sites/git.example.com/log/access.log",
		`1.2.3.4 - - [10/Nov/2026:15:37:58 +0800] "GET / HTTP/1.1" 200 543 "-" "ua" "-"\n`, 0o644)

	write(t, root, "root/x-ui/bin/xray-linux-amd64", "#!/bin/true\n", 0o755)
	write(t, root, "root/x-ui/bin/config.json", `{
		"inbounds": [{"tag":"api","listen":"127.0.0.1","port":62789,"protocol":"dokodemo-door"}]
	}`, 0o644)

	sys := &DetectSys{Root: root, Hostname: "host-b"}
	d, err := Detect(sys, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Hostname != "host-b" {
		t.Fatalf("hn %s", d.Hostname)
	}
	names := map[string]string{}
	for _, i := range d.Interfaces {
		names[i.Name] = i.Reason
	}
	if names["eth0"] == "" || names["wt0"] == "" {
		t.Fatalf("ifaces %+v", d.Interfaces)
	}
	if _, ok := names["docker0"]; ok {
		t.Fatal("docker0 should be skipped")
	}
	if _, ok := names["lo"]; ok {
		t.Fatal("lo should be skipped")
	}
	sb := d.Collectors["singbox"]
	if !sb.Enabled || sb.Config["url"] != "http://127.0.0.1:23390" {
		t.Fatalf("singbox %+v", sb)
	}
	blob, _ := json.Marshal(d)
	if strings.Contains(string(blob), "super-secret") {
		t.Fatal("secret leaked in detect output")
	}
	site := d.Collectors["site"]
	if !site.Enabled {
		t.Fatalf("site %+v", site)
	}
	srcs := sourcesFrom(site.Config["sources"])
	var paths []string
	for _, s := range srcs {
		paths = append(paths, s.Path+"|"+s.Site)
	}
	joined := strings.Join(paths, " ")
	if !strings.Contains(joined, "media-lax.example.com") || !strings.Contains(joined, "git.example.com") {
		t.Fatalf("sources %v", paths)
	}
	nb := d.Collectors["netbird"]
	if !nb.Enabled || !strings.Contains(nb.Reason, "wt0") {
		t.Fatalf("netbird %+v", nb)
	}
	xr := d.Collectors["xray"]
	if !xr.Enabled || xr.Config["server"] != "127.0.0.1:62789" {
		t.Fatalf("xray %+v", xr)
	}

	// --skip site,xray
	d2, _ := Detect(sys, nil, []string{"site", "xray"})
	if d2.Collectors["site"].Enabled || d2.Collectors["xray"].Enabled {
		t.Fatal("skip failed")
	}
	if !d2.Collectors["singbox"].Enabled {
		t.Fatal("singbox should still be on")
	}
}
