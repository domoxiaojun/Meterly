#!/bin/sh
# 限制 Docker 发布的 Meterly 端口只允许指定来源 IP。
# 同时写 INPUT 与 DOCKER-USER:Docker 把宿主端口 DNAT 到容器后走 FORWARD,
# 只写 INPUT 挡不住(DOCKER-USER 看到的是容器目标端口,所以用 conntrack 原始目标端口匹配,
# 同时要求 DNAT 与原始目标 IP，避免误伤其它容器的同号出站端口)。
#
# 用法:
#   AGENT_ALLOW_IPS="203.0.113.10 198.51.100.20" INGEST_PORT=8443 INGEST_BIND_IP="192.0.2.10" \
#   PANEL_ALLOW_IPS="203.0.113.30" PANEL_PUBLIC_PORT=8444 PANEL_BIND_IP="192.0.2.10" \
#   ./meterly-docker-user.sh
#
# 不设默认 IP。
#   AGENT_ALLOW_IPS 非空:写入 ingest 端口限制;为空则不动 ingest 规则(避免误清)。
#   设 CLEAR_INGEST=1 时,清除 ingest 端口上本脚本写过的规则,不加放行、不加 DROP。
#   PANEL_PUBLIC_PORT 非空且 PANEL_ALLOW_IPS 非空:写入面板端口限制;
#   仅端口非空、允许列表为空:清除该面板端口上的旧规则。
# 幂等:先按注释删掉旧规则(含旧版 trafficstat 注释)再插入。
# 可在 docker 启动前运行:若 DOCKER-USER 还不存在会先建链并挂到 FORWARD,避免开机空窗。
set -eu

INGEST_PORT=${INGEST_PORT:-8443}
PANEL_PUBLIC_PORT=${PANEL_PUBLIC_PORT:-}
AGENT_ALLOW_IPS=${AGENT_ALLOW_IPS:-}
PANEL_ALLOW_IPS=${PANEL_ALLOW_IPS:-}
INGEST_BIND_IP=${INGEST_BIND_IP:-}
PANEL_BIND_IP=${PANEL_BIND_IP:-}

# Fail before removing any rules. Use the actual IPv4 published on this host.
valid_bind() {
  printf '%s\n' "$1" | awk -F. 'NF!=4 {exit 1} {for(i=1;i<=4;i++) if($i !~ /^[0-9]+$/ || $i>255) exit 1; if($0=="0.0.0.0") exit 1}'
}
if [ -n "$AGENT_ALLOW_IPS" ] && ! valid_bind "$INGEST_BIND_IP"; then
  echo "请设置 INGEST_BIND_IP 为实际发布地址（单个 IPv4，不能为 0.0.0.0）。" >&2; exit 1
fi
if [ -n "$PANEL_PUBLIC_PORT" ] && [ -n "$PANEL_ALLOW_IPS" ] && ! valid_bind "$PANEL_BIND_IP"; then
  echo "请设置 PANEL_BIND_IP 为实际发布地址（单个 IPv4，不能为 0.0.0.0）。" >&2; exit 1
fi

ensure_docker_user() {
  iptables -N DOCKER-USER 2>/dev/null || true
  if ! iptables -S FORWARD 2>/dev/null | grep -q -- '-j DOCKER-USER'; then
    iptables -I FORWARD -j DOCKER-USER
  fi
}

del_by_comment() {
  chain="$1"
  comment="$2"
  while :; do
    num=$(iptables -L "$chain" -n --line-numbers 2>/dev/null | awk -v c="$comment" 'index($0,c){print $1; exit}')
    [ -n "${num:-}" ] || break
    iptables -D "$chain" "$num"
  done
}

# 删除本脚本及旧版 trafficstat 在该端口上写过的规则。
clear_port() {
  port="$1"
  for chain in INPUT DOCKER-USER; do
    del_by_comment "$chain" "meterly ${port} default drop"
    del_by_comment "$chain" "trafficstat ${port} default drop"
    while :; do
      num=$(iptables -L "$chain" -n --line-numbers 2>/dev/null | awk -v c="meterly ${port} allow" 'index($0,c){print $1; exit}')
      [ -n "${num:-}" ] || break
      iptables -D "$chain" "$num"
    done
    while :; do
      num=$(iptables -L "$chain" -n --line-numbers 2>/dev/null | awk -v c="trafficstat ${port}" 'index($0,c){print $1; exit}')
      [ -n "${num:-}" ] || break
      iptables -D "$chain" "$num"
    done
  done
  # 旧版 ingest 的 INPUT 注释不带端口号
  if [ "$port" = "8443" ]; then
    del_by_comment INPUT "trafficstat ingest"
    del_by_comment INPUT "meterly ingest"
  fi
}

# 在 INPUT 中,把本端口规则插到 ESTABLISHED/lo 之后,以免挡回环或已建立连接。
input_insert_pos() {
  pos=$(iptables -L INPUT -n --line-numbers 2>/dev/null | awk '/ctstate RELATED,ESTABLISHED|state RELATED,ESTABLISHED/{print $1; exit}')
  if [ -n "${pos:-}" ]; then
    echo $((pos + 1))
  else
    echo 1
  fi
}

apply_port() {
  port="$1"
  ips="$2"
  label="$3"
  bind="$4"

  clear_port "$port"

  if [ -z "$ips" ]; then
    echo "$label 端口 $port:允许列表为空,已清除旧规则,未添加新限制。" >&2
    return 0
  fi

  # DOCKER-USER:先插 DROP,再把放行插到链首(位于 DROP 之前)
  iptables -I DOCKER-USER -p tcp -m conntrack --ctstate DNAT --ctorigdst "$bind" --ctorigdstport "$port" --ctdir ORIGINAL -m comment --comment "meterly ${port} default drop" -j DROP
  n=0
  for ip in $ips; do
    n=$((n + 1))
    iptables -I DOCKER-USER -p tcp -s "$ip" -m conntrack --ctstate DNAT --ctorigdst "$bind" --ctorigdstport "$port" --ctdir ORIGINAL -m comment --comment "meterly ${port} allow $ip" -j ACCEPT
  done

  # INPUT:先在 ESTABLISHED 后插 DROP,再在同一位置插放行(放行在 DROP 之前)
  pos=$(input_insert_pos)
  iptables -I INPUT "$pos" -p tcp -d "$bind" --dport "$port" -m comment --comment "meterly ${port} default drop" -j DROP
  for ip in $ips; do
    iptables -I INPUT "$pos" -p tcp -d "$bind" -s "$ip" --dport "$port" -m comment --comment "meterly ${port} allow $ip" -j ACCEPT
  done

  echo "$label:已放行 $n 个来源 IP 访问宿主端口 $port(INPUT + DOCKER-USER),其余来源 DROP。"
}

ensure_docker_user

CLEAR_INGEST=${CLEAR_INGEST:-}

if [ -n "$AGENT_ALLOW_IPS" ]; then
  apply_port "$INGEST_PORT" "$AGENT_ALLOW_IPS" "ingest" "$INGEST_BIND_IP"
elif [ "$CLEAR_INGEST" = "1" ]; then
  apply_port "$INGEST_PORT" "" "ingest" "$INGEST_BIND_IP"
else
  echo "未设置 AGENT_ALLOW_IPS,跳过 ingest 端口 $INGEST_PORT(现有规则保持不变)。" >&2
fi

if [ -n "$PANEL_PUBLIC_PORT" ]; then
  apply_port "$PANEL_PUBLIC_PORT" "$PANEL_ALLOW_IPS" "panel" "$PANEL_BIND_IP"
fi

echo "----- INPUT (meterly) -----"
iptables -L INPUT -n -v --line-numbers 2>/dev/null | awk 'NR==1 || NR==2 || /meterly|trafficstat/'
echo "----- DOCKER-USER -----"
iptables -L DOCKER-USER -n -v --line-numbers
