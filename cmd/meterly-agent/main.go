// meterly-agent:Meterly 采集端(单静态二进制,只出站,不监听端口)。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/domoxiaojun/Meterly/internal/agent"
	"github.com/domoxiaojun/Meterly/internal/compat"
	"github.com/domoxiaojun/Meterly/internal/version"
)

const usage = `用法:
  meterly-agent run     -config /etc/meterly/agent.json   常驻运行(systemd)
  meterly-agent check   -config /etc/meterly/agent.json   采集一次并打印摘要(不推送)
  meterly-agent detect  [-json] [-only a,b] [-skip a,b] [-root DIR]  探测本机采集器,打印建议(不含 secret)
  meterly-agent write-config -center URL -config PATH [-token-file] [-ca-file] [-allow-http] [-only] [-skip]
  meterly-agent enroll  -center URL -code-file PATH -out PATH [-ca-file] [-allow-http] [-hostname NAME]
  meterly-agent upgrade -url https://... -sha256 HEX [-target 路径]  校验后原子替换二进制
  meterly-agent version
token / 注册码不走命令行参数,只从文件或标准输入读取。`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", "", "配置文件(mode 600);缺省 /etc/meterly/agent.json,若不存在则回退 /etc/trafficstat/agent.json")
	url := fs.String("url", "", "升级包地址")
	sum := fs.String("sha256", "", "升级包 sha256")
	target := fs.String("target", "", "要替换的二进制路径(默认当前可执行文件)")
	asJSON := fs.Bool("json", false, "detect:以 JSON 输出")
	only := fs.String("only", "", "只启用这些采集器,逗号分隔")
	skip := fs.String("skip", "", "跳过这些采集器,逗号分隔")
	root := fs.String("root", "", "detect:把此目录当作文件系统根(测试用)")
	center := fs.String("center", "", "中心 URL")
	tokenFile := fs.String("token-file", "", "token 文件路径;缺省 /etc/meterly/agent.token,回退 /etc/trafficstat/agent.token")
	caFile := fs.String("ca-file", "", "中心 CA 证书")
	allowHTTP := fs.Bool("allow-http", false, "允许明文 HTTP")
	codeFile := fs.String("code-file", "", "注册码文件,或 - 表示标准输入")
	outPath := fs.String("out", "", "enroll:写入 token 的路径;缺省 /etc/meterly/agent.token")
	hostname := fs.String("hostname", "", "enroll:声明的主机名(须与注册码一致)")
	stateDir := fs.String("state-dir", "", "状态目录;缺省 /var/lib/meterly,若不存在则回退 /var/lib/trafficstat")
	_ = fs.Parse(args)
	if *cfgPath == "" {
		*cfgPath = compat.AgentConfigPath()
	}
	if *tokenFile == "" {
		*tokenFile = compat.AgentTokenPath()
	}
	if *outPath == "" {
		*outPath = compat.AgentTokenNew
	}
	if *stateDir == "" {
		*stateDir = compat.AgentStateDir()
	}

	splitCSV := func(s string) []string {
		var out []string
		for _, p := range strings.Split(s, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}

	switch cmd {
	case "version", "-v", "--version":
		fmt.Println("meterly-agent", version.String())
	case "detect":
		sys := &agent.DetectSys{Root: *root}
		d, err := agent.Detect(sys, splitCSV(*only), splitCSV(*skip))
		if err != nil {
			log.Error("探测失败", "err", err.Error())
			os.Exit(1)
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(d)
			break
		}
		printDetect(d)
	case "write-config":
		if *center == "" {
			fmt.Fprintln(os.Stderr, "需要 -center")
			os.Exit(2)
		}
		sys := &agent.DetectSys{}
		d, err := agent.Detect(sys, splitCSV(*only), splitCSV(*skip))
		if err != nil {
			log.Error("探测失败", "err", err.Error())
			os.Exit(1)
		}
		cfg := agent.ProposalConfig(d, *center, *tokenFile, *caFile, *stateDir, *allowHTTP)
		if err := agent.WriteManagedConfig(*cfgPath, cfg, splitCSV(*only), splitCSV(*skip)); err != nil {
			log.Error("写配置失败", "err", err.Error())
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "已写入", *cfgPath)
		printDetect(d)
	case "enroll":
		if *center == "" || *codeFile == "" {
			fmt.Fprintln(os.Stderr, "需要 -center 与 -code-file(用 - 表示从标准输入读注册码)")
			os.Exit(2)
		}
		code, err := readSecret(*codeFile)
		if err != nil {
			log.Error("读取注册码失败", "err", err.Error())
			os.Exit(1)
		}
		resp, err := agent.RedeemEnroll(context.Background(), *center, *caFile, *allowHTTP, code, *hostname, 20*time.Second)
		if err != nil {
			log.Error("注册失败", "err", err.Error())
			os.Exit(1)
		}
		if err := agent.WriteTokenFile(*outPath, resp.Token); err != nil {
			log.Error("写 token 失败", "err", err.Error())
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "已注册主机 %s,token 已写入 %s(0600)\n", resp.Host, *outPath)
	case "validate":
		cfg, err := agent.LoadConfig(*cfgPath)
		if err == nil {
			_, err = agent.LoadState(cfg.StateDir)
		}
		if err != nil {
			log.Error("配置验证失败", "err", err)
			os.Exit(1)
		}
		fmt.Println("配置和状态协议有效")
	case "run", "check":
		cfg, err := agent.LoadConfig(*cfgPath)
		if err != nil {
			log.Error("配置错误", "err", err.Error())
			os.Exit(1)
		}
		a, err := agent.New(cfg, log)
		if err != nil {
			log.Error("初始化失败", "err", err.Error())
			os.Exit(1)
		}
		defer a.Close()
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if cmd == "check" {
			os.Exit(a.Check(ctx, os.Stdout))
		}
		if err := a.Run(ctx); err != nil {
			log.Error("退出", "err", err.Error())
			os.Exit(1)
		}
		log.Info("agent 已停止")
	case "upgrade":
		t := *target
		if t == "" {
			var err error
			if t, err = os.Executable(); err != nil {
				log.Error("无法确定当前二进制路径", "err", err.Error())
				os.Exit(1)
			}
		}
		if err := agent.Upgrade(context.Background(), *url, *sum, t); err != nil {
			log.Error("升级失败", "err", err.Error())
			os.Exit(1)
		}
		fmt.Println("升级完成:", t, "\n请执行: systemctl restart meterly-agent")
	case "apply-upgrade":
		if cfg, err := agent.LoadConfig(*cfgPath); err == nil {
			*stateDir = cfg.StateDir
		} else {
			log.Error("读取更新器配置失败", "err", err)
			os.Exit(1)
		}
		if *target == "" {
			*target = "/usr/local/bin/meterly-agent"
		}
		if err := (agent.Updater{}).Apply(context.Background(), *stateDir, *target); err != nil {
			log.Error("升级任务结束", "err", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func readSecret(path string) (string, error) {
	var b []byte
	var err error
	if path == "-" {
		b, err = io.ReadAll(io.LimitReader(os.Stdin, 4096))
	} else {
		if err := mode600(path); err != nil {
			return "", err
		}
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func mode600(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s 权限为 %o,必须为 600", path, st.Mode().Perm())
	}
	return nil
}

func printDetect(d *agent.DetectResult) {
	fmt.Printf("主机名: %s\n", d.Hostname)
	fmt.Print("网卡: ")
	if len(d.Interfaces) == 0 {
		fmt.Println("(无)")
	} else {
		for i, n := range d.Interfaces {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Printf("%s(%s)", n.Name, n.Reason)
		}
		fmt.Println()
	}
	order := []string{"netdev", "singbox", "site", "netbird", "xray"}
	labels := map[string]string{"netdev": "网卡", "singbox": "sing-box", "site": "站点日志", "netbird": "NetBird", "xray": "xray"}
	for _, k := range order {
		c := d.Collectors[k]
		st := "未启用"
		if c.Enabled {
			st = "将启用"
		} else if c.Detected {
			st = "已探测,不启用"
		}
		reason := c.Reason
		if reason == "" {
			reason = "—"
		}
		fmt.Printf("  %-8s  %s  %s\n", labels[k], st, reason)
	}
	for _, n := range d.Notes {
		fmt.Println("说明:", n)
	}
}
