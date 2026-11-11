# Meterly v2

用一个 Go 中心和轻量 agent，查看 **哪一天、哪个网站、哪个客户端 IP 使用了多少源站流量**。适用于 1–20 台主机、100 个以内网站。中心使用 SQLite 文件；面板、ES Modules 和 Chart.js 内嵌在二进制中，部署不需要 Node 或数据库服务。

v2 是不兼容升级：仅支持 `/api/v2` 和协议版本 2；中心拒绝打开旧结构数据库，agent 拒绝读取旧状态。旧库归档、新版本启用和生产部署须另行操作，参见 [旧版切换说明](docs/migrate.md)。

## 面板

- **总览**：源站收发流量、请求数、客户端 IP 数、活跃网站、主要网站/IP、上一等长时段变化和采集告警。
- **网站**：选择日期、主机，搜索网站，按流量、请求或错误率排序；详情含趋势、具体状态码和完整 IP 分页排行。
- **IP**：网站日志与 sing-box 载荷分开查询；支持 IP/CIDR、国家和 ASN，反查关联网站或入站，导出全部匹配结果。
- **主机**：实际网卡、计费配置、采集器、发现/停用/失联的网站来源、配置版本、积压和升级。
- **对账**：按主机自身计费时区及重置日计算配额、预测、服务商读数和 vnstat 差异。
- **系统**：生成安装指引、检查正式版本、查看升级/维护记录、备份、磁盘容量和站内告警。

核心操作：**指定一天 → 网站 → IP 排行 → IP 详情 → 浏览器返回**。日期和筛选保存在 URL，返回保留查询与滚动位置。历史查询不定时刷新；当前范围自动刷新，并取消已过时的请求。支持深色、浅色、跟随系统和移动端。

## 统计口径

| 来源 | 字节含义 | 注意事项 |
|---|---|---|
| 源站日志 | nginx/OpenResty 记录的请求长度、响应字节 | 没有 `$request_length` 时上行不可用；不包含未回源的 CDN 缓存请求 |
| sing-box | 连接上传/下载载荷 | 轮询间隙无法识别的增量明确列为未归因 |
| 实际网卡 | 操作系统 RX/TX | 计费按指定网卡和单/双向口径计算 |
| NetBird / xray | 对等端、入站或用户计数 | xray 不提供来源 IP |

这些口径互有重叠，**不能相加作为总流量**。网站以规范化域名汇总，同域名跨主机仍可筛选；无法识别域名的日志按主机和来源独立展示。客户端 IP 数不是访客人数。访问日志按请求结束时刻归因，长连接可能在结束时形成峰值。

UTC 存储，网站/IP 分析日期统一按中心时区（默认 `Asia/Singapore`）解释。分钟明细默认 48 小时，完整 IP 小时明细 365 天，主机和网站日汇总长期保留；排行榜分页不影响原始明细。超出精度或保留期会显示说明，未采集不显示为精确零。旧日志不回填。

## 构建与验证

```sh
scripts/build.sh                 # 本机二进制
scripts/build.sh dist            # Linux amd64/arm64 二进制、完整版本包和 SHA256SUMS
go vet ./...
go test -race -count=1 ./...
npm ci                          # 仅开发/测试面板时需要
npx playwright install chromium
npm test
npm run test:browser
scripts/install-agent_test.sh
scripts/check-deploy.sh
```

完整包名为 `meterly-linux-amd64.tar.gz` / `meterly-linux-arm64.tar.gz`，包含中心、agent、systemd units、配置模板和安装器。原有二进制资产继续提供给独立更新器。测试使用临时 SQLite 文件、回环 HTTP/TLS 和模拟 systemctl，无需数据库服务或 Docker。

## 中心 systemd 部署

以下命令供**新中心部署**参考。先下载选定正式版本对应平台的完整包，与该版本 `SHA256SUMS` 校验后解压。

```sh
sudo useradd --system --home /var/lib/meterly-server --shell /usr/sbin/nologin meterly
sudo install -d -m 750 -g meterly /etc/meterly
sudo install -m 755 meterly-server /usr/local/bin/meterly-server
sudo install -m 640 -g meterly deploy/config/server.example.json /etc/meterly/server.json
sudo sh -c 'umask 077; openssl rand -base64 32 > /etc/meterly/panel.token'
sudo chown meterly:meterly /etc/meterly/panel.token
sudo install -m 644 deploy/systemd/meterly-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now meterly-server.service
```

已有 `meterly` 用户无需再创建。默认监听 `127.0.0.1:8787`，新库为 `/var/lib/meterly-server/meterly-v2.db`。对外接入应配置 TLS 反代与访问限制。`agent_url` 填写 agent 可访问的 HTTPS 接入地址；面板口令从 `panel_token_file` 读取。

中心内置维护调度：到期清理、归属地更新、每日正式版本检查，以及中心时区每天 **04:20** 的在线压缩备份，保留最近 **7 份**。若已有旧 `meterly-backup.timer`，切换前停用，避免重复备份；新完整包不包含旧 timer。磁盘不足只报警，不自动缩短一年明细保留期。

已有容器部署的模板说明位于 [部署模板说明](deploy/compose/README.md)。

## 接入 agent

1. 在系统页填写主机名及 HTTPS 接入地址，生成一次性注册码。
2. 把注册码保存为权限 `600` 的 `enroll.code`，下载并检查安装器。
3. 执行以下指令；将 `vX.Y.Z` 换成已经发布的目标正式版本。

```sh
chmod 600 enroll.code
sudo ./scripts/install-agent.sh --center https://meterly.internal:8443 \
  --enroll-code-file ./enroll.code --version vX.Y.Z
```

安装器也可单独下载后运行，会自动获取**完整版本包**。自签证书加 `--ca-file ./ca.crt`。仅明确需要明文内网时加 `--allow-http`，不会自动降级 HTTPS。token/注册码不放入 URL 或命令行参数。

脚本先检查二进制、units、现有配置、权限及中心连通性，再注册和启用服务。重复安装保留 v2 token 和显式配置；`--version` 始终获取指定版本。旧 agent 的配置和状态不会自动迁移。新状态目录为 `/var/lib/meterly-v2`。

agent 启动时及每 5 分钟发现网站和网卡，通过出站 HTTPS 获取配置/任务，不增加监听端口。配置优先级为：**本机显式字段/overrides → 面板期望配置 → 自动发现**。无效配置保留上次有效配置；停用来源不会被发现流程重新启用，消失的日志保留断点并报警。

网站支持 `/etc/nginx`、OpenResty/1Panel 配置及常见日志目录。当前不支持的路径可在本机 `collectors.site.sources` 与 `allow_dirs` 中显式配置。请保留轮转日志直至积压追平；若旧 inode 的文件已删除，无法凭空恢复其尾部，面板会显示采集异常。

CDN 归因、JSON 日志和可信代理配置见 [CDN 与日志配置](docs/cdn.md)。

## 升级和运行边界

中心每日缓存正式版本信息。面板主机详情填目标版本并确认后，agent 获取固定升级任务。独立 `meterly-agent-updater.service` 下载本仓库对应平台的二进制、核对 SHA256、原子替换、重启并检查本地就绪文件。新版本未就绪会恢复 `.previous`；中心暂时离线不会触发回滚。远程任务不能执行任意 Shell。

确认升级会短暂重启 agent。备份和升级结果可在系统页查看；任务响应超时可刷新查询现有任务，避免重复操作。

更多接口及完整性说明见 [v2 API](docs/api-v2.md)。本次验证与尚待实际环境验收的边界见 [交付验证记录](plans/full-upgrade/verification.md)。
