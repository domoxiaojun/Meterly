#!/usr/bin/env bash
# 卸载 v2 服务，默认保留配置与数据；--purge 仅清除当前 v2 路径，旧档案保留。
set -euo pipefail
ROOT=${METERLY_ROOT:-}
SYSTEMCTL=${SYSTEMCTL:-systemctl}
PURGE=0
case "${1:-}" in '') ;; --purge) PURGE=1;; *) echo '用法: uninstall-agent.sh [--purge]' >&2; exit 2;; esac
"$SYSTEMCTL" stop meterly-agent-updater.service 2>/dev/null || true
"$SYSTEMCTL" disable --now meterly-agent.service 2>/dev/null || true
rm -f "${ROOT}/etc/systemd/system/meterly-agent.service" "${ROOT}/etc/systemd/system/meterly-agent-updater.service"
rm -rf "${ROOT}/etc/systemd/system/meterly-agent.service.d"
"$SYSTEMCTL" daemon-reload
rm -f "${ROOT}/usr/local/bin/meterly-agent" "${ROOT}/usr/local/bin/meterly-agent.previous"
if [ "$PURGE" -eq 1 ]; then
  rm -f "${ROOT}/etc/meterly/agent.json" "${ROOT}/etc/meterly/agent.token"
  rm -rf "${ROOT}/var/lib/meterly-v2"
else
  echo '已卸载服务，保留 /etc/meterly 的 agent 配置与 /var/lib/meterly-v2 数据'
fi
