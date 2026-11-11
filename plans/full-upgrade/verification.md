# Meterly v2 交付验证

## 已完成

- ✅ 新协议版本 2、`/api/v2` 分析/管理接口，新数据库与旧库隔离；拒绝旧库前先只读检查，不修改旧文件。
- ✅ 网站 × 主机 × IP × 时间 × 具体状态码联合统计，规范化 IP 字典；保留 48 小时分钟明细、365 天完整小时 IP 明细、长期日汇总。
- ✅ 不再进行破坏性 Top-N 合并；缓冲满时保留待写记录，暂停采集，分块重试，不删除未确认数据。
- ✅ nginx / 1Panel/OpenResty 来源发现、include 的可信代理继承、可信链末端解析、Cloudflare/IPv6 及缺字段诊断；不修改 nginx。
- ✅ 自动发现与面板配置版本同步；停用和消失的来源保留清单/断点；无效配置保留最后有效值。
- ✅ 完整版本包、独立/重复安装、指定版本、原子替换、独立升级更新器及回滚状态。
- ✅ 自动清理、归属地/正式版本检查、04:20 备份、保留 7 份、容量估计、站内告警与任务结果。
- ✅ 六页面 ES Modules 面板，日期、网站、IP、主机之间的导航，CSV、状态码、IP/CIDR/国家/ASN 筛选、主题及移动端。

## 验证证据

| 检查 | 结果和范围 | 证据 |
|---|---|---|
| Go 静态检查 | `go vet ./...` 通过 | 与竞态测试顺序执行，退出码 0 |
| 全项目竞态测试 | `go test -race -count=1 ./...` 通过，原有 e2e 包约 431 秒 | [Go 日志](evidence/go-race.txt) |
| 后续修改的定向竞态回归 | agent、server、store 通过；新增来源覆盖检查通过 | [定向检查](evidence/final-targeted.txt)、[来源覆盖](evidence/source-coverage-final.txt) |
| 联合统计 | 137 个 IP × 24 小时、两台主机、不同计费时区；分页、排行、曲线、CSV 和总量一致 | `internal/store/analysis_test.go` |
| 时间边界 | 单日、跨月、跨日、最近一小时、保留期、映射 IPv4/CIDR、等长比较 | `internal/server/analysis_test.go`、`internal/store/analysis_test.go` |
| 持久化恢复 | 缓冲写失败、轮转尾部、解析终止；8,000 条 IP 数据在容量耗尽、部分写入和重启后完整恢复 | [恢复检查](evidence/recovery-final.txt) |
| HTTPS 接入 | 自签 CA 验证的回环 HTTPS 注册与真实 agent 推送/确认，数据进入新分析表 | `internal/e2e/tls_test.go` |
| 自动管理 | 新增/消失/停用来源、无效配置、版本冲突、重复升级状态确认 | `internal/agent/management_test.go`、`internal/server/analysis_test.go` |
| 升级和备份 | 模拟 systemctl 验证升级就绪/失败回滚；8 次在线备份仅保留 7 份且可以打开 | `internal/agent/management_test.go`、`internal/server/maintenance_test.go` |
| 安装器 | 独立完整包、明确版本、重复安装、配置/token 保留、缺 unit 预检、HTTP 必须显式启用 | [安装日志](evidence/install.txt) |
| 浏览器 | 4 项通过：核心流程和返回滚动、持久化、竞态、错误重试、移动端/主题、零/未采集/过期 | [浏览器日志](evidence/browser.txt)、[模块测试](evidence/javascript.txt) |
| 文档链接 | Lychee 22 项成功，0 错误 | [链接检查](evidence/links.txt) |
| 完整发行包 | Linux amd64/arm64 静态二进制与完整包构建成功；6 项 SHA256 一致，包内含两个二进制、units、配置及安装器 | [构建记录](evidence/build-packages.txt)、[包检查](evidence/packages.txt) |
| 部署文件 | ShellCheck、actionlint、JSON、TLS 路由和完整包断言 | [部署检查](evidence/deploy-checks.txt) |

面板截图来自临时测试数据：[网站详情](evidence/site-detail.png)、[移动端系统页](evidence/mobile-system.png)。

## 审查十项问题闭环

1. ✅ 缓冲写失败不再覆盖 Pending；容量满采用背压与分块恢复。
2. ✅ 独立安装从完整版本包获得 agent/updater units。
3. ✅ agent TLS 入口允许注册码核销及出站控制路径，面板入口禁止它们。
4. ✅ 明确版本始终下载指定包，同文件安装先写临时文件再替换。
5. ✅ 相邻带引号字段解析可终止，有回归用例。
6. ✅ 保存轮转 inode 断点，补读旧文件尾部，重启可在原目录找回 inode。
7. ✅ 最近一小时使用分钟明细，范围对齐结束分钟。
8. ✅ 新 IP 分析使用中心时区和 UTC 桶，不跨主机拼接各自本地日数据。
9. ✅ 查询根据实际保留配置选表，超过完整 IP 保留期明确标记过期。
10. ✅ 网卡来自实际发现，默认计费网卡由发现结果确定，全部主机汇总各自计费网卡。

## 交付边界

- 实现验收时尚未提交或推送，没有发布版本或部署生产；没有归档/启用任何生产数据库，没有安装数据库服务或运行 Docker。
- Linux amd64/arm64 版本包为本地构建验证产物。正式安装须使用发布后对应的完整包和 SHA256SUMS。
- 本机无 systemd/Caddy 可执行环境；已运行安装/更新器模拟、部署路由断言和真实回环 TLS 注册/推送。真实 systemd 启停、Caddy 网络策略和实际 1Panel/CDN 日志仍须在目标环境完成部署验收。
- 来源路径/代理 include 超出发现目录、动态生成配置或存在不能确认的 location 覆盖时，保持保守归因，并通过本机显式配置/面板可信代理设置解决。
- 无法恢复已经删除的轮转日志内容；日志读取暂停期间需保留轮转文件。缺少请求长度、开始采集前、明细过期和采集异常均不能解释成精确零流量。
- 用户自定义状态目录时，也需为更新器 unit 配置相应 `ReadWritePaths`。默认成套安装的路径已一致。
