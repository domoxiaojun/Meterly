#!/usr/bin/env bash
# 无 Docker 环境下的部署文件静态校验(盒子用)。工具不存在则跳过对应项:
#   docker compose / docker-compose v2 二进制:compose config 渲染校验(不需要 Docker daemon)
#   hadolint:Dockerfile lint;caddy:Caddyfile validate;python3 + PyYAML:YAML 语法;systemd-analyze:unit 校验与安全评分
# 用法:scripts/check-deploy.sh   (可用 COMPOSE= / HADOLINT= / CADDY= 指定二进制路径)
set -uo pipefail
cd "$(dirname "$0")/.."
fail=0
ok() { echo "  ✔ $*"; }
bad() { echo "  ✘ $*"; fail=1; }
skip() { echo "  - 跳过:$*"; }
COMPOSE=${COMPOSE:-$(command -v docker-compose || true)}
HADOLINT=${HADOLINT:-$(command -v hadolint || true)}
CADDY=${CADDY:-$(command -v caddy || true)}

echo "[compose]"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
cp -r deploy/compose/. "$tmp/" && cp deploy/compose/.env.example "$tmp/.env" && mkdir -p "$tmp/secrets" && echo "placeholder-not-a-secret" > "$tmp/secrets/panel_token"
if [ -n "$COMPOSE" ]; then
  (cd "$tmp" && "$COMPOSE" config -q) && ok "compose config(默认)" || bad "compose config(默认)"
  (cd "$tmp" && "$COMPOSE" --profile caddy config -q) && ok "compose config(--profile caddy)" || bad "compose config(caddy)"
  (cd "$tmp" && "$COMPOSE" --profile caddy config | grep -q 'host_ip: 127.0.0.1') && ok "端口默认只发布到 127.0.0.1" || bad "端口发布地址"
  (cd "$tmp" && rm .env && "$COMPOSE" config -q) && ok "缺少 METERLY_VERSION 时默认 latest" || bad "缺省 latest 渲染"
else skip "未找到 docker-compose"; fi
if python3 -c 'import yaml' 2>/dev/null; then
  python3 - <<'PY' && ok "YAML 语法 + 安全项" || bad "YAML / 安全项"
import yaml
d = yaml.safe_load(open("deploy/compose/docker-compose.yml"))
s = d["services"]["server"]
assert s["read_only"] is True and s["cap_drop"] == ["ALL"] and "no-new-privileges:true" in s["security_opt"]
assert s["user"] == "65532:65532" and any(t.startswith("/tmp") for t in s["tmpfs"])
assert s["logging"]["options"]["max-size"] and s["deploy"]["resources"]["limits"]["memory"]
assert s["healthcheck"]["test"][1] == "/meterly-server" and "curl" not in str(s["healthcheck"])
assert d["services"]["caddy"]["profiles"] == ["caddy"]
assert "ghcr.io/domoxiaojun/meterly-server" in str(s.get("image",""))
assert "build" in s
PY
else skip "python3 PyYAML"; fi

echo "[Dockerfile]"
if [ -n "$HADOLINT" ]; then "$HADOLINT" deploy/compose/Dockerfile && ok "hadolint" || bad "hadolint"; else skip "hadolint"; fi
grep -q '^FROM scratch' deploy/compose/Dockerfile && grep -q '^USER 65532' deploy/compose/Dockerfile && grep -q 'HEALTHCHECK.*healthcheck' deploy/compose/Dockerfile && ok "scratch + 非 root + 自带 healthcheck" || bad "Dockerfile 关键项"

echo "[Caddyfile]"
if [ -n "$CADDY" ]; then
  METERLY_DOMAIN=meterly.internal METERLY_TLS=internal "$CADDY" validate --config deploy/compose/Caddyfile --adapter caddyfile >/dev/null 2>&1 && ok "caddy validate" || bad "caddy validate"
  "$CADDY" fmt deploy/compose/Caddyfile | diff -q - deploy/compose/Caddyfile >/dev/null && ok "caddy fmt 已格式化" || bad "caddy fmt"
else skip "caddy"; fi

echo "[systemd]"
if command -v systemd-analyze >/dev/null; then
  d=$(mktemp -d); for u in deploy/systemd/*; do sed 's|/usr/local/bin/|'"$d"'/|' "$u" > "$d/$(basename "$u")"; done
  : > "$d/meterly-server"; : > "$d/meterly-agent"; chmod +x "$d/meterly-server" "$d/meterly-agent"
  SUDO=""; [ "$(id -u)" != 0 ] && sudo -n true 2>/dev/null && SUDO=sudo
  (cd "$d" && $SUDO systemd-analyze verify --man=no ./*.service ./*.timer) && ok "systemd-analyze verify" || bad "systemd-analyze verify"
  for u in meterly-server meterly-agent; do
    echo "    $u: $(cd "$d" && $SUDO systemd-analyze security --offline=true ./$u.service 2>/dev/null | grep -o 'Overall.*')"
  done
  rm -rf "$d"
else skip "systemd-analyze"; fi
exit $fail
