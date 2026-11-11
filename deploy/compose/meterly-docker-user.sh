#!/bin/sh
# 限制 Docker 发布的 8443 只允许指定来源 IP(agent 所在主机)。
# Docker 把宿主 8443 DNAT 到 Caddy 容器 443,FORWARD/DOCKER-USER 看到的是 dport 443,
# 所以用 conntrack 原始目标端口 8443 来匹配,避免误伤其它容器的 443。
#
# 用法:
#   AGENT_ALLOW_IPS="203.0.113.10 198.51.100.20" ./meterly-docker-user.sh
# 不设默认 IP。AGENT_ALLOW_IPS 为空则只删除旧规则并退出(不会加放行,也不会加 DROP)。
# 幂等:先删本脚本写过的旧注释(含旧版 trafficstat 注释)再插入。
set -eu
PORT=${INGEST_PORT:-8443}
IPS=${AGENT_ALLOW_IPS:-}

del_by_comment() {
  comment="$1"
  while :; do
    num=$(iptables -L DOCKER-USER -n --line-numbers 2>/dev/null | awk -v c="$comment" 'index($0,c){print $1; exit}')
    [ -n "${num:-}" ] || break
    iptables -D DOCKER-USER "$num"
  done
}

# 清理本脚本及旧版 trafficstat 写过的规则
del_by_comment "meterly ${PORT} default drop"
del_by_comment "trafficstat ${PORT} default drop"
# 按前缀删除放行规则
while :; do
  num=$(iptables -L DOCKER-USER -n --line-numbers 2>/dev/null | awk -v c="meterly ${PORT} allow" 'index($0,c){print $1; exit}')
  [ -n "${num:-}" ] || break
  iptables -D DOCKER-USER "$num"
done
while :; do
  num=$(iptables -L DOCKER-USER -n --line-numbers 2>/dev/null | awk -v c="trafficstat ${PORT}" 'index($0,c){print $1; exit}')
  [ -n "${num:-}" ] || break
  iptables -D DOCKER-USER "$num"
done

if [ -z "$IPS" ]; then
  echo "AGENT_ALLOW_IPS 为空,已清除旧规则,未添加新限制。" >&2
  exit 0
fi

iptables -I DOCKER-USER -p tcp -m conntrack --ctorigdstport "$PORT" --ctdir ORIGINAL -m comment --comment "meterly ${PORT} default drop" -j DROP
n=0
for ip in $IPS; do
  n=$((n+1))
  iptables -I DOCKER-USER -p tcp -s "$ip" -m conntrack --ctorigdstport "$PORT" --ctdir ORIGINAL -m comment --comment "meterly ${PORT} allow $ip" -j ACCEPT
done
echo "已放行 $n 个来源 IP 访问宿主端口 $PORT,其余来源 DROP。"
iptables -L DOCKER-USER -n -v --line-numbers
