# v2 部署模板

本目录为已有容器环境保留部署模板。开发和测试不需要 Docker，单二进制 systemd 安装见 [主说明](../../README.md)。本次实现未启动容器或操作生产。

- `server.json` 默认使用全新 `/data/meterly-v2.db`。已有旧库应先按 [切换说明](../../docs/migrate.md) 归档，不能继续使用旧库结构。
- 中心不再依赖旧 backup timer。切换前停用宿主机 `meterly-backup.timer`，新中心在自身时区每天 04:20 调度备份，保留 7 份。
- 配置 `agent_url` 为 agent 可达的 HTTPS 地址。`Caddyfile` 的 agent 监听允许 `/api/v2/ingest`、`/api/v2/enroll/redeem`、`/api/v2/agent/control` 和 `/api/v2/agent/upgrade/status`，除此以外只提供 healthz。
- 面板监听禁止上述 agent 路径，保留来源 IP 白名单与面板登录。端口默认仅绑定回环地址；具体网络开放由部署人员决定。
- 维持原配置中的只读根文件系统、无 capability、非 root 用户和日志容量限制。数据卷及备份目录须允许中心用户写入。
- 独立 agent 使用发行完整包安装到被采集主机，不在中心容器中执行主机升级。系统页下发的任务只允许本项目正式版本。

正式启用前应在目标 Linux/systemd 和实际反代环境验收注册、推送、配置同步、备份及升级回退。静态模板检查和回环 TLS 测试不等于生产路由已验证。
