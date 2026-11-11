#!/usr/bin/env bash
# 卸载 Meterly 中心(docker compose)。也兼容仍在 /opt/trafficstat 的旧布局。
# 默认保留 docker 卷与 secrets,便于回滚。
# --purge 删除卷、安装目录、备份/防火墙 unit,并移除本项目的 iptables 规则。
set -euo pipefail
PURGE=0
[ "${1:-}" = "--purge" ] && PURGE=1

if [ -d "${COMPOSE_DIR:-}" ]; then
  :
elif [ -d /opt/meterly/deploy/compose ]; then
  COMPOSE_DIR=/opt/meterly/deploy/compose
elif [ -d /opt/trafficstat/deploy/compose ]; then
  COMPOSE_DIR=/opt/trafficstat/deploy/compose
else
  COMPOSE_DIR=/opt/meterly/deploy/compose
fi

systemctl disable --now meterly-backup.timer 2>/dev/null || true
systemctl disable --now trafficstat-backup.timer 2>/dev/null || true
systemctl disable --now meterly-firewall.service 2>/dev/null || true
systemctl disable --now trafficstat-firewall.service 2>/dev/null || true
rm -f /etc/systemd/system/meterly-backup.service /etc/systemd/system/meterly-backup.timer
rm -f /etc/systemd/system/trafficstat-backup.service /etc/systemd/system/trafficstat-backup.timer
rm -f /etc/systemd/system/meterly-firewall.service /etc/systemd/system/trafficstat-firewall.service
systemctl daemon-reload || true

if [ -d "$COMPOSE_DIR" ]; then
  cd "$COMPOSE_DIR"
  if [ "$PURGE" -eq 1 ]; then
    docker compose --profile caddy down -v || docker compose down -v || true
  else
    docker compose --profile caddy down || docker compose down || true
  fi
fi

del_by_comment() {
  local table="$1" chain="$2" comment="$3"
  while :; do
    local num
    num=$(iptables -t "$table" -L "$chain" -n --line-numbers 2>/dev/null | awk -v c="$comment" 'index($0,c){print $1; exit}')
    [ -n "${num:-}" ] || break
    iptables -t "$table" -D "$chain" "$num"
  done
}
# 新旧注释都清理(ingest / 面板端口,INPUT 与 DOCKER-USER)
for chain in INPUT DOCKER-USER; do
  for c in "meterly ingest" "trafficstat ingest" "meterly 8443" "trafficstat 8443" "meterly 8444" "trafficstat 8444"; do
    del_by_comment filter "$chain" "$c"
  done
  # 其它端口上带 meterly / trafficstat 注释的规则
  while :; do
    num=$(iptables -L "$chain" -n --line-numbers 2>/dev/null | awk '/meterly |trafficstat /{print $1; exit}')
    [ -n "${num:-}" ] || break
    iptables -D "$chain" "$num"
  done
done

if [ "$PURGE" -eq 1 ]; then
  if [ -f /etc/iptables/rules.v4 ]; then
    tmp=$(mktemp)
    grep -v -e 'meterly ingest' -e 'meterly 8443' -e 'meterly 8444' -e 'trafficstat ingest' -e 'trafficstat 8443' -e 'trafficstat 8444' /etc/iptables/rules.v4 > "$tmp"
    cat "$tmp" > /etc/iptables/rules.v4
    rm -f "$tmp"
  fi
  rm -rf /opt/meterly /opt/trafficstat
  echo "purged /opt/meterly (and /opt/trafficstat if present), compose volumes, firewall unit, and persisted iptables comments"
else
  echo "left install dir and docker volumes (meterly_meterly-data / trafficstat_trafficstat-data) in place (use --purge to delete)"
fi
echo "center stopped"
