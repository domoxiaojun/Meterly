# v2 部署模板

本目录为已有容器环境保留部署模板。开发和测试不需要 Docker，单二进制 systemd 安装见 [主说明](../../README.md)。

中心默认使用 `ghcr.io/domoxiaojun/meterly-server:latest`。在部署主机的 `.env` 中设置 `METERLY_VERSION=latest`；每次推送 `main` 后，GitHub Actions 的 Docker 工作流会构建并发布 `latest` 和对应的 `sha-*` 标签（linux/amd64、linux/arm64）。确认工作流成功后，在已有容器环境的本目录执行：

```sh
docker compose pull server
docker compose up -d --no-build server
```

`pull_policy: always` 会在 Compose 启动服务时检查并拉取镜像；它不会自动更新已运行的容器，仍需执行上述更新命令。需要固定版本时，将 `METERLY_VERSION` 改为已发布的 `vX.Y.Z` 标签。更新前保留一致性数据库备份及原镜像版本，更新后检查服务健康状态。

当前新 React 前端仍为本地预览，尚未接入生产 Go embed 和镜像构建；`latest` 继续包含现有生产面板。

- `server.json` 默认使用全新 `/data/meterly-v2.db`。已有旧库应先按 [切换说明](../../docs/migrate.md) 归档，不能继续使用旧库结构。
- 中心不再依赖旧 backup timer。切换前停用宿主机 `meterly-backup.timer`，新中心在自身时区每天 04:20 调度备份，保留 7 份。
- 配置 `agent_url` 为 agent 可达的 HTTPS 地址。`Caddyfile` 的 agent 监听允许 `/api/v2/ingest`、`/api/v2/enroll/redeem`、`/api/v2/agent/control` 和 `/api/v2/agent/upgrade/status`，除此以外只提供 healthz。
- 面板监听禁止上述 agent 路径，保留来源 IP 白名单与面板登录。端口默认仅绑定回环地址；具体网络开放由部署人员决定。
- 维持原配置中的只读根文件系统、无 capability、非 root 用户和日志容量限制。数据卷及备份目录须允许中心用户写入。
- 独立 agent 使用发行完整包安装到被采集主机，不在中心容器中执行主机升级。系统页下发的任务只允许本项目正式版本。

发布端口前配置 `meterly-docker-user.sh` 或对应 `meterly-firewall.service`：ingest 需要 `AGENT_ALLOW_IPS`、`INGEST_PORT`、`INGEST_BIND_IP`；面板需要 `PANEL_ALLOW_IPS`、`PANEL_PUBLIC_PORT`、`PANEL_BIND_IP`。`*_BIND_IP` 是实际发布到本机的单个 IPv4（不是来源白名单，也不能填 0.0.0.0）；云主机若公网地址在外部做 NAT，应填写数据包到达本机时的目标地址。更换发布地址后需重新应用规则。DOCKER-USER 同时匹配 DNAT、原始目标 IP/端口和 ORIGINAL 方向，INPUT 匹配目标 IP/端口，普通容器向外访问同号端口不会匹配。

此辅助脚本只管理 IPv4。模板继续默认绑定回环；IPv6 发布需由部署人员配置对应 IPv6 访问控制。未提供有效绑定地址时脚本在改动规则前退出；原有清理选项不需要绑定地址。

正式启用前应在目标 Linux/systemd 和实际反代环境验收注册、推送、配置同步、备份及升级回退。静态模板检查和回环 TLS 测试不等于生产路由已验证。
