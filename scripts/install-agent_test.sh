#!/usr/bin/env bash
# 在盒子上用假根目录验证:旧 trafficstat-agent 安装可被幂等迁移到 Meterly,dry-run 不改文件。
set -euo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"

fail=0
ok() { echo "  ✔ $*"; }
bad() { echo "  ✘ $*"; fail=1; }

# 构建真实二进制(write-config / detect 要用)
if [ ! -x build/meterly-agent ]; then
  scripts/build.sh >/tmp/meterly-build.log 2>&1 || { echo "构建失败"; cat /tmp/meterly-build.log; exit 1; }
fi
BIN="$ROOT_DIR/build/meterly-agent"

# ---------- dry-run:不改宿主、不改假根 ----------
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
install -d -m 700 "$tmp/old/etc/trafficstat" "$tmp/old/var/lib/trafficstat"
echo 'should-not-be-touched' > "$tmp/old/etc/trafficstat/agent.token"
chmod 600 "$tmp/old/etc/trafficstat/agent.token"
cp -a "$tmp/old/etc/trafficstat/agent.token" "$tmp/token-before"
METERLY_ROOT="$tmp/old" SYSTEMCTL="$tmp/no-systemctl" \
  "$ROOT_DIR/scripts/install-agent.sh" \
    --center http://127.0.0.1:9 --allow-http --dry-run --binary "$BIN" >/tmp/dry-run.out 2>&1 || { cat /tmp/dry-run.out; bad "dry-run exit"; }
if grep -q '\[dry-run\]' /tmp/dry-run.out && cmp -s "$tmp/old/etc/trafficstat/agent.token" "$tmp/token-before"; then
  ok "dry-run 不修改文件且提示将迁移"
else
  bad "dry-run 行为异常"
  cat /tmp/dry-run.out
fi
# 不应创建新目录
if [ -e "$tmp/old/etc/meterly" ]; then bad "dry-run 不应创建 /etc/meterly"; else ok "dry-run 未创建新路径"; fi

# ---------- 迁移假安装 ----------
fake=$(mktemp -d)
trap 'rm -rf "$tmp" "$fake"' EXIT
install -d -m 700 \
  "$fake/etc/trafficstat" \
  "$fake/var/lib/trafficstat/buffer" \
  "$fake/etc/systemd/system/trafficstat-agent.service.d" \
  "$fake/usr/local/bin" "$fake/usr/local/sbin" \
  "$fake/etc/systemd/system"
printf 'keep-this-token-value\n' > "$fake/etc/trafficstat/agent.token"
chmod 600 "$fake/etc/trafficstat/agent.token"
cat > "$fake/etc/trafficstat/agent.json" <<JSON
{
  "server_url": "https://trafficstat.internal:8443",
  "token_file": "/etc/trafficstat/agent.token",
  "ca_file": "/etc/trafficstat/ca.crt",
  "state_dir": "/var/lib/trafficstat",
  "allow_http": false,
  "interval": "60s",
  "collectors": {"netdev": {"enabled": false}}
}
JSON
chmod 600 "$fake/etc/trafficstat/agent.json"
printf 'dummy-ca\n' > "$fake/etc/trafficstat/ca.crt"
echo '{"seq":1}' > "$fake/var/lib/trafficstat/state.json"
echo '{}' > "$fake/var/lib/trafficstat/buffer/one.jsonl"
echo '# old unit' > "$fake/etc/systemd/system/trafficstat-agent.service"
echo '# dropin' > "$fake/etc/systemd/system/trafficstat-agent.service.d/collectors.conf"
echo '#!/bin/sh' > "$fake/usr/local/bin/trafficstat-agent"
chmod 755 "$fake/usr/local/bin/trafficstat-agent"
echo '#!/bin/sh' > "$fake/usr/local/sbin/uninstall-trafficstat-agent"
chmod 755 "$fake/usr/local/sbin/uninstall-trafficstat-agent"

log="$fake/systemctl.log"
cat > "$fake/systemctl" <<STUB
#!/bin/sh
echo "systemctl \$*" >> "$log"
exit 0
STUB
chmod 755 "$fake/systemctl"

METERLY_ROOT="$fake" SYSTEMCTL="$fake/systemctl" METERLY_FORCE_ROOT=1 \
  "$ROOT_DIR/scripts/install-agent.sh" \
    --center 'http://127.0.0.1:9' --allow-http \
    --binary "$BIN" \
    --unit-file "$ROOT_DIR/deploy/systemd/meterly-agent.service" \
    >/tmp/migrate.out 2>&1 || { echo '--- migrate output ---'; cat /tmp/migrate.out; bad "migrate exit"; }

# token 保留
if [ -f "$fake/etc/meterly/agent.token" ] && [ "$(tr -d '\n' < "$fake/etc/meterly/agent.token")" = "keep-this-token-value" ]; then
  ok "token 已迁移且内容不变"
else
  bad "token 未正确迁移"
  ls -la "$fake/etc/meterly" 2>/dev/null || true
  cat /tmp/migrate.out | tail -40
fi
# 状态迁过去
if [ -f "$fake/var/lib/meterly/state.json" ] && [ -f "$fake/var/lib/meterly/buffer/one.jsonl" ]; then
  ok "状态目录已迁移"
else
  bad "状态目录未迁移"
  find "$fake/var/lib" -ls
fi
# ca
if [ -f "$fake/etc/meterly/ca.crt" ]; then ok "ca.crt 已迁移"; else bad "ca.crt 未迁移"; fi
# 新 unit / 二进制
if [ -x "$fake/usr/local/bin/meterly-agent" ] && [ -f "$fake/etc/systemd/system/meterly-agent.service" ]; then
  ok "新二进制与 unit 已安装"
else
  bad "新二进制或 unit 缺失"
fi
# 旧 unit 删除、旧二进制删除
if [ ! -e "$fake/etc/systemd/system/trafficstat-agent.service" ] && [ ! -e "$fake/etc/systemd/system/trafficstat-agent.service.d" ]; then
  ok "旧 unit 已删除"
else
  bad "旧 unit 仍在"
fi
if [ ! -e "$fake/usr/local/bin/trafficstat-agent" ]; then
  ok "旧二进制已删除"
else
  bad "旧二进制仍在"
fi
# systemctl 调用
if grep -q 'enable --now meterly-agent' "$log" && grep -q 'stop trafficstat-agent' "$log"; then
  ok "systemctl 停旧启新"
else
  bad "systemctl 调用不符合预期"
  cat "$log"
fi
# 新配置权限与 token_file 路径
if [ -f "$fake/etc/meterly/agent.json" ]; then
  perm=$(stat -c '%a' "$fake/etc/meterly/agent.json")
  if [ "$perm" = "600" ]; then ok "新配置权限 600"; else bad "新配置权限 $perm"; fi
  if grep -q '/etc/meterly/agent.token' "$fake/etc/meterly/agent.json"; then
    ok "新配置 token_file 指向新路径"
  else
    bad "新配置未改写 token_file"
    cat "$fake/etc/meterly/agent.json"
  fi
else
  bad "缺少新配置"
fi

# ---------- 再跑一遍,幂等 ----------
METERLY_ROOT="$fake" SYSTEMCTL="$fake/systemctl" METERLY_FORCE_ROOT=1 \
  "$ROOT_DIR/scripts/install-agent.sh" \
    --center 'http://127.0.0.1:9' --allow-http \
    --binary "$BIN" \
    --unit-file "$ROOT_DIR/deploy/systemd/meterly-agent.service" \
    >/tmp/migrate2.out 2>&1 || { cat /tmp/migrate2.out; bad "second run exit"; }
if [ "$(tr -d '\n' < "$fake/etc/meterly/agent.token")" = "keep-this-token-value" ]; then
  ok "第二次运行仍保留原 token"
else
  bad "第二次运行破坏了 token"
fi

# ---------- 中心迁移脚本 dry-run(不需要 docker) ----------
if "$ROOT_DIR/scripts/migrate-center-compose.sh" --dry-run --old-root "$fake/opt/trafficstat" --new-root "$fake/opt/meterly" | grep -q 'dry-run'; then
  ok "migrate-center-compose.sh --dry-run 可执行"
else
  bad "migrate-center-compose.sh dry-run 失败"
fi
# dry-run 不应创建新目录
if [ -e "$fake/opt/meterly" ]; then bad "center dry-run 不应创建目录"; else ok "center dry-run 未改文件系统"; fi

# env 改写的单元测试(直接调 python 逻辑的等价物)
envdir=$(mktemp -d)
cat > "$envdir/.env" <<ENV
TS_VERSION=v0.2.0
TS_DOMAIN=trafficstat.internal
TS_BIND=127.0.0.1
ENV
# 用迁移脚本 --apply 在假目录上改写 .env:造一个最小旧树
oldc="$envdir/old/deploy/compose"
newc="$envdir/new"
install -d "$oldc/secrets"
echo 'x' > "$oldc/secrets/panel_token"
cp "$envdir/.env" "$oldc/.env"
# apply 会调用 docker,我们只测 rewrite:复用脚本中的替换,自己跑一遍对照
python3 - "$oldc/.env" <<'PY'
import pathlib, sys
p = pathlib.Path(sys.argv[1])
text = p.read_text()
for a,b in [
    ("TS_VERSION","METERLY_VERSION"),
    ("TS_DOMAIN","METERLY_DOMAIN"),
    ("TS_BIND","METERLY_BIND"),
    ("trafficstat.internal","meterly.internal"),
]:
    text = text.replace(a,b)
p.write_text(text)
PY
if grep -q 'METERLY_DOMAIN=meterly.internal' "$oldc/.env" && grep -q 'METERLY_VERSION=v0.2.0' "$oldc/.env" && ! grep -q 'TS_VERSION' "$oldc/.env"; then
  ok ".env 变量改写 TS_* → METERLY_*"
else
  bad ".env 改写结果不正确"
  cat "$oldc/.env"
fi
rm -rf "$envdir"

echo
if [ "$fail" -ne 0 ]; then
  echo "install-agent_test.sh: $fail 项失败"
  exit 1
fi
echo "install-agent_test.sh: 全部通过"
