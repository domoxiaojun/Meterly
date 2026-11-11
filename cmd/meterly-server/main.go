// meterly-server:分布式流量统计中心(ingest + SQLite + 中文面板)。
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	_ "time/tzdata" // scratch 镜像内没有 /usr/share/zoneinfo

	"github.com/domoxiaojun/Meterly/internal/geo"
	"github.com/domoxiaojun/Meterly/internal/human"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"github.com/domoxiaojun/Meterly/internal/server"
	"github.com/domoxiaojun/Meterly/internal/store"
	"github.com/domoxiaojun/Meterly/internal/version"
	"github.com/domoxiaojun/Meterly/web"
)

const usage = `用法:
  meterly-server serve  [-config server.json]
  meterly-server host add    NAME [-tz Asia/Singapore] [-reset-day 9] [-billing both|tx|rx|max] [-iface eth0] [-quota 2TiB] [-note 说明]
  meterly-server host rotate NAME     生成新 token(旧 token 立即失效)
  meterly-server host revoke NAME     吊销 token(保留历史数据)
  meterly-server host set    NAME [同 add 的选项]
  meterly-server host list
  meterly-server enroll create -host NAME [-ttl 24h] [-uses 1] [-reissue] [-note 说明]
  meterly-server enroll list
  meterly-server enroll delete ID
  meterly-server import vnstat -host NAME -file vnstat.json [-ifaces eth0,wt0] [-before UNIX秒]
  meterly-server check  vnstat -host NAME -file vnstat.json [-ifaces eth0]   与 vnstat 一致性自检(只读)
  meterly-server backup -out /data/backups/ts-YYYYMMDD.db                    SQLite 在线一致性备份
  meterly-server geo update                                                  立即下载/更新 DB-IP 归属地库
  meterly-server healthcheck [-timeout 3s]                                   本机健康检查(容器 HEALTHCHECK 用)
  meterly-server version
所有子命令都接受 -config(缺省读环境变量 METERLY_CONFIG,回退 TRAFFICSTAT_CONFIG);token 只在 add/rotate 时向 stdout 输出一次,请立即保存到 agent 的 token 文件。`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	syscall.Umask(0o077) // 数据库 / WAL / 备份 / 归属地库只给本用户读写(容器里没有 systemd 的 UMask=)
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:], log)
	case "host":
		err = hostCmd(os.Args[2:])
	case "enroll":
		err = enrollCmd(os.Args[2:])
	case "import":
		err = importCmd(os.Args[2:])
	case "check":
		err = checkCmd(os.Args[2:])
	case "backup":
		err = backupCmd(os.Args[2:])
	case "geo":
		err = geoCmd(os.Args[2:], log)
	case "healthcheck":
		err = healthcheck(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("meterly-server", version.String())
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func openStore(cfgPath string) (*server.Config, *store.Store, error) {
	cfg, err := server.LoadConfig(server.ConfigPath(cfgPath))
	if err != nil {
		return nil, nil, err
	}
	st, err := store.Open(cfg.DBPath)
	return cfg, st, err
}

func serve(args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", "", "配置文件")
	_ = fs.Parse(args)
	cfg, st, err := openStore(*cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	srv := server.New(cfg, st, web.FS(), log)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go srv.RunMaintenance(ctx, 10*time.Minute)
	gs := srv.Geo().Status()
	log.Info("归属地库", "dir", cfg.Geo.Dir, "city", gs.City.Loaded, "asn", gs.ASN.Loaded, "auto_update", cfg.Geo.AutoUpdateOn())
	if cfg.Geo.AutoUpdateOn() {
		u := &geo.Updater{DB: srv.Geo(), BaseURL: cfg.Geo.BaseURL, Log: log}
		go u.Run(ctx, time.Duration(cfg.Geo.CheckHours)*time.Hour)
	}
	hs := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 60 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	if !cfg.Loopback() {
		log.Warn("监听非回环地址,请确认仅在内网(NetBird)或反代后暴露", "listen", cfg.Listen)
	}
	log.Info("meterly-server 启动", "version", version.Version, "listen", cfg.Listen, "db", cfg.DBPath, "panel_auth", cfg.PanelAuth())
	if cfg.TLSCert != "" {
		err = hs.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
	} else {
		err = hs.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func hostCmd(args []string) error {
	if len(args) < 1 {
		return errors.New(usage)
	}
	sub, rest := args[0], args[1:]
	var name string
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		name, rest = rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("host "+sub, flag.ExitOnError)
	cfgPath := fs.String("config", "", "配置文件")
	tz := fs.String("tz", "", "计费时区(默认 Asia/Singapore)")
	rd := fs.Int("reset-day", 0, "计费重置日(默认 9)")
	bill := fs.String("billing", "", "计费口径 both|tx|rx|max(默认 both)")
	iface := fs.String("iface", "", "计费网卡(默认 eth0)")
	quota := fs.String("quota", "", "月配额,如 2TiB")
	note := fs.String("note", "", "备注")
	_ = fs.Parse(rest)
	_, st, err := openStore(*cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	q, err := human.ParseBytes(*quota)
	if err != nil {
		return err
	}
	opts := store.HostOpts{TZ: *tz, ResetDay: *rd, BillingMode: *bill, BillingIface: *iface, QuotaBytes: q, Note: *note}
	need := func() error {
		if name == "" {
			return errors.New("缺少主机名")
		}
		return nil
	}
	switch sub {
	case "add":
		if err := need(); err != nil {
			return err
		}
		tok, err := st.CreateHost(ctx, name, opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "已创建主机 %s。下面一行是它的 token,只显示这一次:\n", name)
		fmt.Println(tok)
	case "rotate":
		if err := need(); err != nil {
			return err
		}
		tok, err := st.RotateToken(ctx, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "主机 %s 的新 token(旧 token 已失效),只显示这一次:\n", name)
		fmt.Println(tok)
	case "revoke":
		if err := need(); err != nil {
			return err
		}
		if err := st.RevokeHost(ctx, name); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "已吊销 %s 的 token(历史数据保留)\n", name)
	case "set":
		if err := need(); err != nil {
			return err
		}
		return st.UpdateHost(ctx, name, opts)
	case "list":
		hs, err := st.Hosts(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "名称\t状态\t时区\t重置日\t口径\t网卡\t配额\t最近上报\t版本")
		for _, h := range hs {
			state := "启用"
			if h.RevokedAt > 0 {
				state = "已吊销"
			}
			seen := "-"
			if h.LastSeen > 0 {
				seen = time.Unix(h.LastSeen, 0).In(time.FixedZone("SGT", 8*3600)).Format("01-02 15:04 SGT")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%d\t%s\t%s\n", h.Name, state, h.TZ, h.ResetDay, h.BillingMode, h.BillingIface, h.QuotaBytes, seen, h.AgentVersion)
		}
		return tw.Flush()
	default:
		return errors.New(usage)
	}
	return nil
}

func enrollCmd(args []string) error {
	if len(args) < 1 {
		return errors.New(usage)
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("enroll "+sub, flag.ExitOnError)
	cfgPath := fs.String("config", "", "配置文件")
	host := fs.String("host", "", "绑定的主机名")
	ttl := fs.Duration("ttl", 24*time.Hour, "有效期")
	uses := fs.Int("uses", 1, "可核销次数")
	reissue := fs.Bool("reissue", false, "允许给已存在的主机换发 token")
	note := fs.String("note", "", "备注")
	_ = fs.Parse(rest)
	_, st, err := openStore(*cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	switch sub {
	case "create":
		if *host == "" {
			return errors.New("需要 -host")
		}
		code, rec, err := st.CreateEnroll(ctx, *host, store.EnrollOpts{TTL: *ttl, MaxUses: *uses, Reissue: *reissue, Note: *note}, time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "已生成注册码,绑定主机 %s,有效至 %s,次数 %d。下面一行只显示这一次:\n",
			rec.HostName, time.Unix(rec.ExpiresAt, 0).In(time.FixedZone("SGT", 8*3600)).Format("2006-01-02 15:04 MST"), rec.MaxUses)
		fmt.Println(code)
	case "list":
		list, err := st.ListEnroll(ctx, time.Now())
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\t主机\t有效至\t次数\t换发\t状态")
		for _, c := range list {
			stt := "有效"
			if c.Expired {
				stt = "已过期"
			} else if c.Exhausted {
				stt = "已用完"
			}
			exp := time.Unix(c.ExpiresAt, 0).In(time.FixedZone("SGT", 8*3600)).Format("01-02 15:04")
			rei := "否"
			if c.Reissue {
				rei = "是"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%d/%d\t%s\t%s\n", c.ID, c.HostName, exp, c.Uses, c.MaxUses, rei, stt)
		}
		return tw.Flush()
	case "delete":
		if len(rest) < 1 {
			return errors.New("需要 ID")
		}
		var id int64
		fmt.Sscanf(rest[0], "%d", &id)
		if id == 0 {
			return errors.New("ID 无效")
		}
		if err := st.DeleteEnroll(ctx, id); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "已删除注册码 %d\n", id)
	default:
		return errors.New(usage)
	}
	return nil
}

func importCmd(args []string) error {
	if len(args) < 1 || args[0] != "vnstat" {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("import vnstat", flag.ExitOnError)
	cfgPath := fs.String("config", "", "配置文件")
	host := fs.String("host", "", "主机名(需先 host add)")
	file := fs.String("file", "", "vnstat --json 输出文件")
	ifaces := fs.String("ifaces", "eth0,wt0", "网卡列表")
	before := fs.Int64("before", 0, "只导入该 UNIX 时间之前的桶(避免与 agent 数据重叠)")
	_ = fs.Parse(args[1:])
	_, st, err := openStore(*cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	h, err := st.HostByName(ctx, *host)
	if err != nil {
		return fmt.Errorf("主机 %q: %w", *host, err)
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := store.ParseVnstat(f, strings.Split(*ifaces, ","), *before)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(h.TZ)
	if err != nil {
		return err
	}
	for iface, hours := range data {
		if err := st.ImportHours(ctx, h.ID, loc, proto.KindIface, iface, hours); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "已导入 %s/%s:%d 个小时桶\n", h.Name, iface, len(hours))
	}
	return nil
}

func checkCmd(args []string) error {
	if len(args) < 1 || args[0] != "vnstat" {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("check vnstat", flag.ExitOnError)
	cfgPath := fs.String("config", "", "配置文件")
	host := fs.String("host", "", "主机名")
	file := fs.String("file", "", "vnstat --json 输出文件")
	ifaces := fs.String("ifaces", "", "网卡列表(默认该主机计费网卡)")
	_ = fs.Parse(args[1:])
	_, st, err := openStore(*cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	h, err := st.HostByName(ctx, *host)
	if err != nil {
		return fmt.Errorf("主机 %q: %w", *host, err)
	}
	list := []string{h.BillingIface}
	if *ifaces != "" {
		list = strings.Split(*ifaces, ",")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := store.ParseVnstat(f, list, 0)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(h.TZ)
	if err != nil {
		return err
	}
	res, err := st.CompareVnstat(ctx, h.ID, loc, data, time.Now())
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', tabwriter.AlignRight)
	for _, c := range res {
		fmt.Fprintf(tw, "[%s] 比对小时数 %d,仅 vnstat 有 %d,仅本系统有 %d,单日最大偏差 %.2f%%\n", c.Iface, c.Total.Hours, c.OnlyVnstat, c.OnlyOurs, c.MaxAbsPct)
		fmt.Fprintln(tw, "日期\t小时\tvnstat\t本系统\t差值\t偏差%\t")
		for _, d := range append(c.Days, c.Total) {
			fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%.2f\t\n", d.Day, d.Hours, d.Vnstat.Total(), d.Ours.Total(), d.Diff, d.Pct)
		}
	}
	return tw.Flush()
}

func backupCmd(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	cfgPath := fs.String("config", "", "配置文件")
	out := fs.String("out", "", "备份目标文件(必须不存在)")
	_ = fs.Parse(args)
	if *out == "" {
		return errors.New("缺少 -out")
	}
	_, st, err := openStore(*cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := os.MkdirAll(filepath.Dir(*out), 0o700); err != nil {
		return err
	}
	start := time.Now()
	if err := st.Backup(context.Background(), *out); err != nil {
		return fmt.Errorf("备份失败: %w", err)
	}
	_ = os.Chmod(*out, 0o600)
	fi, _ := os.Stat(*out)
	fmt.Fprintf(os.Stderr, "已备份到 %s(%d 字节,用时 %s)\n", *out, fi.Size(), time.Since(start).Round(time.Millisecond))
	return nil
}

func geoCmd(args []string, log *slog.Logger) error {
	if len(args) < 1 || args[0] != "update" {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("geo update", flag.ExitOnError)
	cfgPath := fs.String("config", "", "配置文件")
	_ = fs.Parse(args[1:])
	cfg, err := server.LoadConfig(server.ConfigPath(*cfgPath))
	if err != nil {
		return err
	}
	db := geo.Open(cfg.Geo.Dir)
	defer db.Close()
	u := &geo.Updater{DB: db, BaseURL: cfg.Geo.BaseURL, Log: log}
	if err := u.UpdateOnce(context.Background()); err != nil {
		return err
	}
	st := db.Status()
	fmt.Fprintf(os.Stderr, "归属地库就绪:city=%v asn=%v(%s)\n", st.City.Loaded, st.ASN.Loaded, cfg.Geo.Dir)
	return nil
}

// healthcheck 只解析配置里的 listen / tls,向本机 /healthz 发请求;不读取面板口令、不打开数据库。
// 监听 0.0.0.0 / [::] 时访问 127.0.0.1 同端口。返回非零表示不健康。
func healthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	cfgPath := fs.String("config", "", "配置文件")
	timeout := fs.Duration("timeout", 3*time.Second, "超时")
	_ = fs.Parse(args)
	var c struct {
		Listen  string `json:"listen"`
		TLSCert string `json:"tls_cert"`
	}
	c.Listen = "127.0.0.1:8787"
	if p := server.ConfigPath(*cfgPath); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &c); err != nil {
			return fmt.Errorf("解析配置失败: %w", err)
		}
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("listen 无效: %s", c.Listen)
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	scheme := "http"
	tr := &http.Transport{Proxy: nil}
	if c.TLSCert != "" {
		scheme = "https"
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // 仅本机自检,不校验证书
	}
	cl := &http.Client{Timeout: *timeout, Transport: tr}
	resp, err := cl.Get(scheme + "://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return fmt.Errorf("不健康: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("不健康: HTTP %d", resp.StatusCode)
	}
	return nil
}
