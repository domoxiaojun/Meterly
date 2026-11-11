# 中心部署 · 方式一:docker compose(推荐)

中心 = `meterly-server`(单个静态 Go 二进制,镜像基于 `scratch`)+ 可选 Caddy 反代。
**后端主机一律用 agent + systemd(不放容器)**,见仓库根 README「agent」。中心也可以不用 Docker,见根 README「方式二:systemd」。

镜像默认从 `ghcr.io/domoxiaojun/meterly-server:${METERLY_VERSION:-latest}` 拉取;也可以在本目录 `docker compose build` 本地构建。数据库文件名默认为 `/data/trafficstat.db`(兼容旧部署,不要随意改)。

上线前请按文末「验证清单」逐项过一遍。

## 文件
| 文件 | 作用 |
|---|---|
| `Dockerfile` | 多阶段:`golang:1.27-alpine` 静态编译(`CGO_ENABLED=0`)→ `scratch`;uid 65532;内置 CA 证书与默认配置;`HEALTHCHECK` 用二进制自带的 `healthcheck` 子命令 |
| `docker-compose.yml` | `server`(只读根、tmpfs `/tmp`、`cap_drop: ALL`、`no-new-privileges`、CPU/内存/进程数限制、json-file 日志轮转 10m×3、命名卷 `/data`)+ `caddy`(profile,可选) |
| `server.json` | 容器内配置(打进镜像 `/etc/meterly/server.json`):监听 `0.0.0.0:8787`、数据 `/data`、面板口令 `/run/secrets/panel_token`、归属地 `/data/geo` |
| `Caddyfile` | TLS(内置 CA 自签或 ACME);宿主 ingest 端口只反代 `POST /api/v1/ingest`,面板端口只反代面板;面板另有来源 IP 白名单 |
| `.env.example` | 版本号、发布地址、资源限制、Caddy 参数、可选面板端口;**不含任何 secret** |
| `meterly-docker-user.sh` | 为宿主 ingest/面板端口写入 INPUT 与 DOCKER-USER 来源 IP 限制 |
| `meterly-firewall.service` | 开机(在 docker 发布端口前)套用上述规则的 systemd 模板,拷走后填写 IP |

端口:server 容器内监听 `0.0.0.0:8787`,compose 只发布到宿主 `${METERLY_BIND:-127.0.0.1}`。启用 Caddy 时再发布两个默认只绑本机的端口:ingest(`METERLY_CADDY_PORT`,默认 8443)和面板(`METERLY_PANEL_PUBLIC_PORT`,默认 8444)。默认不对公网暴露。

## 首次部署
```bash
cd deploy/compose
cp .env.example .env                      # 设置 METERLY_VERSION(镜像 tag),按需改 METERLY_BIND
mkdir -p secrets
( umask 077; openssl rand -base64 32 > secrets/panel_token )   # 面板口令,只写文件
sudo chown 65532:65532 secrets/panel_token                     # 容器内 uid 65532 读取;server 要求权限 600/400
docker compose config -q                  # 渲染校验
docker compose build
docker compose up -d
docker compose ps                         # STATUS 应为 healthy
docker compose logs -f server             # 首次启动会下载 DB-IP 归属地库(约 65 MB);失败只告警,不影响服务
```
`METERLY_VERSION` 建议 `$(git describe --tags --always)`;同时可设 `METERLY_COMMIT=$(git rev-parse --short HEAD)`、`METERLY_BUILD_DATE=$(date -u +%FT%TZ)`。

### 让 agent 推送进来
二选一(都不对公网开放):
1. **NetBird 内网明文 + token**:`.env` 里 `METERLY_BIND=<宿主 NetBird 地址>`(如 `100.66.x.x`),agent 配
   `"server_url": "http://100.66.x.x:8787"` + `"allow_http": true`。
2. **Caddy TLS(内置 CA)**:`docker compose --profile caddy up -d`,`METERLY_CADDY_BIND=<NetBird 地址>`;
   导出根证书给 agent 做证书固定:
   ```bash
   docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt ./caddy-root.crt
   # 拷到每台 agent 的 /etc/meterly/ca.crt,agent.json 设 "ca_file": "/etc/meterly/ca.crt",
   # "server_url": "https://meterly.internal:8443"(agent 主机需能把该名字解析到中心 NetBird 地址)
   ```
   `METERLY_TLS=邮箱` 走 ACME 公网证书,需要公网 80/443 —— 属于开新端口,须先确认已开放 80/443 并完成域名解析。


### 指定 IP 访问面板(可选)

默认面板只在宿主回环地址:`http://127.0.0.1:8787`(或经 SSH 隧道)。若要让某几个固定公网 IP 直接用 HTTPS 打开面板,用 Caddy 的面板专用端口,不要把 ingest 端口或 `0.0.0.0:8787` 暴露出去。

1. 在 `.env` 里设置(把地址和 IP 换成你的,不要提交真实值):
   ```bash
   METERLY_PANEL_BIND=192.0.2.10          # 宿主要发布的地址,尽量不要用 0.0.0.0
   METERLY_PANEL_PUBLIC_PORT=8444         # 选一个空闲端口;不要占用 80/443
   # 若证书要带上访问时用的名字或 IP,把它们写进站点地址(均须带 :端口)
   METERLY_PANEL_ADDRESSES=meterly.internal:8444, 192.0.2.10:8444
   # 用 IP 访问时客户端通常不带 SNI,须与上面某一个站点名/IP 一致
   METERLY_DEFAULT_SNI=192.0.2.10
   # Caddy 层白名单也要包含客户端公网 IP(与防火墙列表可以相同,或再加内网网段)
   METERLY_PANEL_ALLOW=127.0.0.0/8 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 203.0.113.30
   ```
2. **先写防火墙,再发布端口**(Docker 发布的端口会绕过 INPUT,两条链都要写,且必须在端口开始监听之前生效):
   ```bash
   # ingest 端口已在跑的话,把原有 AGENT_ALLOW_IPS 一并带上,避免重跑时漏掉
   sudo AGENT_ALLOW_IPS="203.0.113.10 198.51.100.20" INGEST_PORT=8443 \
        PANEL_ALLOW_IPS="203.0.113.30" PANEL_PUBLIC_PORT=8444 \
        ./meterly-docker-user.sh
   sudo cp meterly-firewall.service /etc/systemd/system/
   # 编辑该 unit,填入同样的允许列表;多个 IP 必须写成 Environment="AGENT_ALLOW_IPS=... ..."(加引号,否则 systemd 只保留第一个)
   sudo systemctl daemon-reload
   sudo systemctl enable --now meterly-firewall.service
   ```
3. 然后再启动或重建 Caddy:`docker compose --profile caddy up -d caddy`。
4. 客户端浏览器打开 `https://<METERLY_PANEL_BIND>:<METERLY_PANEL_PUBLIC_PORT>/`。面板口令仍然有效,不要改 token。

`METERLY_TLS=internal` 时证书由 Caddy 内置 CA 签发。浏览器会提示证书不受信任;把 CA 根证书加到系统或浏览器信任区后即可消除警告(若 `METERLY_PANEL_ADDRESSES` 里包含访问用的名字或 IP,也不会再报名称不匹配):

```bash
docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt ./caddy-root.crt
```

以后若改用公网域名和 Let's Encrypt,把 `METERLY_TLS` 设为邮箱并完成域名解析即可,不需要再手工信任根证书。不要为此新开 80/443,除非你已经确认这两端口可以给 Caddy 使用。

回滚:把 `METERLY_PANEL_BIND` 改回 `127.0.0.1`(或去掉面板端口映射),`docker compose --profile caddy up -d caddy`,再执行 `sudo PANEL_PUBLIC_PORT=8444 PANEL_ALLOW_IPS= ./meterly-docker-user.sh` 清掉面板端口规则。ingest 规则不受影响。

### 主机与 token
```bash
# token 只在 stdout 输出一次:直接重定向到 600 文件,再安全拷到 agent 的 /etc/meterly/agent.token,然后删除本地副本
( umask 077; docker compose exec -T server /meterly-server host add host-a -billing both -quota 2TiB > host-a.token )
docker compose exec server /meterly-server host list
docker compose exec server /meterly-server host set host-b -billing tx -reset-day 9 -tz Asia/Singapore
( umask 077; docker compose exec -T server /meterly-server host rotate host-a > host-a.token )   # 旧 token 立即失效
docker compose exec server /meterly-server host revoke host-a
```

## 归属地库(DB-IP Lite,CC BY 4.0)
- 文件在卷内 `/data/geo/dbip-{city,asn}-lite.mmdb`。server 启动时检查一次,之后每 24 小时检查;
  只有跨月(DB-IP 每月初发布)才真正下载。下载 → 解压(上限 1 GiB)→ 完整校验 → 原子替换 → 热加载。
- 下载失败(无外网、DB-IP 不可达)只记录告警,服务照常;没有库时面板归属地显示「未知」。
- 手动更新:`docker compose exec server /meterly-server geo update`。
- 离线导入:把 `dbip-city-lite-YYYY-MM.mmdb.gz` 解压后改名为 `dbip-city-lite.mmdb`(ASN 同理),
  `docker compose cp` 进 `server:/data/geo/` 后 `docker compose restart server`。
- IP 查询全部在本地完成,**不会把任何 IP 发给第三方**。面板页脚有 DB-IP 署名(许可要求)。

## 备份与恢复
SQLite 在线一致性备份(`VACUUM INTO`,不停服务,不阻塞 ingest 太久):
```bash
docker compose exec server /meterly-server backup -out /data/backups/ts-$(date +%Y%m%d-%H%M%S).db
docker compose cp server:/data/backups/ts-20261111-042000.db ./          # 拷出到宿主 / 异地
```
宿主 crontab 例(每天 04:20,保留 14 份):
```cron
20 4 * * * cd /opt/meterly/deploy/compose && docker compose exec -T server /meterly-server backup -out /data/backups/ts-$(date +\%Y\%m\%d).db
```
(scratch 镜像里没有 shell,清理旧备份请在宿主上对卷目录操作,或拷出后在宿主上轮转。)

恢复:
```bash
docker compose stop server
docker run --rm -v meterly_meterly-data:/data -v "$PWD":/in:ro alpine \
  sh -c 'cp /in/ts-20261111-042000.db /data/trafficstat.db && rm -f /data/trafficstat.db-wal /data/trafficstat.db-shm && chown 65532:65532 /data/trafficstat.db && chmod 600 /data/trafficstat.db'
docker compose start server
```
备份文件里有 host token 的 **SHA-256 哈希**(不是明文),仍应按敏感数据保管。

## 升级
```bash
git pull                                   # 或拷入新版本源码
sed -i "s/^METERLY_VERSION=.*/METERLY_VERSION=$(git describe --tags --always)/" .env
docker compose exec server /meterly-server backup -out /data/backups/pre-upgrade-$(date +%Y%m%d).db
docker compose build && docker compose up -d   # 新 tag 的镜像替换容器,卷保留
docker compose ps && docker compose exec server /meterly-server version
```
回滚:把 `.env` 的 `METERLY_VERSION` 改回旧 tag(旧镜像仍在本机),`docker compose up -d`。目前 schema 变更只有新增表(`CREATE TABLE IF NOT EXISTS`),
旧版本一般能直接读新库;若某次升级说明里写了不兼容变更,回滚时先按「恢复」导回升级前备份。

## 安全要点
- 面板口令走 compose `secrets`(文件挂载),不进环境变量、不进 `docker inspect`。
- 容器:uid 65532、只读根、`cap_drop: ALL`、`no-new-privileges`、进程 umask 077;scratch 镜像无 shell。
- 日志不含 token / 口令 / 请求体;Caddy 未开启 access log。
- 面板与 ingest 鉴权分离;经 Caddy 时分端口:ingest 端口只放行 `POST /api/v1/ingest`,面板端口只反代面板(另有来源 IP 白名单)。把面板端口发到非回环地址前,须先用 `meterly-docker-user.sh` 写好 INPUT 与 DOCKER-USER。
- 修改类接口只接受 `application/json`,会话 Cookie `SameSite=Strict`(防 CSRF)。

## 验证清单
> 先确认:该主机已装 Docker Engine ≥ 24 与 compose 插件 v2(`docker version`、`docker compose version`),
> 并确认可以在该主机上部署。以下全部在 `deploy/compose/` 目录执行。

| # | 检查 | 命令 / 期望 |
|---|---|---|
| 1 | 渲染 | `docker compose config -q` 无输出;`docker compose --profile caddy config -q` 无输出 |
| 2 | 构建 | `docker compose build` 成功;`docker image ls meterly-server` 有 `METERLY_VERSION` tag,大小约 15 MB |
| 3 | 镜像内容 | `docker run --rm --entrypoint /meterly-server meterly-server:$METERLY_VERSION version` 输出版本 |
| 4 | 启动 | `docker compose up -d`;`docker compose ps` 在 20~60 s 内变为 `healthy` |
| 5 | HEALTHCHECK | `docker inspect --format '{{json .State.Health}}' meterly-server-1` 最近几次 ExitCode 0 |
| 6 | 只读根 | `docker compose exec server /meterly-server backup -out /x.db` 应失败(只读);`-out /data/backups/t.db` 成功 |
| 7 | 用户与能力 | `docker inspect` 中 `User=65532:65532`、`ReadonlyRootfs=true`、`CapDrop=[ALL]`、`SecurityOpt` 含 `no-new-privileges` |
| 8 | 端口 | 宿主 `ss -ltnp` 上 `8787` 只绑 `127.0.0.1`(或设定的 NetBird 地址),**没有** `0.0.0.0:8787`;Caddy 的 8443/8444 默认也只绑 `127.0.0.1` |
| 9 | 鉴权 | `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8787/api/v1/overview` = 401;`/healthz` = 200 |
| 10 | 面板 | 浏览器经 SSH 隧道 / NetBird(或已放行的面板端口)打开面板,口令登录正常,页脚有 DB-IP 署名 |
| 11 | 归属地下载 | 日志有「归属地库已更新」;`/data/geo/*.mmdb` 存在(若主机无外网:日志告警,服务仍 healthy) |
| 12 | ingest | `host add` 一台测试主机,agent(或 `demoseed`)推送后面板出现数据 |
| 13 | 重启保留 | `docker compose restart server` 与 `docker compose down && docker compose up -d` 后数据、主机、归属地库都在,且不重复下载 |
| 14 | 资源 | `docker stats --no-stream` 内存 < 100 MB(限制 512M);日志文件轮转配置生效(`docker inspect` LogConfig) |
| 15 | 备份恢复 | 按「备份与恢复」做一遍,恢复后数据一致 |
| 16 | 升级 | 改 `METERLY_VERSION` 重建,`up -d` 后数据保留,`version` 显示新版本 |
| 17 | Caddy(可选) | `--profile caddy up -d`;`curl --cacert caddy-root.crt https://meterly.internal:8443/healthz`(`--resolve` 指到 ingest 绑定地址)= ok;`POST /api/v1/ingest` 可达;ingest 端口上访问面板或 `GET /api/v1/ingest` 返回 403;面板端口 `https://meterly.internal:8444/` 为面板 HTML,`/api/v1/overview` 无口令时 401,`POST /api/v1/ingest` 为 403;非白名单来源访问面板 403 |
| 18 | Caddy 证书 | `docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt .` 成功;重启后证书不变(caddy-data 卷持久) |
| 19 | trusted_proxies | 经 Caddy 访问时 server 日志里的 `ip` 是真实来源 IP 而不是 172.30.87.3 |
| 20 | 网段冲突 | `172.30.87.0/24` 不与宿主现有网络冲突(`ip route`);冲突时按 compose 注释同时改 3 处 |
| 21 | 面板公网端口(可选) | 先 `meterly-docker-user.sh` 再 `up -d caddy`;未放行来源对面板端口应超时;放行来源可打开面板;ingest 端口限制不变 |
