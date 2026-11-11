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
| `Caddyfile` | TLS(内置 CA 自签或 ACME)、`POST /api/v1/ingest` 与面板路径分离、面板来源 IP 白名单 |
| `.env.example` | 版本号、发布地址、资源限制、Caddy 参数;**不含任何 secret** |

端口:server 容器内监听 `0.0.0.0:8787`,compose 只发布到宿主 `${METERLY_BIND:-127.0.0.1}`。默认不对公网暴露,也不新增对外端口。

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
- 面板与 ingest 鉴权分离;经 Caddy 时面板额外受来源 IP 白名单限制,ingest 只放行 `POST /api/v1/ingest`。
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
| 8 | 端口 | 宿主 `ss -ltnp` 只有 `127.0.0.1:8787`(或设定的 NetBird 地址),**没有** `0.0.0.0:8787` |
| 9 | 鉴权 | `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8787/api/v1/overview` = 401;`/healthz` = 200 |
| 10 | 面板 | 浏览器经 SSH 隧道 / NetBird 打开面板,口令登录正常,页脚有 DB-IP 署名 |
| 11 | 归属地下载 | 日志有「归属地库已更新」;`/data/geo/*.mmdb` 存在(若主机无外网:日志告警,服务仍 healthy) |
| 12 | ingest | `host add` 一台测试主机,agent(或 `demoseed`)推送后面板出现数据 |
| 13 | 重启保留 | `docker compose restart server` 与 `docker compose down && docker compose up -d` 后数据、主机、归属地库都在,且不重复下载 |
| 14 | 资源 | `docker stats --no-stream` 内存 < 100 MB(限制 512M);日志文件轮转配置生效(`docker inspect` LogConfig) |
| 15 | 备份恢复 | 按「备份与恢复」做一遍,恢复后数据一致 |
| 16 | 升级 | 改 `METERLY_VERSION` 重建,`up -d` 后数据保留,`version` 显示新版本 |
| 17 | Caddy(可选) | `--profile caddy up -d`;`curl --cacert caddy-root.crt https://meterly.internal:8443/healthz`(`--resolve` 指到绑定地址)= ok;非白名单来源访问面板 403;`POST /api/v1/ingest` 可达;`GET /api/v1/ingest` 走面板分支 |
| 18 | Caddy 证书 | `docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt .` 成功;重启后证书不变(caddy-data 卷持久) |
| 19 | trusted_proxies | 经 Caddy 访问时 server 日志里的 `ip` 是真实来源 IP 而不是 172.30.87.3 |
| 20 | 网段冲突 | `172.30.87.0/24` 不与宿主现有网络冲突(`ip route`);冲突时按 compose 注释同时改 3 处 |
