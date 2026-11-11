#!/usr/bin/env bash
# 卸载 meterly-agent(同时清理旧版 trafficstat-agent unit,如果还在)。
# 不触碰旧版 Python trafficstat、sing-box、nginx、vnstat。
# 默认保留数据目录;加 --purge 才删除配置、缓冲,以及 hosts 中的 meterly.internal / trafficstat.internal。
set -euo pipefail
PURGE=0
[ "${1:-}" = "--purge" ] && PURGE=1
ROOT="${METERLY_ROOT:-}"
SYSTEMCTL="${SYSTEMCTL:-systemctl}"

"$SYSTEMCTL" disable --now meterly-agent 2>/dev/null || true
"$SYSTEMCTL" disable --now trafficstat-agent 2>/dev/null || true
rm -f "${ROOT}/etc/systemd/system/meterly-agent.service"
rm -rf "${ROOT}/etc/systemd/system/meterly-agent.service.d"
rm -f "${ROOT}/etc/systemd/system/trafficstat-agent.service"
rm -rf "${ROOT}/etc/systemd/system/trafficstat-agent.service.d"
"$SYSTEMCTL" daemon-reload 2>/dev/null || true
rm -f "${ROOT}/usr/local/bin/meterly-agent" "${ROOT}/usr/local/bin/trafficstat-agent"
rm -f "${ROOT}/usr/local/sbin/uninstall-meterly-agent" "${ROOT}/usr/local/sbin/uninstall-trafficstat-agent"
if [ "$PURGE" -eq 1 ]; then
  rm -rf "${ROOT}/etc/meterly" "${ROOT}/var/lib/meterly" "${ROOT}/etc/trafficstat" "${ROOT}/var/lib/trafficstat"
  if [ -f "${ROOT}/etc/hosts" ]; then
    tmp=$(mktemp)
    grep -v -e '[[:space:]]meterly\.internal\([[:space:]]\|$\)' \
            -e '[[:space:]]trafficstat\.internal\([[:space:]]\|$\)' \
            "${ROOT}/etc/hosts" > "$tmp" || true
    cat "$tmp" > "${ROOT}/etc/hosts"
    rm -f "$tmp"
  fi
  echo "purged configs, state, and meterly.internal / trafficstat.internal hosts entries"
else
  echo "left /etc/meterly and /var/lib/meterly in place (use --purge to delete)"
fi
echo "agent uninstalled"
