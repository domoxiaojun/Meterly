# 从 trafficstat 迁移到 Meterly

Meterly 是本项目的新名字。已经在跑旧版 `trafficstat-agent` / `trafficstat-server` 的主机,升级时会保留 token 和历史数据。本页只描述通用步骤,不涉及任何具体机房或主机名。

数据库文件名可以保持为 `trafficstat.db`(compose 默认 `/data/trafficstat.db`)。不要在升级时改 `server.json` 里的 `db_path`,否则会当成空库。

环境变量与路径的回退(二进制自己处理,不迁也能先跑起来):

| 新 | 旧(仍被读取) |
| --- | --- |
| `METERLY_CONFIG` | `TRAFFICSTAT_CONFIG` |
| `METERLY_TOKEN` | `TRAFFICSTAT_TOKEN` |
| `METERLY_PANEL_TOKEN` | `TRAFFICSTAT_PANEL_TOKEN` |
| `/etc/meterly/` | `/etc/trafficstat/` |
| `/var/lib/meterly` | `/var/lib/trafficstat` |
| `/var/lib/meterly-server` | `/var/lib/trafficstat-server` |
| `/opt/meterly` | `/opt/trafficstat` |

建议还是跑一次迁移脚本,把文件落到新路径,避免两套配置长期并存。

## 1. 先升级中心

在中心主机上,先做数据库备份,再迁移 compose 项目:

```bash
# 只看计划,不改任何东西
sudo ./scripts/migrate-center-compose.sh --dry-run

# 确认后执行。会停止旧栈、把代码同步到 /opt/meterly、
# 复制 Docker 卷 trafficstat_trafficstat-data → meterly_meterly-data、
# 改写 .env 中的 TS_* 为 METERLY_*、启动新栈。旧目录默认保留。
sudo ./scripts/migrate-center-compose.sh --apply
```

也可以不用脚本、手工操作:

1. `docker compose exec -T server /trafficstat-server backup -out /data/backups/pre-meterly-$(date +%Y%m%d).db`
2. 把备份再拷出卷外一份。
3. `docker compose --profile caddy down`(不带 `-v`)。
4. 把新版本同步到 `/opt/meterly`,保留原来的 `secrets/`、`.env`、Caddy 证书。
5. 把 `.env` 里的 `TS_VERSION` 等改成 `METERLY_VERSION`,镜像改为 `ghcr.io/domoxiaojun/meterly-server:latest`(或指定版本)。
6. 新建卷并拷贝旧卷数据,或暂时让新 compose 使用 `external: true` 挂上旧卷名。`db_path` 仍指向 `/data/trafficstat.db`。
7. `docker compose --profile caddy up -d`,执行 `docker compose exec server /meterly-server healthcheck`。
8. 打开面板确认主机列表和近期数据还在,再去升级 agent。

回滚:停新栈,在旧目录 `docker compose --profile caddy up -d`,必要时把备份库拷回卷内 `/data/trafficstat.db` 并删掉 `-wal`/`-shm`。

若从 GHCR 拉镜像,而包的可见性仍是 private,需要先 `docker login ghcr.io`。公开仓库新推的包默认是 private,可在 GitHub Package settings 里改为 Public。

## 2. 再升级各 agent

把新版 `scripts/install-agent.sh` 和二进制(或等 Release 发布后省略 `--binary`)拷到该机,先看探测结果:

```bash
# 只打印将启用的采集器,以及「将迁移旧版 trafficstat-agent」的提示,不改文件
sudo ./install-agent.sh --center URL --dry-run --binary ./meterly-agent-linux-amd64

# 正式升级:停止旧 unit,迁移 /etc/trafficstat 与 /var/lib/trafficstat 到新路径,保留 token,启用 meterly-agent
sudo ./install-agent.sh --center URL --token-file /etc/trafficstat/agent.token \
  --ca-file /etc/trafficstat/ca.crt \
  --binary ./meterly-agent-linux-amd64
```

脚本是幂等的:已迁过的机器再跑一遍只会保留现有 token 并刷新配置。不会改 sing-box / nginx / 1Panel / xray 的配置,也不会换发 token。

`server_url` 如果仍指向 `https://trafficstat.internal:…`,升级后可以暂时保留 `/etc/hosts` 中的旧名字,等中心域名改好后再把 agent 配置和 hosts 改成 `meterly.internal`。

回滚 agent:把上一版 `trafficstat-agent` 二进制拷回 `/usr/local/bin/trafficstat-agent`,用脚本留下的 `agent.json.bak-*` 恢复配置(或把文件迁回 `/etc/trafficstat`),启动旧 unit。state 目录可留,旧版本会忽略新的 kind。

## 3. 卸载

- agent:`/usr/local/sbin/uninstall-meterly-agent`(加 `--purge` 才删配置、数据和 hosts 项;同时清理残留的 `trafficstat-agent`)
- 中心:`/opt/meterly/scripts/uninstall-center-compose.sh`(加 `--purge` 才删卷和目录;也会清理旧的 `/opt/trafficstat` 与对应 iptables 注释)
