#!/usr/bin/env bash
# Meterly v2 systemd 安装器。下载完整版本包，保留已有 v2 配置与 token。
# install-agent.sh --center https://HOST:8443 --enroll-code-file FILE [--version vX.Y.Z]
# 可用 --token-file FILE 重用凭据；注册码可从 stdin 读取（--enroll-code-file -）。
# 本地包测试可用 --binary FILE --unit-file FILE --updater-unit-file FILE。
set -euo pipefail
CENTER='' ENROLL_FILE='' TOKEN_FILE='' CA_FILE='' BIN='' UNIT_SRC='' UPDATER_SRC=''
ONLY='' SKIP='' HOSTNAME_HINT='' ALLOW_HTTP=0 DRY=0
VERSION=${METERLY_VERSION:-latest}
ROOT=${METERLY_ROOT:-}
SYSTEMCTL=${SYSTEMCTL:-systemctl}
REPO=domoxiaojun/Meterly
while [ $# -gt 0 ]; do
  case "$1" in
    --center|--server-url) CENTER=${2:?}; shift 2;;
    --enroll-code-file) ENROLL_FILE=${2:?}; shift 2;;
    --token-file) TOKEN_FILE=${2:?}; shift 2;;
    --ca-file) CA_FILE=${2:?}; shift 2;;
    --binary) BIN=${2:?}; shift 2;;
    --unit-file) UNIT_SRC=${2:?}; shift 2;;
    --updater-unit-file) UPDATER_SRC=${2:?}; shift 2;;
    --version) VERSION=${2:?}; shift 2;;
    --hostname) HOSTNAME_HINT=${2:?}; shift 2;;
    --only) ONLY=${2:?}; shift 2;;
    --skip) SKIP=${2:?}; shift 2;;
    --allow-http) ALLOW_HTTP=1; shift;;
    --dry-run) DRY=1; shift;;
    -h|--help) sed -n '2,5p' "$0"; exit 0;;
    *) echo "未知参数：$1（秘密只接受文件或 stdin）" >&2; exit 2;;
  esac
done
fail(){ echo "$*" >&2; exit 1; }
case "$CENTER" in
  https://*) ;;
  http://*) [ "$ALLOW_HTTP" -eq 1 ] || fail 'HTTP 必须显式提供 --allow-http';;
  *) fail '需要有效 --center https://HOST:PORT';;
esac
# URL 不允许凭据、查询和片段。curl 仅用于无凭据健康检查。
case "$CENTER" in *'@'*|*'?'*|*'#'*) fail '中心地址不得携带凭据、查询或片段';; esac
[ -n "$ROOT" ] || [ "$(uname -s)" = Linux ] || fail '安装器仅支持 Linux systemd'
if [ "$DRY" -eq 0 ] && [ -z "$ROOT" ]; then [ "$(id -u)" -eq 0 ] || fail '安装需要 root'; fi
for tool in curl tar awk install mktemp; do command -v "$tool" >/dev/null || fail "缺少 $tool"; done
command -v "$SYSTEMCTL" >/dev/null || fail '缺少 systemctl'
case "$(uname -m)" in x86_64|amd64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; *) fail '不支持的架构';; esac
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
script_dir=$(cd "$(dirname "$0")" && pwd)
if [ -z "$BIN" ]; then
  if [ "$VERSION" = latest ]; then
    VERSION=$(curl --proto '=https' -fsSL --max-time 30 "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
  fi
  [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail '版本必须为正式 vX.Y.Z'
  BASE="https://github.com/$REPO/releases/download/$VERSION"
  ASSET="meterly-linux-$ARCH.tar.gz"
  curl --proto '=https' -fsSL --max-time 120 -o "$TMP/$ASSET" "$BASE/$ASSET"
  curl --proto '=https' -fsSL --max-time 30 -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS"
  EXPECT=$(awk -v f="$ASSET" '$2==f {print $1}' "$TMP/SHA256SUMS")
  if command -v sha256sum >/dev/null; then GOT=$(sha256sum "$TMP/$ASSET" | awk '{print $1}'); else GOT=$(shasum -a 256 "$TMP/$ASSET" | awk '{print $1}'); fi
  [ -n "$EXPECT" ] && [ "$EXPECT" = "$GOT" ] || fail '版本包 SHA256 校验失败'
  tar -xzf "$TMP/$ASSET" -C "$TMP" meterly-agent deploy/systemd/meterly-agent.service deploy/systemd/meterly-agent-updater.service
  BIN="$TMP/meterly-agent"
  UNIT_SRC="$TMP/deploy/systemd/meterly-agent.service"
  UPDATER_SRC="$TMP/deploy/systemd/meterly-agent-updater.service"
else
  UNIT_SRC=${UNIT_SRC:-$script_dir/../deploy/systemd/meterly-agent.service}
  UPDATER_SRC=${UPDATER_SRC:-$script_dir/../deploy/systemd/meterly-agent-updater.service}
fi
[ -f "$BIN" ] && [ -x "$BIN" ] && [ ! -L "$BIN" ] || fail '二进制不存在或不可执行'
for unit in "$UNIT_SRC" "$UPDATER_SRC"; do [ -f "$unit" ] && [ ! -L "$unit" ] || fail "缺少 unit：$unit"; done
BUILD_VERSION=$("$BIN" version | awk '{print $2}')
[ "$VERSION" = latest ] || [ "$BUILD_VERSION" = "$VERSION" ] || fail '二进制版本与指定版本不一致'
CONFIG="${ROOT}/etc/meterly/agent.json"
TOKEN_DST="${ROOT}/etc/meterly/agent.token"
CA_DST="${ROOT}/etc/meterly/ca.crt"
STATE="${ROOT}/var/lib/meterly-v2"
DEST="${ROOT}/usr/local/bin/meterly-agent"
UNIT_DIR="${ROOT}/etc/systemd/system"
ca=(); curl_ca=(); extra=(); detect=()
[ -n "$ONLY" ] && { extra+=(-only "$ONLY"); detect+=(-only "$ONLY"); }
[ -n "$SKIP" ] && { extra+=(-skip "$SKIP"); detect+=(-skip "$SKIP"); }
[ "$ALLOW_HTTP" -eq 1 ] && extra+=(-allow-http)
if [ -z "$CA_FILE" ] && [ -f "$CA_DST" ]; then CA_FILE=$CA_DST; fi
if [ -n "$CA_FILE" ]; then [ -r "$CA_FILE" ] || fail 'CA 文件不可读'; curl_ca=(--cacert "$CA_FILE"); fi
"$BIN" detect ${detect[@]+"${detect[@]}"}
[ "$DRY" -eq 0 ] || { echo '[dry-run] 版本、二进制和 unit 已检查；未注册或修改服务。'; exit 0; }
if [ -f "$CONFIG" ]; then "$BIN" validate -config "$CONFIG"; fi
curl -fsS --max-time 20 ${curl_ca[@]+"${curl_ca[@]}"} "${CENTER%/}/healthz" >/dev/null
# 到这里完整包、原配置、权限、连通性都已通过检查，再消耗注册码。
install -d -m 700 "${ROOT}/etc/meterly" "$STATE"
install -d -m 755 "${ROOT}/usr/local/bin" "$UNIT_DIR"
if [ -n "$CA_FILE" ]; then
  if [ "$CA_FILE" != "$CA_DST" ]; then install -m 644 "$CA_FILE" "$CA_DST"; fi
  ca=(-ca-file "$CA_DST")
fi
if [ -s "$TOKEN_DST" ]; then
  echo '保留已有 v2 token'
elif [ -n "$TOKEN_FILE" ]; then
  [ -s "$TOKEN_FILE" ] || fail 'token 文件为空或不存在'
  install -m 600 "$TOKEN_FILE" "$TOKEN_DST"
elif [ -n "$ENROLL_FILE" ]; then
  enroll=(-center "$CENTER" -code-file "$ENROLL_FILE" -out "$TOKEN_DST" ${ca[@]+"${ca[@]}"})
  [ "$ALLOW_HTTP" -eq 1 ] && enroll+=(-allow-http)
  [ -n "$HOSTNAME_HINT" ] && enroll+=(-hostname "$HOSTNAME_HINT")
  "$BIN" enroll "${enroll[@]}"
else
  fail '需要注册码文件或 token 文件'
fi
"$BIN" write-config -center "$CENTER" -config "$CONFIG" -token-file "$TOKEN_DST" -state-dir "$STATE" ${ca[@]+"${ca[@]}"} ${extra[@]+"${extra[@]}"}
"$BIN" validate -config "$CONFIG"
# 先复制到同目录临时文件再替换，重复安装或 --binary 指向当前文件也安全。
if [ -f "$DEST" ]; then cp -p "$DEST" "$DEST.previous"; fi
install -m 755 "$BIN" "$DEST.install"
mv -f "$DEST.install" "$DEST"
install -m 644 "$UNIT_SRC" "$UNIT_DIR/meterly-agent.service"
install -m 644 "$UPDATER_SRC" "$UNIT_DIR/meterly-agent-updater.service"
"$SYSTEMCTL" daemon-reload
"$SYSTEMCTL" enable meterly-agent.service
if ! "$SYSTEMCTL" restart meterly-agent.service || ! "$SYSTEMCTL" is-active --quiet meterly-agent.service; then
  if [ -f "$DEST.previous" ]; then cp -p "$DEST.previous" "$DEST.rollback"; mv -f "$DEST.rollback" "$DEST"; "$SYSTEMCTL" restart meterly-agent.service || true; fi
  fail '服务启动失败；如有上一版本已恢复，请检查 journalctl -u meterly-agent'
fi
echo "已安装 ${BUILD_VERSION}；配置 ${CONFIG}；状态 ${STATE}。请在面板核对采集健康度。"
