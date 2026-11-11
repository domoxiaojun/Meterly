# Meterly

自建的分布式流量统计:在每台机器上跑一个只出站的 agent,把网卡、sing-box、站点日志、NetBird、xray 的流量按分钟汇总到中心,用中文面板查看。不依赖 Prometheus / Grafana。

Meterly is a self-hosted traffic accounting system: a small outbound-only agent on each host, a single-binary center with SQLite and a Chinese web panel. See the rest of this file for setup; the documentation is in Chinese.

## 功能

- 按主机、时间、入站、来源 IP、站点、NetBird 对等端查看流量
- 网卡总量与 sing-box / 站点 / NetBird 的对账拆解(口径不同,不能并列相加)
- IP 归属地与 ASN(离线 [DB-IP Lite](https://db-ip.com/),见下方版权说明)
- 计费周期、配额、与服务商数字比对(可导入 vnstat)
- 一条命令安装 agent:自动探测本机有什么采集器,用短时效注册码向中心领 token
- 中心可用 Docker Compose,或 systemd 单二进制;agent 只用 systemd

## 架构

```
主机 A, B, …                    中心
┌─ meterly-agent ─┐            ┌─ meterly-server ─┐
│ 网卡 /proc      │  HTTPS     │ ingest + SQLite  │
│ sing-box API    │ ──每分钟──▶│ 中文面板 :8787   │
│ nginx/1Panel 日志│  只出站    │ 可选 Caddy TLS   │
│ NetBird / xray  │            └──────────────────┘
└─────────────────┘
```

agent 是静态 Go 二进制,默认不监听任何端口。中心把事实数据写进统一表 (`host, minute, kind, dim, rx, tx`),并汇总到小时/天。库文件在 compose 下默认仍叫 `/data/trafficstat.db`(兼容旧部署,不要随意改名)。

## 快速开始

需要 Go 1.27+ 才能从源码构建。也直接用 GitHub Release 里的二进制,或 GHCR 镜像。

```bash
git clone https://github.com/domoxiaojun/Meterly.git
cd Meterly
scripts/build.sh          # 本机构建到 build/
scripts/build.sh dist     # linux amd64/arm64 静态包 + SHA256SUMS
```

### 中心 · 方式一:Docker Compose(推荐)

镜像:`ghcr.io/domoxiaojun/meterly-server`。也可以在本目录 `docker compose build`。加固项(非 root、只读根、drop cap、口令走文件)保持不变,细节见 [`deploy/compose/README.md`](deploy/compose/README.md)。

```bash
cd deploy/compose
cp .env.example .env          # METERLY_VERSION 默认 latest,可改成 v0.3.0
mkdir -p secrets
( umask 077; openssl rand -base64 32 > secrets/panel_token )
sudo chown 65532:65532 secrets/panel_token
docker compose pull           # 或 docker compose build
docker compose up -d
# 需要 TLS 反代时:
docker compose --profile caddy up -d
```

默认只把面板发布到宿主 `127.0.0.1:8787`。给 agent 推送时,把 `.env` 里的 `METERLY_BIND` 改成内网或 NetBird 地址,不要用 `0.0.0.0`。若要让固定公网 IP 用 HTTPS 打开面板,见 `deploy/compose/README.md`「指定 IP 访问面板」(独立端口 + 防火墙白名单)。拉 GHCR 私有包需要先 `docker login ghcr.io`。

```bash
# 登记一台主机,token 只在 stdout 出现这一次
( umask 077; docker compose exec -T server /meterly-server host add host-a -billing both -quota 2TiB > host-a.token )
# 或生成短时效注册码,让 agent 自己领 token
docker compose exec -T server /meterly-server enroll create -host host-a
```

### 中心 · 方式二:systemd 单二进制

```bash
install -m 755 dist/meterly-server-linux-amd64 /usr/local/bin/meterly-server
useradd -r -s /usr/sbin/nologin meterly
install -d -m 750 -g meterly /etc/meterly
install -m 640 -g meterly deploy/config/server.example.json /etc/meterly/server.json
( umask 077; openssl rand -base64 32 > /etc/meterly/panel.token )
chown meterly:meterly /etc/meterly/panel.token
cp deploy/systemd/meterly-server.service /etc/systemd/system/
cp deploy/systemd/meterly-backup.{service,timer} /etc/systemd/system/   # 可选,每日在线备份
systemctl daemon-reload && systemctl enable --now meterly-server meterly-backup.timer
meterly-server healthcheck -config /etc/meterly/server.json
```

默认只监听 `127.0.0.1:8787`。数据在 `/var/lib/meterly-server`(含 `geo/`、`backups/`)。新安装默认库文件是 `meterly.db`;从旧版迁过来时,二进制会回退已有的 `trafficstat.db`。

### 一条命令装 agent

```bash
# 中心上先出注册码(限次、短时效;不要把码写进命令行)
meterly-server enroll create -host host-a -config /etc/meterly/server.json
# 把码写进 600 文件后,在新机器上:
curl -fsSL -o install-agent.sh https://raw.githubusercontent.com/domoxiaojun/Meterly/main/scripts/install-agent.sh
chmod +x install-agent.sh
sudo ./install-agent.sh --center https://meterly.internal:8443 \
  --enroll-code-file ./enroll.code --ca-file ./ca.crt
```

未指定 `--binary` 时,脚本会从 GitHub Release 下载对应架构的 `meterly-agent`,并用 `SHA256SUMS` 校验。可用 `--version v0.3.0` 钉死版本。`--dry-run` 只打印将启用的采集器,不改任何文件。

已有 token 的机器(升级、不换发):

```bash
sudo ./install-agent.sh --center URL --token-file /etc/meterly/agent.token --ca-file /etc/meterly/ca.crt
```

若这台机器以前跑的是 `trafficstat-agent`,脚本会停止旧 unit、把配置和 token 迁到 `/etc/meterly` 与 `/var/lib/meterly`、启用新 unit。详见 [`docs/migrate.md`](docs/migrate.md)。

## 配置要点

- token 来源:配置里的 `token_file` > 环境变量 `METERLY_TOKEN`(回退 `TRAFFICSTAT_TOKEN`) > `token` 字段。文件权限必须是 600。
- 中心配置路径:命令行 `-config` > `METERLY_CONFIG`(回退 `TRAFFICSTAT_CONFIG`) > `/etc/meterly/server.json`(若不存在则回退 `/etc/trafficstat/server.json`)。
- 面板口令:`panel_token_file` > `METERLY_PANEL_TOKEN`(回退 `TRAFFICSTAT_PANEL_TOKEN`)。监听非回环地址时必须配置,至少 12 个字符。
- 采集器在 `agent.json` 里分别 `enabled`。安装脚本会探测 sing-box、xray、nginx、1Panel openresty、NetBird 和网卡,默认只开启探测到的项。
- xray 只能按入站/用户统计,没有来源 IP;站点日志按请求结束时归因(长连接会记在结束的那一小时)。

常用运维命令(compose 下加上 `docker compose exec -T server`):

```bash
meterly-server backup -out /var/lib/meterly-server/backups/ts-$(date +%F).db
meterly-server geo update
meterly-server check vnstat -host host-a -file vnstat.json
meterly-server healthcheck
meterly-server host list
```

## 安全

- agent 只出站,不监听端口;systemd 带 Nice、内存上限和 ProtectSystem 等加固。
- 中心 compose:uid 65532、只读根、`cap_drop: ALL`、口令只放 `secrets/panel_token`(600)。
- 默认端口只绑在 `127.0.0.1`。Caddy 可选,面板走来源 IP 白名单,ingest 用 host token,两者分开。
- 日志和探测输出不含 token / clash secret。
- 不要把 token、口令、`.env`、`secrets/` 提交进仓库。

## 从旧版 trafficstat 升级

已经部署过旧名字的,先备份中心数据库,再按 [`docs/migrate.md`](docs/migrate.md) 先升级中心、后升级 agent。不要改 `db_path`。

## 开发

```bash
go test -race ./...
scripts/check-deploy.sh      # compose / unit 静态检查,不需要 Docker daemon
scripts/install-agent_test.sh
```

面板截图(演示数据):

- [机器与采集器](docs/panel-collectors.png)
- [对账拆解](docs/panel-recon.png)
- [xray](docs/panel-xray.png)

## License

源代码为 [MIT](LICENSE)。

IP 归属地数据来自 [DB-IP Lite](https://db-ip.com/)(City / ASN),许可为 [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/)。面板页脚和本段为署名;请勿去掉。运行时库下载到数据目录的 `geo/`,不包含在本仓库中。
