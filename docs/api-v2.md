# v2 API 与数据契约

所有分析/管理接口位于 `/api/v2`。面板通过 `/api/login` 和 HttpOnly 会话 cookie 登录；主机 token 仅用于 agent 路由，不能访问面板。写入管理接口需 JSON。中心确认 `max_seq` 的前提是统计事务已提交，agent 只在确认后清理缓冲。

## 分析

| 路径 | 用途 |
|---|---|
| `GET /overview` | 当前范围总览、网站/IP Top 5、等长比较、网卡独立口径、告警 |
| `GET /analytics/options` | 主机、网站和中心时区候选项 |
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

## 管理与采集

| 路径 | 方法与说明 |
|---|---|
| `/hosts` | GET：主机、inventory、health、期望/已应用配置版本 |
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
