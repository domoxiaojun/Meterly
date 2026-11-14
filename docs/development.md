# Meterly 开发文档

更新日期：2026-11-14。本文是整个项目的开发主入口，覆盖中心、存储、agent、采集器、客户端接入、前端、联调和交付。任务进度记录在 [项目计划](../plans/development/plan.md)，接口字段详见 [API 契约](api-v2.md)。

阅读顺序：先看系统与数据链路，再看采集配置及客户端兼容条件；开发页面时继续阅读前端方案。标为“当前”的内容依据本次工作区源码，标为“目标/待实施”的内容尚未交付，不能据此判断服务器已部署。

## 1. 项目目标与当前状态

Meterly 面向 1–20 台主机、100 个以内网站，回答“哪一天、哪个网站、哪个客户端 IP 使用了多少源站流量”，同时提供主机管理、计费对账与运维操作。

| 范围 | 当前实现 | 后续开发方向 |
|---|---|---|
| 中心与存储 | Go 中心、SQLite 文件、协议 v2、数据库 schema 4 | 保持统计语义，补全发布门槛与恢复验证 |
| 采集端 | Linux agent 出站上报、配置同步、受控升级 | 验证实际采集器、断网恢复及升级状态 |
| 代理内核接入 | 一个 agent 对接一个 Clash API；另有 NetBird 与 Xray 采集器 | sing-box 实机验证、兼容端适配、多实例设计 |
| 前端 | React/TypeScript/Tailwind/shadcn 为默认入口；生产登录、真实 API、Go embed 已接入 | 管理写操作由 `/legacy/` 保留，后续逐步迁入 React |
| 部署 | Vite 产物嵌入 Go；CI、Release 和镜像自动构建 | 保持资源与二进制同版本 |
| 验证 | Go 测试、Node 单测、Playwright | 迁移时保留业务覆盖，增加类型与组件检查 |

**新前端已接入生产默认入口。** 六页查询使用真实同源 API，登录/退出沿用 HttpOnly 会话；会话失效会清理数据并返回登录。时区、版本和时钟来自 `/api/v2/me`。开发演示仅由 Vite `demo` 模式显式标记，生产不请求 `/api/preview`。主机配置/升级、服务商读数、注册码及维护写操作暂保留 `/legacy/` 入口，共享原有服务端校验；不把这些操作记作已迁入 React。生产接入计划见 [对应计划](../plans/frontend-production/plan.md)。

## 2. 架构与职责

采集链路：Linux agent → 出站 HTTPS → Go 中心 → SQLite。查询链路：浏览器 → Go 面板 API → 聚合结果。前端负责筛选、交互和呈现，统计、权限、任务状态及完整性判断由服务端负责。

```text
Linux 主机
  /proc/net/dev              → 网卡累计计数 → 增量
  sing-box/兼容内核 Clash API → 连接快照     → IP/入站/用户增量
  nginx/OpenResty 日志       → 请求记录     → 网站/IP/状态码
  NetBird / Xray             → 外部命令结果 → 对等端/入站/用户增量
                 ↓
  agent：基线与断点 → Pending → JSONL 缓冲 → 出站 HTTPS
                 ↓                             ↑ 配置/升级任务
  Go 中心：鉴权 → 校验 → 事务/高水位 → 分钟、小时、日聚合
                 ↓
  查询/CSV/计费/完整性 API → Go 内嵌面板
```

Meterly 管理自身 agent 的采集选项和升级，不提供代理订阅下发、节点切换或 sing-box 内核升级。用户说的“对接 sing-box 客户端”，在这里具体指：**读取该客户端实际使用的代理内核的统计 API，验证流量方向与维度，再接入 Meterly**。图形界面的品牌名称不能代替内核和 API 能力说明。

| 目录 | 开发职责 |
|---|---|
| `cmd/meterly-server`、`internal/server` | 中心命令、HTTP 路由、会话、分析与管理接口 |
| `cmd/meterly-agent`、`internal/agent` | 采集、发现、断点、缓冲、配置应用与升级恢复 |
| `internal/store` | SQLite 事务、聚合、查询、保留与维护 |
| `internal/proto` | 中心与 agent 的交互结构 |
| `internal/geo` | 本地 IP 归属地查询 |
| `web` | 当前静态面板及 Go 嵌入入口 |
| `tests`、各包的 `*_test.go` | 单元、集成、浏览器与受控夹具 |
| `scripts`、`deploy`、`.github/workflows` | 构建、安装、部署模板与发布流程 |

修改规则：协议字段同步检查发送端、接收端和回归；统计规则集中在后端维护；前端不得重新实现一套计费、在线状态或完整性算法。数据和状态恢复优先保护现有记录，不能用重置数据库掩盖问题。

## 3. 中心、协议与存储

### 3.1 中心职责与配置

中心入口为 [server.go](../internal/server/server.go)，配置结构见 [config.go](../internal/server/config.go)，可部署模板见 [server.example.json](../deploy/config/server.example.json)。HTTP handler 处理鉴权、输入和错误；`internal/store` 负责事务、聚合与查询；维护调度负责保留期、备份、归属地与正式版本缓存。

| 配置 | 当前用途与默认值 |
|---|---|
| `listen`、`tls_cert`、`tls_key` | 默认 `127.0.0.1:8787`；支持自身 TLS，也可由反代提供 TLS |
| `db_path` | 新部署模板使用 `/var/lib/meterly-server/meterly-v2.db`；省略时有旧路径兼容逻辑，部署应显式指定 |
| `panel_token_file` | 面板口令文件，禁止组/其他用户访问；优先于 `METERLY_PANEL_TOKEN`，非回环监听必须配置口令 |
| `agent_url` | 安装指引使用的 agent 可达地址，不是 Clash API 地址 |
| `tz`、`reset_day` | 默认 `Asia/Singapore`、9；网站/IP 日期使用中心时区，主机计费另有自己的时区与周期 |
| `offline_after_minutes` | 默认 5；中心统一计算在线状态 |
| `trusted_proxies` | 仅用于中心反代来源识别；与 agent 网站日志里的可信代理配置分开 |
| `retention` | 实际清理依据 `MinuteDetailHours`、`HourDays`；见下节 |
| `geo` | 本地库目录与更新设置；缺库不影响保存流量，归属地不可用需明确显示 |
| `maintenance` | 备份开关、目录和保留份数；默认每日备份，保留 7 份 |

面板通过 `/api/login`、`/api/logout` 与 HttpOnly cookie 管理会话；主机 Bearer token 用于 ingest/control 等 agent 路由。主机身份由 token 对应的中心记录决定，不能信任上报的 `hostname` 作为授权依据。配置中未知字段会被拒绝。

### 3.2 上报与确认

协议类型的唯一源码是 [proto.go](../internal/proto/proto.go) 与 [control.go](../internal/proto/control.go)。上报入口为 `POST /api/v2/ingest`，请求体为 `{meta,records}`，`meta.protocol` 必须为 2。正文最大 8 MiB、每批最多 5000 条；协议不匹配返回 426，限制超出返回 413。

| 字段 | 开发约定 |
|---|---|
| `meta.instance_id` | agent 状态目录中持久化的实例身份；重传必须沿用，不能每次启动随机生成 |
| `records[].seq` | 同一实例严格递增、按序上传；中心保存 `(host_id, instance_id)` 高水位，较小序号之后补发会视为重复 |
| `ts`、`prev_ts` | UTC 秒，增量覆盖 `(prev_ts,ts]`；网站记录用请求结束所在分钟的尾边界；查询范围采用 `[from,to)` |
| `kind`、`dim1`、`dim2` | 采集来源与维度；见采集器表，禁止把不同来源相加为一个总量 |
| `rx`、`tx` | 字节增量；`site_req` 是请求计数的特殊类型，不是字节 |
| `meta.collectors`、`inventory` | 当前健康、历史错误时间及设备/日志来源；不携带 secret |
| 响应 `max_seq` | 已在中心事务确认的高水位；agent 据此确认缓冲，不能按 `accepted` 条数猜测确认位置 |

事务同时处理聚合、覆盖信息和高水位。超时或写入失败后重发原批次；不要提前清理本地记录。非法记录可能被拒绝并推进确认位置，所以 HTTP 200 或 `max_seq` 前进不代表每条记录都成为有效统计，必须检查 `rejected` 与完整性原因。数值超限和整批回撤语义详见 [API 契约](api-v2.md)。

### 3.3 数据模型与保留

中心使用项目自带的纯 Go SQLite 驱动，WAL 模式，单写连接与独立读取池，无需安装数据库服务。表定义见 [store.go](../internal/store/store.go)、[analytics_schema.go](../internal/store/analytics_schema.go)。

| 表/数据 | 用途与边界 |
|---|---|
| `hosts`、`enroll_codes`、`ingest_hwm` | 主机设置与 token 哈希、注册码、实例序号确认 |
| `fact_minute`、`fact_hour` | 按主机/时间/kind/两维度保存增量；供代理、网卡等查询 |
| `fact_day` | 网卡等长期汇总；具体保留维度由写入逻辑决定，不能据此承诺长期完整 IP |
| `site_minute`、`site_hour` | 网站、IP、状态码、上行是否已知与 IP 来源的联合分布；分页排行不裁掉明细 |
| `site_day` | 网站、状态码与已知上行的 UTC 日汇总，不保留可去重的 IP 联合明细 |
| `ips`、`sites` | IP 字典及归属地索引、网站发现范围；归属地展示还会使用中心当前本地库 |
| `source_coverage`、`collection_coverage`、`data_issues` | 覆盖起止与已知缺口；不能把有一行记录理解为整个时段完整 |
| `host_config`、`upgrade_tasks` | 期望配置版本、升级任务与固定状态 |
| `maintenance_runs`、`storage_samples`、`provider_readings` | 维护记录、容量采样、人工录入服务商读数 |

当前 [Maintain](../internal/store/maint.go) 用 `MinuteDetailHours` 清理两类分钟表（默认 48 小时），用 `HourDays` 清理两类小时表（默认 365 天），不清理日汇总。`MinuteDays` 仍在配置结构和模板中，但当前清理函数不读取它，不能把它描述为另一个有效保留开关；后续需兼容处理再清理该字段。

数据库当前 schema 为 **4**，与 HTTP 协议 **2** 是不同版本。`checkSchema` 遇到不兼容已有库会拒绝启动，不自动执行跨版本迁移。若未来改变维度/表结构，需要先确定迁移或归档新建方案、回退方式与数据验证，不能只修改 `CREATE TABLE IF NOT EXISTS`。

## 4. agent 与采集器开发

### 4.1 配置与生命周期

基本配置见 [agent.example.json](../deploy/config/agent.example.json)，完整字段见 [agent/config.go](../internal/agent/config.go)。默认 `interval=60s`、`batch_size=2000`、`buffer_max_bytes=256 MiB`、`http_timeout=20s`，sing-box 默认每 10 秒采样；采样间隔必须在 1 秒与汇总间隔之间。

凭据优先级为 `token_file` → `METERLY_TOKEN`（兼容旧变量）→ `token`，生产使用权限 `600` 的 token 文件。`server_url` 使用 HTTPS；`ca_file` 用于中心证书校验，不能用于配置 Clash API 的自签证书。`allow_http` 是显式允许明文的开关，当前校验不强制限定地址范围，应只用于受信网络。

`Run` 启动时读取控制配置、做发现；运行中获取配置与升级任务，发现约每 5 分钟重做。有效采集配置由自动发现、中心期望策略、本机显式字段及 `overrides` 组合，后者优先级最高。面板不能设置 secret、任意命令或可执行路径。`discovery=false` 关闭自动套用发现建议，不代表完全不执行库存探测。

无效策略保留当前采集配置并报告 `config_error`。本机改了 JSON 文件后需重启 agent 才会重新加载基础配置；中心下发采集策略则由运行循环应用。`config_version` 表示已应用版本，不能用中心保存成功代替 agent 已应用。

### 4.2 持久化和故障恢复

| 状态文件 | 内容 |
|---|---|
| `state.json` | 协议、实例、下一个序号、网卡/代理计数基线、日志 inode/offset、Pending |
| `buffer/seg-*.jsonl`、`buffer/ack` | 待确认记录与已确认最大序号 |
| `desired.json`、`sources.json` | 最近期望配置、已发现日志描述，断网或来源消失时供恢复 |
| `upgrade-request.json`、`upgrade-result.json`、`ready.json` | 独立更新器交接与本地就绪状态 |

汇总先保存“新基线 + Pending”，再把未写入的序号追加到缓冲，最后清空 Pending 并保存。重启时补齐未写完记录；中心确认后才释放已确认分段。缓冲满会保留 Pending 并暂停继续消费，网站积压超过阈值会暂缓读日志。必须保留尚未追平的轮转日志，不能承诺已删除旧 inode 的尾部可恢复。

首次看到累计计数时只建基线；重启/清零按各采集器规则处理。不要通过删除 `state.json`、换 `instance_id` 或截断缓冲修复统计异常：这会改变计量起点或丢失待发数据。相关实现见 [agent.go](../internal/agent/agent.go)、[state.go](../internal/agent/state.go)、[buffer.go](../internal/agent/buffer.go)。

### 4.3 采集器与统计口径

| 采集器 | 输入及主要配置 | 输出 | 边界 |
|---|---|---|---|
| `netdev` | `/proc/net/dev`；`interfaces`、`counter_bits`、`max_rate_mbps` | `iface`：网卡 RX/TX | 计费指定真实网卡；多网卡可能重复经过同一流量，不能全加 |
| `singbox` | Clash API；`config_path` 或 `url` + `secret_file` | `singbox`：IP/入站；`singbox_user`：用户/入站 | RX=upload，TX=download；用户维度与 IP 维度重叠 |
| `site` | access log；`sources`、`formats`、`allow_dirs`、可信代理 | `site` 和 `SiteMetrics` | 日志上行是 `$request_length`（含请求行/头等），不是纯请求体；下行为日志记录的响应字节 |
| `netbird` | `command` 默认 `netbird`，`wg_interface` 默认 `wt0`，`timeout` 默认 8s | `netbird`：对等端 | 先 `netbird status --json`；命令失败才尝试 `wg show ... transfer`，JSON 解析失败不保证回退 |
| `xray` | `bin` 默认 `xray`，`server` 默认 `127.0.0.1:62789`，`timeout` 默认 5s | `xray` 入站、`xray_user` 用户 | 调用 `xray api statsquery`，无来源 IP；不是 sing-box Clash API |

网站手动接入片段（合入现有 agent JSON，保留身份及中心配置）：

```json
{
  "collectors": {
    "site": {
      "enabled": true,
      "sources": [{"path": "/var/log/nginx/example.access.log", "site": "example.com", "format": "combined"}],
      "allow_dirs": ["/var/log/nginx"],
      "from_start": false,
      "max_read_bytes": 8388608
    }
  }
}
```

默认从首次发现时的日志尾部开始。`from_start=true` 是显式读取现存日志的选项，不应在生产排障中随意启用；还需考虑中心接收时间限制和保留期。日志格式可使用内置 `combined/main/common`、自定义 `formats` 或来源的 `format_def`；JSON 字段及 CDN 可信代理见 [日志接入说明](cdn.md)。缺少请求长度时标记上行未知，不能填写假零。

NetBird、Xray 的片段如下，只开启目标机器实际存在且已验证的采集器。它们调用固定程序及参数，不经过 Shell；面板不负责安装这些程序或启用其统计服务。

```json
{
  "collectors": {
    "netbird": {"enabled": true, "command": "netbird", "wg_interface": "wt0", "timeout": "8s"},
    "xray": {"enabled": true, "bin": "xray", "server": "127.0.0.1:62789", "timeout": "5s"}
  }
}
```

## 5. sing-box 与其他客户端接入

### 5.1 按内核/API 确认兼容性

| 对象 | 当前可用路径 | 支持程度与限制 |
|---|---|---|
| 原生 sing-box，标准单文件 JSON | `experimental.clash_api` → `GET /connections` | 已在 dmitco 的 1.12.8 上通过实际读取与两段增量校验；该次 API 无用户字段，逐 IP 仍有未归因量，见 5.4 |
| 自定义路径、分片配置或客户端生成的 sing-box 配置 | 显式 `url`、`secret_file` | 当前不解析配置目录合并、JSONC 或客户端私有配置；不能把分片路径直接当完整 JSON |
| Clash/Mihomo 内核 | 兼容 `/connections` 的 `url`、`secret_file` | 上游有相关接口；Meterly 当前没有专用发现器、YAML 读取器或已验收的版本矩阵，属于待兼容验证 |
| sing-box 或 Mihomo 的图形客户端 | 核实其内核 API 是否开启、agent 是否可达 | GUI 有流量图不代表可供 Meterly 访问；Windows/macOS/移动端不是当前 Linux agent 的既有部署目标 |
| 只有 sing-box V2Ray API | gRPC 统计服务 | 当前 `singbox` 采集器不能读取；不能直接填写成 Clash URL，需要另行适配 |
| 一台主机多个内核/实例 | 未来独立 collector instance | 当前只有一个 `SingboxConfig` 和一份 `SBState`，未支持并行多实例 |

上游参考：[sing-box Clash API](https://sing-box.sagernet.org/configuration/experimental/clash-api/) 定义控制地址与 Bearer 鉴权；[Mihomo API](https://wiki.metacubex.one/api/) 定义 `/connections` 的全局累计字节及连接数组；[sing-box V2Ray API](https://sing-box.sagernet.org/configuration/experimental/v2ray-api/) 是另一套 gRPC 统计服务。上游提供接口不等于 Meterly 已通过兼容验收。

### 5.2 两种现有配置方式

方式 A：读取实际 sing-box 配置。下面是 **sing-box 配置的待合并片段**，不是 Meterly 配置，也不能替换原文件；示例 secret 必须换成本机生成值。对已有服务启用 API 前，先确认所用版本支持该配置，再按该服务的变更流程校验和生效。

```json
{
  "experimental": {
    "clash_api": {
      "external_controller": "127.0.0.1:9090",
      "secret": "REPLACE_WITH_LOCAL_SECRET"
    }
  }
}
```

对应 Meterly agent 片段：

```json
{
  "collectors": {
    "singbox": {
      "enabled": true,
      "config_path": "/etc/sing-box/config.json",
      "poll_interval": "10s"
    }
  }
}
```

方式 B：已有兼容 HTTP API，手动指定地址和 secret 文件。Mihomo/YAML、配置目录合并或只允许读取单独凭据的场景使用此方式。

```json
{
  "collectors": {
    "singbox": {
      "enabled": true,
      "url": "http://127.0.0.1:9090",
      "secret_file": "/etc/meterly/clash.secret",
      "poll_interval": "10s"
    }
  }
}
```

`url` 填 API 基地址，采集器自动加 `/connections`，不要重复写该路径。此方式应省略 `config_path`，否则仍会尝试读取指定的 sing-box JSON。`secret_file` 存原始 secret 文本，权限 `600`；已有文件但读不到会直接失败。非空文件内容优先于配置中的 secret；401 时重读配置并重试一次，其他失败不靠修改内核配置恢复。

采集器禁用环境 HTTP 代理，使用普通 HTTP(S) 请求，不支持 Unix socket、客户端证书或自定义 Clash CA 参数。优先在同机回环地址采集；SSH 联调可使用临时本地隧道。`0.0.0.0`/`::` 配置转换成回环访问只改变请求目标，不会收紧内核实际监听范围。Meterly 不会自动启用 Clash API，也不会修改或重启 sing-box。

### 5.3 必须验证的响应与计量行为

依据 [现有适配器](../internal/agent/singbox.go)，接入时必须检查：

1. **结构与数值**：`uploadTotal`、`downloadTotal` 以及每个连接的 `upload`、`download` 是非负累计整数字节；`connections` 是数组。只有 HTTP 200 或 JSON 可解析不够，当前 decoder 会把缺失计数字段当作零。
2. **连接身份**：连接存续期间 `id` 稳定且不同连接不复用；首个快照只建基线，下一次才产生差值。现有无 ID 回退使用 metadata 的源地址/端口/type/start，不能当作已验证的唯一标识（部分上游把 `start` 放在连接外层）。
3. **元数据**：来源 IP 顺序为 `sourceIP`/`source_ip`；入站取第一个非空的 `type`/`inbound`/`inboundTag`；用户取 `inboundUser`/`user`/`username`。`type` 存在时会遮盖单独的 tag，不能承诺分开同类型的多个入站；未读取的用户名别名也不能视为支持。
4. **流量方向**：Meterly 固定 RX=API upload、TX=API download，需以目标内核真实流量确认含义；代理载荷与网卡、网站统计有重叠和协议开销差异，不能用相等作为验收条件。
5. **连续快照**：检查全局与连接增量、ID 重复、新建/关闭、IPv6、计数回退；全局差额进入 `~unattributed`。客户端采集到的源 IP 可能只有本机/局域网地址，不代表 VPS 上所有用户的真实来源。
6. **重置与故障**：当前把全局计数下降视作重启；若重启后计数已超过旧值，单靠该规则可能识别不到。逐连接增量大于全局增量时现实现不会强行削减连接量，必须排查两者口径是否一致。

`singbox_user` 是同一载荷按用户的额外投影，不能与 `singbox` 加总。`chains` 虽被解码，目前不作为出站排行维度。用户流量未进入现有统一 IP 分析页面，也不能把“采到了用户记录”描述为已有完整用户管理产品。

### 5.4 dmitco 实机试点

**结论：此主机的 Clash API 可以由当前 Meterly 适配器读取，样本内总载荷增量守恒；逐 IP 归因不完整，API 未提供用户维度。** 这是一个具体版本的兼容验证，不代表 Mihomo 或所有 sing-box 客户端均通过。

2026-11-14 用户解锁 Bitwarden 后，通过实际 SSH 别名 `dmit-co`（或 `co`）成功连接。初次失败是本机 SSH Agent 签名受阻，已解除。

| 项目 | 实际观察 |
|---|---|
| 系统 | Debian GNU/Linux 12，Linux amd64 |
| 代理内核 | sing-box 1.12.8，包含 `with_clash_api`；`sing-box.service` 为 active/running |
| 生效入口 | `/usr/bin/sing-box`，工作目录 `/var/lib/sing-box`，通过 `-C /etc/sing-box` 加载；该目录此次只发现 `config.json` |
| 配置能力 | 23 个入站、7 个出站；Clash API 地址 `0.0.0.0:23390`，已设 secret，未配置 V2Ray API |
| 实际采样地址 | 主机自身 `http://127.0.0.1:23390/connections`，读取本机配置中的 secret 发起 GET，未输出凭据 |
| 已安装 Meterly | agent v0.4.0（21f7d0b），服务 active/running；配置 `/etc/meterly/agent.json`，自动发现开启，状态目录 `/var/lib/meterly-v2` |
| 验证代码 | 当前工作区 `ClashClient.Fetch` 与 `SingboxAcc.Apply/Drain`，临时 Linux 探针直接导入 `internal/agent`，只用内存基线，不运行 agent 常驻循环 |

结构检查样本有 171 条连接：全局和逐连接计数字段均为非负整数，`connections` 为数组，ID 非空且不重复，所有连接都有来源 IP 和入站字段。metadata 中没有 `inboundUser`、`user`、`username`，采样未产出用户记录；不能因此推断内核没有用户认证，只能判定这次响应未暴露用户统计维度。

随后于 **14:55:40–14:56:00（Asia/Singapore）** 每 10 秒取一个快照，执行当前适配器：

| 快照 | 活动连接 | 全局 upload 增量 | 全局 download 增量 | 未归因 RX / TX | 验证结果 |
|---|---:|---:|---:|---:|---|
| 1 | 164 | — | — | 0 / 0 | 首次只建基线，输出 0 条增量 |
| 2 | 169 | 114,295 B | 760,978 B | 9,314 / 668,505 B | 适配器 RX/TX 与两项全局增量分别相等 |
| 3 | 165 | 96,443 B | 80,130 B | 10,410 / 35,725 B | 适配器 RX/TX 与两项全局增量分别相等 |

两段共采集 upload **210,738 B**、download **841,108 B**；未归因分别为 **19,724 B（9.36%）**、**704,230 B（83.73%）**。第二、三次相邻快照分别观察到新建 10/6 条、消失 5/10 条连接；共享 ID 未发现来源/入站变化或计数回退。17 个入站标签都包含 `/`，本次不是只返回单个协议类型，但没有据此证明 23 个配置入站都被覆盖或与 tag 一一对应。

`~unattributed` 保住了全局载荷增量，但不能还原那些字节属于哪个 IP；短连接和关闭前最后一段是轮询缺口的已知来源，这次采样未逐条定位全部差额。**83.73% 只描述该 20 秒窗口，不是长期误差率或客户端固定属性。** 逐用户/完整逐 IP 若是验收目标，需要单独设计能够提供对应维度的统计来源，不能通过 UI 隐藏未归因桶或把差额分摊给已知 IP。

常驻 agent 的状态记录从 `next_seq=111310/ack=111309` 推进到 `next_seq=111371/ack=111370`，两次 Pending 均为 0；采样后两个服务仍 active/running。这说明当时常驻 agent 仍在推进确认状态，但本次没有直接核对中心落库、页面或 CSV，不能记为完整端到端验收。

本次未改代理/agent 配置、未重启服务、未生成额外代理负载、未替换已安装二进制；临时探针和目录已从本机及 VPS 清理。API 配置监听所有 IPv4 地址，但本次仅验证了回环访问，未判断公网可达性或修改防火墙。

后续验收范围：真实流量方向的受控样本、内核重启/计数清零、断网补发、日志轮转、中心查询与导出、升级及回滚。涉及服务中断的测试安排专门窗口；本次自然流量采样不代替这些场景。

## 6. 后端与客户端扩展流程

### 6.1 增加兼容端或新采集器

先收集脱敏响应和计数说明，再判断能否复用现有来源。兼容 `/connections` 且口径相同的客户端优先扩展字段解析与发现，不仅凭品牌名新增 `kind`。

| 步骤 | 必须同步的部分 | 最小验收 |
|---|---|---|
| 定义能力 | 来源、RX/TX、可用 IP/入站/用户、重置特征、缺失值 | 不宣称端点没有的维度 |
| 配置与发现 | 配置结构、默认值、校验、脱敏、显式覆盖、发现建议 | 原有配置仍可读取，停用优先 |
| 累计与持久化 | 基线、连接身份、差值、Pending、独立实例状态 | 首次不灌历史，重传与恢复不重计 |
| 协议与存储 | `proto`、kind 白名单、维度规范化、整数上限、表/索引及版本策略 | 合法数据可查，坏批次不污染累计 |
| API 与页面 | 查询过滤、分页总量、来源标签、CSV、完整性原因 | 缺失维度明确不可用，页数不改变总量 |
| 回归与文档 | 单测夹具、API 回归、实机记录、配置样例 | 模拟验证与真实兼容结果分别记录 |

多实例需要新增独立的 **collector 实例标识**；不能把目前用于上传去重的 `meta.instance_id` 直接当代理实例。应让每个代理拥有独立配置、计数基线和健康信息，并同步数据库键、API 筛选与面板。仅启动多个 agent 共用一个 host token 会让网卡/网站重复计量，不能作为多实例的默认方案。

### 6.2 修改 API 和存储

从 [路由注册](../internal/server/server.go) 找 handler，再查对应 store 查询，先确认请求/响应与鉴权边界。新增字段优先兼容添加；统计意义、JSON 数值精度、schema 或去重规则变化需先写明版本与迁移策略。前端和 agent 类型不能靠强制类型断言掩盖不一致。

查询始终绑定参数、限制时间范围/分页，排序覆盖完整结果集；CSV 在中心完整生成。按修改范围选 agent/server/store 的对应测试，数据异常必须验证恢复后仍能继续处理下一批。现有保护约定见后文“后端变更约定”，详细数值规则集中在 [API 契约](api-v2.md)。

## 7. 联调、排障与运维

### 7.1 分层验证

| 层级 | 操作 | 能证明什么 |
|---|---|---|
| 配置 | `meterly-agent validate -config ...` | 配置字段、权限、token 读取和状态协议；不证明 API 连通 |
| 发现 | `meterly-agent detect -only singbox -json` | 发现建议；不证明代理已启用 API |
| 采集 | `meterly-agent check -config ...`，使用独立临时状态目录 | 当前启用采集器的摘要；不上传，但初始化会打开/修复缓冲，不能与生产进程共用状态目录 |
| 上报 | 在测试中心运行 agent，检查 `accepted/rejected/max_seq` 与积压 | 身份、事务确认和补发；首次基线没有流量记录属正常情况 |
| 查询 | 用相同 host/source/time 查排行、趋势及 CSV | 统计口径、窗口和导出一致性；不是服务商账单验收 |
| 故障 | 测试环境断网、日志轮转、服务中断 | 恢复行为；Linux/systemd 与持久化场景须实际验证 |

`check` 根据传入的显式配置初始化，不执行 `Run` 的完整自动发现/控制同步；对只含 `discovery=true` 的最小安装配置，不能把它作为全部采集器的兼容验收。现有 sing-box 单测可用 `go test ./internal/agent -run 'Test(Singbox|ClashClient)' -count=1` 定向运行，范围包括基线、短连接、重启、重复 ID、IPv6、用户和配置凭据读取。

### 7.2 常见问题

| 现象 | 检查顺序 |
|---|---|
| SSH 别名解析失败 | 检查 `~/.ssh/config` 的 Include 与 Host 名，不猜公网地址 |
| 公钥被接受但登录失败 | 检查本机 SSH Agent 是否允许签名；Bitwarden 锁定或未允许密钥使用时需用户处理 |
| Clash API 拒绝连接/404/401 | 依次核对已生效监听、网络命名空间、基地址和接口、secret；不输出认证头或配置全文 |
| HTTP 200 但统计为零 | 检查必需字段、是否首次基线、是否有实际代理流量、是否只读到空闲内核 |
| 用户/入站缺失或全部合并 | 核对真实 metadata 字段、适配器优先级及协议能力，不从 IP/端口猜用户 |
| 上报积压或 503/429 | 检查中心写入/磁盘与退避状态，保留缓冲和日志；401 核对 host token，426 核对成套版本 |
| 历史显示不可用或增长率为空 | 检查保留期、精度、覆盖、缺口、未知上行与数值超限，不能补零掩盖问题 |
| 升级长期 running | 查询中心任务、本地 request/result、更新器 ActiveState/Job；真实主机状态确认前不删除现场 |

### 7.3 备份、恢复与发布

中心通过 SQLite `VACUUM INTO` 生成一致性副本，再压缩为 `meterly-时间.db.gz`。系统页可启动备份；自动调度按中心时区每天 04:20 到期运行。只复制正在使用的 `.db` 主文件可能漏掉 WAL 中的数据，不作为备份流程。

恢复演练的目标是独立回环实例：从选定备份解压到**新的私有测试目录**，复制兼容的中心配置并修改 `db_path`、监听端口、备份目录，关闭自动备份和归属地下载，使用单独的测试口令。查询代表性日期/主机/网站、配置版本和缺口，再比较备份时间与实际可恢复时间。备份包含主机 token 哈希、配置和历史统计，不能给公网测试实例开放真实 agent 写入。

正式恢复需先停止中心、保留原 `.db` 及相关 WAL/SHM，按已验证步骤替换并检查权限；旧高水位恢复后，agent 可能已清理备份时间之后的确认记录，不能承诺重传补齐全部历史。备份也不包含 agent 状态、中心口令文件和所有部署配置，这些需单独保护。当前文档给出恢复流程，真实备份恢复演练仍待实施。

发布产物由 [build.sh](../scripts/build.sh)、[package.sh](../scripts/package.sh) 生成，包含双架构二进制、完整包与校验和。当前 [CI](../.github/workflows/ci.yml) 执行测试；[Release](../.github/workflows/release.yml) 在标签推送后直接构建发布，尚未以同一提交的全部检查成功作为发布前提。后续先补齐门槛，再安排发布与试点部署。

## 8. 前端技术方案

原则：**选择最新稳定技术，复用官方现行组件，通过业务组合完成页面。** 框架与组件版本、API 写法一起更新；具体候选版本及来源列在文末，安装时再次核对兼容性。

| 层级 | 选型 | 负责内容 |
|---|---|---|
| 应用 | React 19 + TypeScript 7 | 函数组件、严格类型、可读 TSX |
| 构建 | Vite 8 + React 官方插件 | 开发服务器与静态资源打包 |
| 样式 | Tailwind CSS 4 | 布局、响应式、主题变量 |
| 基础组件 | shadcn/ui + Base UI | 控件、弹窗、导航及交互基础 |
| 编译优化 | 稳定版 React Compiler | 适用组件和 Hooks 的自动缓存优化 |
| 路由 | TanStack Router | 路由、类型化 URL 参数、返回恢复 |
| 服务端状态 | TanStack Query | 请求、缓存、取消、刷新与失效 |
| 表格 | TanStack Table + shadcn Table | 列定义、排序、筛选、服务端分页 |
| 图表 | shadcn Charts + Recharts 3 | 趋势、坐标、提示与图例 |

工程约定：

- 延续 npm，一份根 `package.json` 和 `package-lock.json`；CI 使用 `npm ci`，构建过程不临时拉取 `latest`。
- 本地与 CI 固定一致的受支持 Node LTS。当前 CI 使用 Node 22；迁移时核对全部依赖要求，再决定统一版本。
- shadcn 通过官方 CLI / registry 引入，保留 `components.json`、组件源码和必要来源说明。CLI 版本与组件源码版本分开记录。
- 开启 TypeScript 严格模式；类型检查独立于 Vite 构建。React Hooks / Compiler 检查接入项目 lint。
- React Compiler 采用安装版本支持的稳定 Babel 路径。Vite 文档中的 Rust Compiler 接入仍标为实验性，不作为默认配置。[React Compiler](https://react.dev/blog/2025/10/07/react-compiler-1)、[Vite 插件说明](https://github.com/vitejs/vite-plugin-react/blob/main/packages/plugin-react/README.md)

## 9. 本地开发与命令

Go 版本以 [go.mod](../go.mod) 为准。当前面板入口由 [web/embed.go](../web/embed.go) 嵌入；现有 npm 命令以 [package.json](../package.json) 为准。

### 9.1 当前可用流程

```sh
npm ci
npm run build
go run ./tests/ui-server
```

第二条命令启动回环地址 `127.0.0.1:18787` 上的受控 UI 夹具，使用临时 SQLite 文件与测试数据，适合检查内嵌生产页面。测试面板口令为 `local-browser-test-token`，仅用于独立回环夹具，不能用于部署。退出后可运行以下测试：

```sh
npm test
npm run test:browser
```

Playwright 首次准备浏览器可运行 `npx playwright install chromium`。[浏览器测试配置](../playwright.config.js) 会自行启动同端口的夹具，运行前应停止手动启动的进程。常规 Go 构建使用 `scripts/build.sh`，完整构建说明见 [README](../README.md)。

本机不安装数据库服务、不运行或安装 Docker。若使用 Python 辅助开发，统一通过 `uv`；大型 Python 项目使用虚拟环境。

### 9.2 新前端预览

执行 `npm ci` 后，在两个终端分别运行 `npm run preview:api` 和 `npm run dev`，打开 `http://127.0.0.1:5173`。前端仅将 `/api` 代理到 `127.0.0.1:18788`，不使用生产中心或生产凭据。Go 夹具位于 [preview-server](../tests/preview-server/main.go)，将独立 SQLite 文件放在系统临时目录，正常退出后清理；不安装数据库服务。

演示数据固定在 2026-11-14 15:00（Asia/Singapore），含 5 台主机、6 个网站、90 个网站来源 IP，以及离线、补发、未知上行与未归因示例。面板明确标注“演示数据”；统计通过现有服务端生成，不在浏览器伪造生产查询结果。

已使用 React 19.3.1、TypeScript 7.0.2、Vite 8.3.8、Tailwind 4.3.5、Base UI 1.9.0、TanStack Router/Query、Recharts 3.10.1 和 Babel React Compiler 1.0.0。官方 UI 源码通过 `shadcn@4.23.0 add` 获取，风格为 `base-nova`，配置保存在 [components.json](../components.json)。CLI 现行源码使用 `cn` 包；业务代码在 `components/meterly`，避免把业务请求塞进官方组件。

`npm run build` 包含 TypeScript 严格检查并输出 `web/dist`，`npm run preview:frontend` 可在 API 夹具运行时预览这些静态产物（端口 4173）。当前单个应用包仍有 Vite 大于 500 kB 的提示，完整迁移阶段再按页面/图表拆分；不把已完成的预览包装成生产性能验收。

### 9.3 工程命令与剩余接入

| 命令 | 目标职责 | 当前状态 |
|---|---|---|
| `npm run dev` | Vite demo 模式，代理本地演示 Go API | 已有；显式演示标记 |
| `npm run typecheck` | TypeScript 检查 | 已有 |
| `npm run lint` | 项目静态检查，默认不自动修复 | 待新增 |
| `npm run build` | `frontend/` → `web/dist/`，复制经典管理资源 | 已接入 Go embed、脚本、CI、Release 与镜像 |
| `npm test` | 路由、数据格式化等单元回归 | 已有；迁移时适配新源码 |
| `npm run test:browser` | 构建后启动带真实登录的 Go 夹具 | 覆盖 React 生产页与经典管理页 |

Vite 代理应覆盖登录、退出和 `/api/v2` 请求，沿用同源会话行为；开发环境也不绕过鉴权。

## 10. 目标工程结构

以下为完整迁移结构，其中 `frontend/src`、根工程配置、生产会话与 `web/dist` 构建已落地；细分 feature/routes 与管理写操作迁移继续按任务推进。`frontend` 保存开发源码，`web/dist` 保存生成产物并由 Git 忽略；旧面板通过 `web/dist/legacy` 保持管理操作可用。

```text
components.json
package.json / package-lock.json
vite.config.ts / tsconfig.json
frontend/
  index.html
  src/
    main.tsx
    app/                  # providers、路由配置、应用布局
    routes/               # 页面入口，组合对应 feature
    components/ui/        # 官方组件源码
    components/meterly/   # 跨页面的业务组件
    features/             # analysis、hosts、billing、system 等业务
    lib/                  # API 客户端、类型、参数、格式化
    styles.css            # Tailwind 与主题变量
web/
  embed.go
  dist/                   # 构建生成的 HTML / JS / CSS / 资源
tests/
  browser/
  ui-server/
```

页面入口负责组合，不堆积所有请求和业务判断。官方组件与业务组件分开；公共能力确认被多处使用后再抽取，避免为小规模项目引入无用抽象。

## 11. 页面与组件开发

### 11.1 页面范围

| 页面 | 必须完成的业务 |
|---|---|
| 总览 | 当前范围指标、网站/IP 排行、趋势、等长比较、采集告警 |
| 网站 | 搜索/排序/分页、网站详情、状态码与完整 IP 排行 |
| IP | IP/CIDR/国家/ASN 筛选、归属地、关联网站或入站、主机与时段分布、CSV |
| 主机 | 全局主机筛选、真实网卡、采集器、发现来源、配置版本、积压与升级 |
| 对账 | 主机计费周期、配额预测、服务商读数、vnstat 差异 |
| 系统 | 安装指引、正式版本、升级/维护记录、备份、容量与告警 |

### 11.2 组件复用

| 需求 | 组件 |
|---|---|
| 导航与页面骨架 | Sidebar、Sheet、官方 Blocks |
| 指标与分组 | Card、Badge、Separator |
| 筛选和表单 | Date Picker、Combobox / Select、Input Group、Field |
| 排行与趋势 | Table、Pagination、Chart、Tooltip、Legend |
| 编辑与确认 | Dialog、Alert Dialog、Button |
| 加载和反馈 | Skeleton、Spinner、Alert、Empty |

按钮、弹窗、下拉、日期选择、焦点管理与键盘行为使用官方组件。自定义代码主要用于布局、表格列、API 连接和业务提示。shadcn 的 [Data Table](https://ui.shadcn.com/docs/components/base/data-table) 是组合指南，需要连接 Meterly 的服务端分页；它不是免业务代码的完整表格。

采用 [Base UI 组件体系](https://ui.shadcn.com/docs/changelog/2026-07-base-ui-default)，按 [Vite 接入文档](https://ui.shadcn.com/docs/installation/vite) 配置。引入 Blocks 后移除演示数据与不适用的框架绑定；修改官方源码需记录原因，避免升级覆盖业务行为。

### 11.3 视觉与交互

- 使用 [Tailwind CSS 主题变量](https://tailwindcss.com/docs/theme) 统一颜色、间距、圆角和图表色，支持深色、浅色、跟随系统；统一图标、排版与数值单位。
- 数值列便于纵向比较，长 IP/域名不能挤走主要操作；375 px 窄屏覆盖筛选、菜单、表单和弹窗，横向滚动限制在表格区域。
- 控件有标签、错误说明与可见焦点；弹窗支持键盘操作和关闭后焦点恢复；状态同时提供文字。动画遵循减少动态效果偏好，按交互需要使用。
- 按 [Recharts 3 图表示例](https://ui.shadcn.com/docs/components/base/chart) 设置容器高度或宽高比。统一中心时区、单位和 tooltip；缺失点保留断点，不用演示数值补齐。

## 12. 状态管理与 API 规则

### 12.1 URL 与页面状态

已应用的日期、主机、来源、IP、排序和分页保存在 URL；输入草稿、展开项、弹窗开关保存在局部状态。通过 TanStack Router 的 [参数校验](https://tanstack.com/router/latest/docs/guide/search-params) 统一解析、默认值和非法参数处理。

首轮迁移沿用 hash history，兼容现有 `#/...?...` 链接及 IPv6 编码。保留“日期→网站→IP→详情→返回”的条件、页码与滚动；IP 反查网站保留 IP、状态码等适用条件。

选择项即时应用；文本输入停顿 **450 ms** 后应用，支持中文输入法组合态，刷新结果不打断焦点。非法 IP/CIDR、国家代码、ASN、状态码和日期显示校验错误，不发送请求。筛选变化回到第一页；日期/周期变化将粒度重置为 `auto`，单独选择粒度则保留用户选择。

### 12.2 请求与缓存

- TanStack Query 统一管理服务端状态；查询键包含改变响应的全部筛选、分页与详情标识。
- 查询函数将 [AbortSignal](https://tanstack.com/query/latest/docs/framework/react/guides/query-cancellation) 传给 `fetch`。切换条件后旧响应不能覆盖新页面，错误响应不能作为正常数据缓存。
- 历史范围不定时刷新，当前范围按既有规则刷新；明确配置轮询、窗口焦点及重连刷新，避免库默认行为改变历史页面或产生重复定时器。
- API 客户端统一处理 HTTP 状态、JSON 与会话。401 进入登录流程；409 提示配置冲突并重新读取版本。重试按错误类型设置，写操作不盲目重试。
- 写操作在用户事件中发起，成功后失效相关查询；提交中防止重复操作，结果不明确时先查询已有任务。`useEffect` 用于外部同步和清理，不维护第二套常规请求状态机。
- 函数组件和 Hooks 遵循 React 规则。Compiler 优化与第三方组件兼容性实际验证，不机械添加或删除全部 `useMemo` / `useCallback`。

### 12.3 业务展示契约

| 场景 | 统一规则 |
|---|---|
| 服务端分页 | `page` 从 1 开始，表格内部页码在适配层转换；排序作用于全部匹配数据，`count` / `totals` 来自服务端，不从当前页重算 |
| 时间与比较 | 网站/IP 使用中心时区；计费使用主机时区和周期；趋势采用服务端范围与粒度；`growth_pct=null` 显示不可比较，不改为 0% |
| 数据完整性 | 区分真实零、未采集、部分缺口、明细过期和未知上行；保留 `meta` 原因、保留期与精度提示 |
| 数值超限 | `overflow` 或保留上限值展示“超出统计范围”，图表留缺失点；超过 JavaScript 安全整数范围不宣称精确，精确导出使用服务端 CSV |
| 主机状态 | 使用服务端 `online`，无心跳显示待上报；列表遵循全局 host，详情路径指定主机优先；网卡选项来自 inventory |
| 配置与会话 | 编辑携带读取到的配置版本；沿用 HttpOnly cookie，管理写入遵循 JSON 契约，保留升级/维护状态反馈 |
| 来源与导出 | 源站日志、sing-box 载荷、网卡计费分别展示；CSV 导出全部匹配结果，保留筛选、错误状态及下载流程，不用当前页拼装 |
| 归属地 | 省市、ASN、组织与地址类型使用中心返回值；缺库或无匹配不丢流量，也不推测位置 |

字段类型要明确，外部输入仍做运行时校验；TypeScript 类型断言不能替代数据检查。更改 JSON 精度或统计语义须同步设计 API，不在 UI 内静默修正。

## 13. 后端变更约定

开发后端时必须保护下列现有行为，完整边界以 [API 契约](api-v2.md) 和正式回归测试为准：

- 上报按批次、序号和事务确认，高水位只在事务完成后确认；重传不能重复计量。缓冲压力与写入失败不能静默丢弃待确认记录。
- 聚合检查批内、跨批和查询累加范围；数值超限记录缺口，不能把异常值当精确用量或通过删除历史数据恢复页面。
- 比较两期使用共同可用精度，小时查询的部分边界使用可用分钟数据；精度或覆盖不足时明确不可比较。
- 采集器历史错误与当前故障分开；部分网卡不可用时报告异常，同时处理其余可用数据。在线判断统一由中心执行。
- 配置保留版本冲突、本机覆盖、停用优先和无效配置回退。升级恢复检查真实服务状态及排队任务，保留未确认结果和回滚备份。
- 注册、上报和控制请求保护凭据及重定向边界；面板会话和 agent token 保持各自访问范围。

## 14. 构建与交付

当前构建顺序：**安装锁定依赖 → 类型检查 → Vite 静态产物 → Go embed → 二进制或版本包**；独立 lint 仍待补充。生产不需要 Node；页面、图表和必要字体等运行资源随中心提供。

1. Vite 输入为 `frontend/`，输出为 `web/dist/`；配置 TS、Vite 和 shadcn 的一致别名。清理产物不能误删 `web/embed.go` 或旧面板源码。
2. Go 通过子文件系统把 `dist` 作为静态根，正确加载 `/` 及资源路径；API 路由继续独立处理。
3. 本地脚本、浏览器夹具和发布流程在 Go 构建前生成前端。缺产物明确失败；从干净检出验证，不依赖开发机遗留的 `dist`。
4. `web/dist` 不入库。Dockerfile 的 Node 阶段从锁文件与源码生成前端，再复制到 Go 阶段；本机产物和依赖不进入构建上下文。脚本、Makefile 和 CI 在 Go 编译前执行前端构建，不临时运行组件生成器。
5. `/` 已切换到 React，旧 ES Modules、CSS 和 Chart.js 复制至 `legacy/` 以保留尚未迁移的管理写操作；未完成迁移前不可删除。新面板使用 Zod jitless 模式，兼容现有禁止 eval 的 CSP。

部署方式、安装包与运行操作见 [README](../README.md)、[部署模板说明](../deploy/compose/README.md)。旧版切换见 [版本切换说明](migrate.md)，源站日志和代理归因见 [日志接入说明](cdn.md)。这些是仍有效的操作参考。

## 15. 实施顺序与验收

实施按 [plan.md](../plans/development/plan.md) 推进，完成一项并通过最小必要验证后标记 ✅。项目分为客户端实机验证、后端/发布恢复完善、前端迁移三条工作线。开发文档、dmitco API 试点、六页预览以及新前端生产构建接入已经完成；管理写操作的 React 迁移、独立 lint 和剩余边界行为继续按项目计划逐项验收。

| 改动范围 | 最小必要验证 |
|---|---|
| 文档整理 | 链接、失效引用、差异及空白检查 |
| 依赖/工程配置 | 项目类型检查、lint、构建和关键组件/Compiler 接入检查 |
| 页面/交互 | 对应单元与浏览器回归；验证真实行为，不只检查组件存在 |
| Go/API/存储 | 对应包测试、必要的竞态与集成检查 |
| 构建切换 | 干净构建、Go 嵌入资源访问、登录/刷新及六页关键流程 |
| 脚本/配置 | 按文件类型运行 shellcheck、actionlint、taplo 等适用检查 |

已有关键测试入口：[路由与参数](../tests/router.test.js)、[数值展示](../tests/components.test.js)、[浏览器链路](../tests/browser/analysis.spec.js)、[agent 回归](../internal/agent/review_fixes_test.go)、[中心回归](../internal/server/review_fixes_test.go)、[存储回归](../internal/store/review_fixes_test.go)。迁移测试入口时保留原有行为断言。

前端切换至少覆盖：登录/退出、日期→网站→IP→返回、刷新与前进后退、输入焦点与中文防抖、请求竞态与错误恢复、服务端分页、主机筛选和详情优先级、数据不可用状态、三种主题及窄屏；管理页补充版本冲突、提交中状态与任务查询。

项目声明的检查优先，默认只读；不进行无关全仓扫描或自动格式化。完成目标且最小必要验证通过后停止。交付分别说明本地结果、提交、发布、部署和真实环境验收，未执行的层次不得标为通过。

## 16. 尚待安排的工作

以下事项从旧待办中保留，尚未完成；实施前按当前源码、环境和需求重新确认，不能把旧审查结论当成最新扫描结果。

| 事项 | 下一步 |
|---|---|
| 独立归属地夹具工具 | 检查 `tools/mkgeofixture` 的依赖与单独模块验证；旧报告提到的 Windows 依赖公告不直接等同于 Linux 生产漏洞 |
| CI 与发布约束 | 评估 action 固定 SHA、凭据持久化、缓存/测试门槛及现有 Shell 静态告警 |
| 配置与恢复 | 兼容清理未生效的 `MinuteDays` 等字段；按本文流程执行独立备份恢复演练 |
| 演示工具与可读性 | 核对 demoseed 的协议标记和用途；随相关功能改动改善复杂函数，前端长行由迁移处理 |
| 真实环境验收 | 独立工具模块、真实 DB-IP 库、Linux/systemd/Caddy/iptables、实际采集器与远端发行资产均需对应环境验证 |
| 故障与长期运行 | 实际包转发、断电持久性、夏令时/半小时时区及长期资源压力仍需单列验收 |

## 附录：依赖版本快照

来源为前一轮选型时查询的 **2026-11-14 npm `latest` 元数据**。本轮整理沿用该快照，没有重新安装或测试；版本也不代表相互兼容。开始实施时检查 peer dependencies、对应大版本文档和官方组件示例，再写入锁文件。

| 包 | 候选正式版本 |
|---|---|
| [react](https://registry.npmjs.org/react/latest) | 19.3.1；`react-dom` 保持同版 |
| [typescript](https://registry.npmjs.org/typescript/latest) | 7.0.2 |
| [vite](https://registry.npmjs.org/vite/latest) / [@vitejs/plugin-react](https://registry.npmjs.org/@vitejs%2Fplugin-react/latest) | 8.3.8 / 6.3.0 |
| [tailwindcss](https://registry.npmjs.org/tailwindcss/latest) | 4.3.5 |
| [shadcn](https://registry.npmjs.org/shadcn/latest) / [@base-ui/react](https://registry.npmjs.org/@base-ui%2Freact/latest) | CLI 4.23.0 / 1.9.0 |
| [babel-plugin-react-compiler](https://registry.npmjs.org/babel-plugin-react-compiler/latest) | 1.0.0 |
| [@tanstack/react-router](https://registry.npmjs.org/@tanstack%2Freact-router/latest) | 1.170.46 |
| [@tanstack/react-query](https://registry.npmjs.org/@tanstack%2Freact-query/latest) | 5.107.1 |
| [@tanstack/react-table](https://registry.npmjs.org/@tanstack%2Freact-table/latest) | 9.3.1 |
| [recharts](https://registry.npmjs.org/recharts/latest) | 3.10.1 |

这组 Vite 与 React 插件的 Node 要求是 `^20.19.0 || >=22.12.0`。Table 等跨大版本依赖尤其要确认官方示例适用版本，不能直接混用旧示例与新 API。
