#!/usr/bin/env bash
# 旧版原地迁移已关闭，防止新中心继续使用旧数据库和旧备份定时器。
set -euo pipefail
echo 'Meterly v2 不兼容旧数据库和 agent。请按 docs/migrate.md 归档旧库，使用新 db_path 和新 agent 状态目录。此脚本未修改任何部署。' >&2
exit 2
