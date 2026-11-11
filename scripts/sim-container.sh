#!/usr/bin/env bash
# 在没有 Docker 的机器上近似模拟中心容器的运行约束(需要 sudo + bubblewrap):
#   uid/gid 65532、无 capability、no_new_privs、env -i、根文件系统只读(只含二进制/CA/配置,同 scratch 镜像)、
#   仅 /data 可写、/tmp 为 tmpfs、面板口令以只读文件挂在 /run/secrets/panel_token。
# 用法:
#   scripts/sim-container.sh nonet   # 独立网络命名空间(与容器一致监听 0.0.0.0:8787;无网络 → 验证 geo 下载失败降级)
#   scripts/sim-container.sh net     # 共享宿主网络,改为监听 127.0.0.1:18791,并像 Docker 一样注入 resolv.conf(验证真实 DB-IP 下载)
#   scripts/sim-container.sh stop
# 目录:/tmp/ts-ctr(rootfs、data、secret)。不会打印任何口令。
set -euo pipefail
cd "$(dirname "$0")/.."
R=/tmp/ts-ctr
mode=${1:-nonet}
if [ "$mode" = stop ]; then sudo pkill -u 65532 -f '^/meterly-server serve' || true; exit 0; fi
[ -x dist/meterly-server-linux-amd64 ] || scripts/build.sh dist >/dev/null
sudo rm -rf "$R/rootfs"
mkdir -p "$R"/rootfs/{etc/ssl/certs,etc/meterly,data,tmp,proc,dev,run/secrets} "$R/data" "$R/secret" 2>/dev/null || sudo mkdir -p "$R/data" "$R/secret"
cp dist/meterly-server-linux-amd64 "$R/rootfs/meterly-server"
cp /etc/ssl/certs/ca-certificates.crt "$R/rootfs/etc/ssl/certs/"
cp deploy/compose/server.json "$R/rootfs/etc/meterly/server.json"
touch "$R/rootfs/run/secrets/panel_token" "$R/rootfs/etc/resolv.conf" "$R/rootfs/etc/hosts"
if ! sudo test -s "$R/secret/panel_token"; then
  sudo bash -c "umask 077; head -c 32 /dev/urandom | base64 > '$R/secret/panel_token'"
fi
sudo chown -R 65532:65532 "$R/data" "$R/secret"; sudo chmod 700 "$R/secret"; sudo chmod 600 "$R/secret/panel_token"
extra=(--unshare-net)
if [ "$mode" = net ]; then
  python3 -c "import json;c=json.load(open('deploy/compose/server.json'));c['listen']='127.0.0.1:18791';json.dump(c,open('$R/server-net.json','w'))"
  extra=(--share-net --ro-bind "$R/server-net.json" /etc/meterly/server.json --ro-bind /etc/resolv.conf /etc/resolv.conf --ro-bind /etc/hosts /etc/hosts)
fi
sudo bash -c "( setpriv --reuid=65532 --regid=65532 --clear-groups --no-new-privs --bounding-set=-all --inh-caps=-all \
  env -i bwrap --unshare-user --unshare-pid --unshare-ipc --unshare-uts --die-with-parent ${extra[*]} \
  --ro-bind $R/rootfs / --bind $R/data /data --ro-bind $R/secret/panel_token /run/secrets/panel_token \
  --tmpfs /tmp --proc /proc --dev /dev \
  --setenv METERLY_CONFIG /etc/meterly/server.json --setenv TMPDIR /tmp --setenv TZ Asia/Singapore \
  /meterly-server serve > $R/serve.log 2>&1 & )"
sleep 3
PID=$(pgrep -u 65532 -f '^/meterly-server serve' | tail -1)
echo "== 进程 $PID"; grep -E '^(Uid|CapEff|CapBnd|NoNewPrivs)' /proc/"$PID"/status
echo "== 环境变量"; sudo cat /proc/"$PID"/environ | tr '\0' '\n'
echo "== 挂载"; sudo awk '{print "  "$5, $6}' /proc/"$PID"/mountinfo | grep -E '^  /( |data|tmp|run)'
echo "== healthcheck 子命令(在其网络命名空间内)"
if [ "$mode" = net ]; then cfg="$R/server-net.json"; else cfg="$R/rootfs/etc/meterly/server.json"; fi
sudo nsenter -t "$PID" -n setpriv --reuid=65532 --regid=65532 --clear-groups "$R/rootfs/meterly-server" healthcheck -config "$cfg" && echo "  healthy"
echo "== 日志"; cat "$R/serve.log"
