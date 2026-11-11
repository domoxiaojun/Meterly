# Meterly 项目审查报告

日期：2026-11-11。基准：`main` / `9241ad5`。开始审查时工作区干净。

结论：确认 10 项应修复的问题，其中 3 项 P1、7 项 P2。现有测试全部通过，但未覆盖下述安装断点、异常恢复与统计边界。此次只新增审查材料，未修改业务实现。

## 发现

### R1 · P1 · 写缓冲失败后，下一轮覆盖待恢复记录，永久漏计

位置：[internal/agent/agent.go](../../internal/agent/agent.go)，265–272 行；同文件 `Cycle` 和 `New`。

`Tick` 已更新各采集器基线、调用 `Drain`，并把当前记录写入 `state.Pending`，随后才追加缓冲。如果追加失败，`Cycle` 只记录错误并继续运行。下一轮又用新记录覆盖 `Pending`；恢复旧 `Pending` 的逻辑只在进程启动时执行。因此，可恢复的磁盘/目录写入错误会丢掉上一批，中心无法补发，也不会体现在 `Dropped` 中。

复现：Xray 累计值依次为 100、200、300；在中间一轮让缓冲目录暂时不可访问，确认 100 字节已保存在 `state.Pending`；恢复目录后继续 `Tick`，缓冲只剩 seq=2 的 100 字节，应为 200 字节。

建议：先成功恢复未提交的 `Pending`，再推进采集基线和生成下一批；同时保证 state 保存失败时，不丢失已经从采集器取出的增量。

证据：`TestReviewTickRetriesPendingAfterWriteFailure`，见 [Go 复现输出](go-repro.txt)。

### R2 · P1 · 按 README 单独下载安装脚本，新安装必然缺少 systemd unit

位置：[scripts/install-agent.sh](../../scripts/install-agent.sh)，308–318 行；[README.md](../../README.md) 的“一条命令装 agent”。

README 只下载 `install-agent.sh`，脚本虽能下载 agent 二进制，但 unit 只能来自显式 `--unit-file`、相邻源码仓库，或者已有安装。全新机器不满足这些条件，在写入 token 和配置之后以“缺少 --unit-file”退出，无法启动服务。

建议：独立脚本应携带或下载与二进制版本匹配的 unit，并在兑换一次性注册码、写入安装内容前检查安装资源是否齐备。

证据：在只有原安装脚本的临时目录内，用假 agent 及假 token 排除下载和注册因素，脚本退出码为 2，并到达上述缺少 unit 分支。见 [安装复现输出](install-repro.txt)。

### R3 · P1 · Caddy 的 agent 入口未放行注册兑换，推荐 HTTPS 安装流程无法完成

位置：[deploy/compose/Caddyfile](../../deploy/compose/Caddyfile)，19–27、34–36 行；[internal/agent/enroll.go](../../internal/agent/enroll.go)，60–61 行。

README 的 agent 中心地址使用 8443，对应 Caddy 容器 443。该入口只转发 `POST /api/v1/ingest` 和 `/healthz`，而 agent 注册请求为 `POST /api/v1/enroll/redeem`，会落入兜底 403。把中心地址改成面板端口也不能完成整条链路，因为面板端口明确拒绝 ingest，而 agent 注册和推送共用中心地址。

建议：在 agent 入口精确放行注册兑换路径，继续由中心校验注册码和执行限流；不需要开放面板管理接口。

证据：静态核对 Caddy 匹配规则、Compose 端口和 agent 的实际请求路径。本次没有运行 Caddy 或 Docker，因此未声称完成部署实测。

### R4 · P2 · 已安装机器忽略指定版本，并把二进制复制到自身

位置：[scripts/install-agent.sh](../../scripts/install-agent.sh)，138–140、263 行。

存在 `BIN_DST` 时，`resolve_binary` 总是返回当前已安装二进制，未使用 `--version` 决定下载目标版本。随后 `install -m 755 "$AGENT_BIN" "$BIN_DST"` 的源和目标是同一文件。升级或按 README 重新安装时会被这个分支截断；即使避开同文件复制，当前逻辑仍未获取所选版本。

建议：明确区分复用本地二进制与按版本更新；更新时下载校验后原子替换目标，复用时跳过同文件复制。

证据：假根中已有旧 agent，指定不存在的测试版本仍成功 dry-run 并执行旧 agent；实际安装随后因同文件复制退出。退出码 64 是本机 macOS `install` 的结果，Linux 工具的具体退出码未实测。见 [安装复现输出](install-repro.txt)。

### R5 · P2 · 特定自定义日志格式触发无限循环，阻塞整个 agent

位置：[internal/agent/logformat.go](../../internal/agent/logformat.go)，202–206 行。

未加引号字段后直接跟着带引号/括号字段时，解析器进入 `for end > pos && end == len(line)`，循环体没有修改条件，也没有退出语句。比如格式 `$remote_addr"$request" $status $body_bytes_sent [$time_local]` 能通过编译，但解析第一条记录就不返回。站点采集与其他采集、持久化、推送同在主循环里，因而不仅该日志丢数据，整个 agent 都停止完成后续轮次。

建议：移除空循环，实现有界分隔符查找；不支持的格式应在编译阶段报错，不能挂住运行循环。

证据：`TestReviewAdjacentQuotedFieldTerminates` 在 200 ms 截止仍未返回。见 [解析器复现输出](parser-repro.txt)。

### R6 · P2 · rename 日志轮转丢失旧文件尚未采集的尾部

位置：[internal/agent/sitelog.go](../../internal/agent/sitelog.go)，311–322 行。

每轮关闭日志文件，下次仅重新打开配置路径。若两次轮询之间，旧文件新增请求后被重命名为 `access.log.1`，代码发现 inode 变化便直接从新文件开头读；旧 inode 上最后一个 offset 之后的内容再也不会被访问。默认一分钟轮询以及存在读取积压时都会遇到这个窗口。

建议：保留旧文件句柄直到读完轮转尾部，或按旧 inode 定位轮转文件并补读，再切换新文件；重启断点也需保留对应信息。

证据：旧文件轮转前新增 100 字节请求，新文件新增 200 字节请求，最终只得到 200 字节、1 次请求，预期 300 字节、2 次请求。见 [Go 复现输出](go-repro.txt)。

### R7 · P2 · 最近一小时总览读取小时表，漏掉窗口前半段

位置：[internal/server/api.go](../../internal/server/api.go)，148–159 行。

`last1h` 是按分钟移动的窗口，但总览仍调用 `store.TableFor` 选择小时表。窗口从 15:31 开始时，15 点的桶时间戳小于窗口起点，整桶被排除，15:31–15:59 的有效流量随之漏算。曲线、对账和明细已使用分钟表，因此同一页面显示相互矛盾的数字。

建议：总览也使用处理 `last1h` 的 `factTable`，统一筛选口径。

证据：16:30 查看最近一小时，窗口内 15:45、16:15 分别有 100、200 字节；分钟曲线合计 300，总览只有 200。见 [Go 复现输出](go-repro.txt)。

### R8 · P2 · 全部主机的 IP 排行按面板时区读取各自主机日桶，跨时区少算

位置：[internal/server/api.go](../../internal/server/api.go)，268–269 行；[internal/store/ingest.go](../../internal/store/ingest.go) 的 `DayStart(ts, loc)`。

`fact_day` 的时间戳按各主机时区生成，但 `host=all` 的查询边界按面板时区生成。`topTable` 只检查查询范围是否在面板时区整日对齐，便读取日表，没有检查所选主机的时区。日桶可能落在查询范围之外，导致 IP 排行、地理汇总、IP 明细总量与按小时汇总的入站统计不一致。

建议：跨时区全局查询在小时数据可用时统一使用小时表；如果必须使用日表，应明确其可表达的统计口径和精度限制。

证据：一台新加坡时区主机和一台 UTC 主机，在新加坡 11 月 10 日 02:00 各上报 100 字节。查询该日全部主机，IP 排行合计 100，入站合计 200。见 [Go 复现输出](go-repro.txt)。

### R9 · P2 · 查询保留期固定为 400 天，与配置不一致

位置：[internal/store/query.go](../../internal/store/query.go)，30–36 行；[internal/store/maint.go](../../internal/store/maint.go)，48 行。

维护任务按 `Retention.HourDays` 删除小时数据，查询却始终用全局固定 400 天判断是否回退日表，配置没有同步到这里。用户缩短小时保留期后，已经只有日表保留的数据依然被查询小时表，导致历史用量显示为 0，影响总览、对账和服务商比较。

建议：查询和维护使用同一份实际保留策略，并处理跨越保留边界的区间。

证据：把小时保留期设为 1 天，维护后 3 天前的日表仍有 100 字节，但同日总览为 0。见 [Go 复现输出](go-repro.txt)。

### R10 · P2 · 面板固定只有 eth0/wt0，其他网卡无法查看曲线

位置：[web/index.html](../../web/index.html)，48–50 行；[web/app.js](../../web/app.js)，3、158、508–514、536 行。

agent 能自动探测 `ens3`、`enp1s0` 等默认路由网卡，中心也支持配置计费网卡，但曲线状态初始固定为 `eth0`，按钮仅有 `eth0`、`wt0`。切换主机时既不使用该主机的 `billing_iface`，也不根据实际 `ifaces` 更新选项。因此，即使主机计费总量已正确配置并有数据，图表仍可能一直显示空白/0，用户无法从界面选中真实网卡。

建议：从所选主机的实际网卡构造选项，默认选择其计费网卡；全部主机视图也应明确定义按同名网卡还是各自计费网卡汇总。

证据：静态追踪网卡状态的全部读写位置及按钮定义，未进行真实浏览器交互测试。

## 验证及复现

现有验证通过：

- `go vet ./...`
- `go test -race -count=1 ./...`：8 个有测试的包通过，包含完整端到端测试；真实 DB-IP 数据库测试因未配置数据目录按条件跳过。
- `node --check web/app.js`
- `bash -n scripts/install-agent.sh`

新发现的缺陷用例使用 Go overlay，不修改现有包源码或测试文件。断言的是正确行为，所以当前基准上预计失败。这些失败与上面的现有测试通过并不矛盾。

在仓库根目录复现：

```sh
go test -overlay plans/project-review-20261111/repro/overlay.json \
  -count=1 -run '^TestReview' -v -timeout 30s \
  ./internal/agent ./internal/server

node plans/project-review-20261111/repro/install_review.mjs
```

Go 复现使用临时 SQLite 文件和测试数据；安装复现使用原脚本、假 agent 和临时假根。Go overlay 的路径绑定当前工作区，移动仓库后需调整 [overlay.json](repro/overlay.json)。安装脚本复现会清理临时目录，并更新审查目录里的输出文件。

复现源：[agent 用例](repro/agent_review_test.go.txt)、[server 用例](repro/server_review_test.go.txt)、[安装脚本用例](repro/install_review.mjs)。

## 覆盖边界

- 已审查 agent 采集/状态/缓冲/推送、中心鉴权/注册/写入/聚合/保留、面板 API 调用以及安装与部署配置；没有运行全仓安全或依赖扫描。
- 未安装数据库服务，未执行 Docker 命令，未部署、提交或推送。
- 未原样运行依赖 GNU 工具和直接调用 `python3` 的安装迁移测试，也未运行包含 Docker 调用的部署检查脚本。
- Caddy、Linux systemd、真实 sing-box/NetBird/Xray、实际日志源和浏览器呈现未做现场验收；本报告把静态证据与定向运行结果分别标明。
- 未就未经复现的潜在边界、纯风格问题或无明确收益的优化扩展审查。

优先处理 R1–R3，随后处理 R4–R10，并把已确认的复现用例转为修复后的回归测试。
