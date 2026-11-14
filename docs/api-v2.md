# v2 API 与数据契约

所有分析/管理接口位于 `/api/v2`。面板通过 `/api/login` 和 HttpOnly 会话 cookie 登录；主机 token 仅用于 agent 路由，不能访问面板。写入管理接口需 JSON。中心确认 `max_seq` 的前提是统计事务已提交，agent 只在确认后清理缓冲。

系统架构、各采集器配置、客户端兼容条件和扩展步骤见 [项目开发文档](development.md)。HTTP/上报协议版本为 2，当前数据库 schema 为 4；两者独立，不兼容旧库或旧 agent 状态不能通过修改版本号强行使用。

## agent 上报约定

`POST /api/v2/ingest` 使用对应主机的 Bearer token，请求为 `{meta,records}`。`meta.protocol=2`，`meta.instance_id` 持久化在 agent 状态中；记录 `seq` 在实例内递增并按序发送。去重键是主机与实例的序号高水位，低于已确认高水位的迟到记录视为重复；它不是多个代理内核的实例维度。

| `kind` | `dim1` / `dim2` | RX / TX |
|---|---|---|
| `iface` | 网卡 / 空 | 操作系统接收 / 发送字节 |
| `singbox` | 来源 IP 或 `~unattributed` / 入站 | Clash API upload / download 增量 |
| `singbox_user` | 用户 / 入站 | 与 singbox 重叠的用户投影，不能相加 |
| `site` | 网站 / 客户端 IP | 已知请求长度 / 响应字节，附 `SiteMetrics` |
| `site_req` | 兼容请求计数，详见 proto 定义 | RX 是请求数，TX 为 0；新网站采集使用 `SiteMetrics.requests`，不重复上报 |
| `netbird` | 对等端标识 / 对等端 IP | 对等端接收 / 发送增量 |
| `xray`、`xray_user` | 入站 tag 或用户 / 可选维度 | uplink / downlink，无来源 IP |

正文上限 8 MiB，每批最多 5000 条。`ts/prev_ts` 是 UTC 秒，增量覆盖 `(prev_ts,ts]`。响应包含 `accepted`、`duplicates`、`rejected`、`max_seq`、`host`；有拒绝记录也可能返回 200 并推进确认位置，必须同时读取拒绝数和数据缺口。协议不匹配返回 426、超限返回 413、写入失败返回 503；网络失败重发原批次，不能重新编号。

当前统一 IP 分析只提供 `source=site|singbox`；`singbox_user` 可被采集不代表已有独立用户分析页面。其他内核经兼容 `/connections` 接入后沿用 `singbox` 来源名，不能据此认定其内核就是 sing-box。兼容验收要检查必需计数、稳定连接 ID 和实际元数据，而不仅是 HTTP 200。

## 分析

| 路径 | 用途 |
|---|---|
| `GET /overview` | 当前范围总览、网站/IP Top 5、等长比较、网卡独立口径、告警 |
| `GET /analytics/options` | 主机、网站和中心时区候选项、`geo` 数据库状态 |
| `GET /analytics/rank` | 联合筛选、总量与分页排行 |
| `GET /analytics/series` | 相同筛选下的分钟/小时/日曲线 |
| `GET /analytics/export` | 相同筛选下的全部 CSV 行，不受 page_size 限制 |
| `GET /series` | 网卡等采集器曲线 |
| `GET /billing/overview` | 各主机自身周期的配额和预测 |
| `GET /reconcile`, `/provider` | 来源对账和服务商记录 |

分析参数：

- `period=today|yesterday|last7d|last30d|last1h|current|previous|day|custom`；单日传 `start=YYYY-MM-DD`，自定义同时传含尾日的 `end`。
- 或 `from` / `to` UTC 秒，区间 `[from,to)`；一次最多查询 800 天。
- `host=all|主机名`、`source=site|singbox`、`site=规范化域名`、`ip=IP或CIDR`、`country=国家代码`、`asn=编号或AS编号`、`status=100..599`、`search=维度关键字`。
- `group=ip|site|host|status|country|asn|inbound`；网站/状态只适用于 site，入站只适用于 singbox。
- `sort=total|rx|tx|requests|error_rate|key`、`order=asc|desc`、`page` 从 1 开始、`page_size=1..500`、`gran=auto|minute|hour|day`。

例如：`/api/v2/analytics/rank?period=day&start=2026-11-10&site=example.com&group=ip&sort=total&page=2&page_size=50`。

返回 `rows`、筛选后 `totals`、`count` 和 `meta`。`meta` 包含实际对齐后的 UTC 范围、中心时区、存储精度、图表粒度、可用起点、最近采集、保留期、完整性与原因。精度不足会明确报告对齐；完整 IP 过期返回 `state=expired` 和不可用总量，CSV 返回 410。网站长期日汇总不含可去重的 IP 明细，`ips_available=false`。`rx_known=false` 时上行不能当作零。

当天与昨天相同已过时长比较；其他范围与紧邻的上一等长区间比较。当前范围以已结束分钟为查询上界，未完成桶或保留精度不足时不承诺秒级精确。跨时区汇总使用 UTC 小时明细；超出明细期限的长期日汇总按其实际桶对齐并标记精度限制。

比较双方统一使用仍在保留期内的分钟或小时数据。`comparison_resolution` 返回比较粒度，`comparison_complete` 表示双方覆盖、边界及数据缺口检查均通过；任一方不完整、数值超限或上期为零，`growth_pct` 返回 null。跨分钟保留边界时可回落到小时；不足整小时且历史分钟不可用时不计算增长率。

## 管理与采集

| 路径 | 方法与说明 |
|---|---|
| `/hosts` | GET：主机、inventory、health、期望/已应用配置版本、中心计算的 `online` |
| `/hosts/{host}` | PUT：计费设置，传 `version`（读取的 `settings_version`）；并发冲突返回 409 |
| `/hosts/{host}/config` | PUT：`{version,policy}`，版本不一致返回 409 |
| `/hosts/{host}/upgrade` | POST：`{version:"vX.Y.Z"}`；只接受已发布正式版本，每主机一个活动任务 |
| `/agent/control` | GET，host token：期望配置与待执行升级任务 |
| `/agent/upgrade/status` | POST，host token：固定状态，重复确认幂等 |
| `/ingest` | POST，host token：`{meta:{protocol:2,instance_id,...},records:[...]}` |
| `/enroll` | 面板创建/查看/删除一次性注册码 |
| `/enroll/redeem` | POST：JSON 正文核销注册码，不通过 URL |
| `/system`, `/upgrades`, `/releases`, `/install` | GET：维护、容量、升级状态与安装指引 |
| `/system/jobs/{kind}` | POST：backup、retention、geo、release；仅固定操作 |

网站记录 `kind=site`，`dim1=网站`、`dim2=客户端IP`，`site={status,requests,rx_known,ip_source}`，RX/TX 为增量。TS 表示日志分钟的结束边界，服务器写入其对应分钟；序号在实例内递增并按顺序发送。重复序号不再次计量。新库的 site_minute/site_hour 保存联合分布，IP 字典规范化地址并复用本地归属地。

前端每页只取分页结果，趋势由 SQLite 聚合，不把全年明细传入浏览器。CSV 先在中心临时文件中完整生成，成功后提供下载；失败不会以正常 CSV 返回部分结果。

## 数值、完整性与展示

- IP 行在原有国家、ASN、运营商字段外，返回 `region`、`city`、`scope`。省市和地址类型由中心当前本地归属地库补全；缺失时为空，不推断位置。
- `GET /analytics/options` 的 `geo` 包含归属地库当前状态，供面板说明数据来源和可用性。
- `GET /series?gran=hour` 的点时间为小时桶起点。完整小时用小时表；两个不足一小时的边界用分钟表，只计入所选区间。边界分钟已过期或没有对齐到分钟时返回 400，需改为整点区间。
- 合法单条增量仍需满足 RX/TX 各小于 1 PiB、网站请求数小于 2^40。每个累计桶的 RX+TX 及请求数必须小于 **2^62−1**。若批内或跨批累计超限，撤回该批全部新记录的事实、网站及覆盖写入，返回 `accepted=0`、`rejected=该批非重复记录数`，同时提交数据缺口与高水位；重复重传不会再写入，后续健康批次可正常处理。未溢出批次的单条非法记录仍按原规则跳过。
- 查询以整数求和，达到 **2^62−1（4611686018427387903）** 时返回该保留值作为不可用标记，不把超限结果当作精确用量。旧库里的负数/REAL 异常计数也标记不可用，不自动删改历史。分析指标新增 `overflow`；为 true 时 `meta.complete=false`、原因中说明数值超限，依赖该指标的比例/增长不可用。原网卡、计费接口的用量也遵循同一保留值约定；前端显示“超出统计范围”，曲线跳过超限值。
- `collectors.*.last_err/err_at` 保留最近一次错误供诊断，`fails=0` 表示已恢复。中心按 `err_at` 记录历史缺口并去重；旧 agent 缺失 `err_at` 时仅对活动失败使用 `sent_at`（再缺失才用接收时间），不会把恢复后的旧错误重复登记到当前窗口。部分网卡缺失会报异常，同时继续保存可用网卡的增量。
- 主机列表、总览、计费页及离线告警统一使用中心时间和 `offline_after_minutes`。到达阈值即离线；没有心跳为待上报。主机管理列表使用全局 host 筛选，详情 URL 指定的主机优先。

CSV 按顺序输出：维度、源站上行/上传字节、源站下行/下载字节、已知流量合计、请求数、错误请求、国家、ASN、运营商、开始UTC秒、结束UTC秒、数据完整、省份/地区、城市、地址类型、数值超限。最后四列为追加字段；超限数值写“超出统计范围”，对应行数据完整为 false。

## 升级状态与网络边界

升级任务状态为 pending、running、succeeded、failed、rolled_back。自创建起 **24 小时**仍未完成的任务，在获取控制任务、查询升级列表或创建新任务时转为 failed 并释放活动槽位；反复上报 running 不延长这个期限。过期后的迟到状态只确认收到，不覆盖中心已保存的过期终态。过期表示未完成确认，不表示主机安装结果已验证。

agent 优先处理持久结果。running 或仅有 request 的文件存在至少 30 秒后，使用 systemd ActiveState 和 Job 检查更新器；确认 inactive/failed 且没有排队任务才落盘中断失败。活动状态、排队任务和查询失败时保留现场。终态由中心确认后再清理请求/结果文件，不重新执行旧任务，也不覆盖已有 .previous 备份。中断提示出现后应先检查主机版本、服务和回滚备份，再发起新任务。正常升级仍进行 SHA256 校验、原子替换和本地就绪失败回滚。

注册兑换、数据上报和控制请求只跟随同来源重定向（协议、主机、有效端口均相同，最多 9 次跳转），拒绝跨来源与 HTTPS 降级，以保护请求正文和 Bearer 凭据。发行二进制下载保留其独立重定向逻辑。
