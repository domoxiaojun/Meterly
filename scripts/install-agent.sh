#!/usr/bin/env bash
# 在后端主机上一键安装/更新 meterly-agent(systemd)。幂等、可逆、可重复执行。
# 若检测到旧版 trafficstat-agent,会停止旧 unit、把配置/token/状态迁到新路径并启用新 unit(保留 token)。
#
# 推荐(新机器,从 GitHub Release 拉二进制):
#   install-agent.sh --center URL --enroll-code-file FILE [--ca-file PATH] [--version v0.3.0]
#     [--only singbox,site] [--skip xray] [--dry-run]
# 已有 token 的机器(升级/重装,不换发):
#   install-agent.sh --center URL --token-file FILE [--binary PATH | --version v0.3.0] [--ca-file PATH]
# 注册码也可从标准输入读: --enroll-code-file -   不要把码写在命令行参数里。
#
# 环境变量:
#   METERLY_ROOT   把所有路径前缀到该目录(测试用;生产不要设)
#   METERLY_REPO   GitHub 仓库,默认 domoxiaojun/Meterly
#   SYSTEMCTL      systemctl 可执行文件(测试可换成桩)
set -euo pipefail

CENTER=""
ENROLL_FILE=""
TOKEN_FILE=""
CA_FILE=""
BIN=""
UNIT_SRC=""
ONLY=""
SKIP=""
DRY=0
HOSTNAME_HINT=""
ALLOW_HTTP=0
VERSION="${METERLY_VERSION:-latest}"

ROOT="${METERLY_ROOT:-}"
REPO="${METERLY_REPO:-domoxiaojun/Meterly}"
SYSTEMCTL="${SYSTEMCTL:-systemctl}"

CONFIG_PATH="${ROOT}/etc/meterly/agent.json"
TOKEN_DST="${ROOT}/etc/meterly/agent.token"
CA_DST="${ROOT}/etc/meterly/ca.crt"
STATE_DIR="${ROOT}/var/lib/meterly"
UNIT_DST="${ROOT}/etc/systemd/system/meterly-agent.service"
DROPIN_DIR="${ROOT}/etc/systemd/system/meterly-agent.service.d"
BIN_DST="${ROOT}/usr/local/bin/meterly-agent"
UNINSTALL_DST="${ROOT}/usr/local/sbin/uninstall-meterly-agent"

OLD_CONFIG_DIR="${ROOT}/etc/trafficstat"
OLD_STATE_DIR="${ROOT}/var/lib/trafficstat"
OLD_UNIT="${ROOT}/etc/systemd/system/trafficstat-agent.service"
OLD_DROPIN="${ROOT}/etc/systemd/system/trafficstat-agent.service.d"
OLD_BIN="${ROOT}/usr/local/bin/trafficstat-agent"
OLD_UNINSTALL="${ROOT}/usr/local/sbin/uninstall-trafficstat-agent"

usage() { sed -n '2,16p' "$0" | sed 's/^# \?//'; exit 2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --center|--server-url) CENTER="${2:-}"; shift 2;;
    --enroll-code-file) ENROLL_FILE="${2:-}"; shift 2;;
    --enroll-code) echo "请使用 --enroll-code-file(或 - 表示 stdin),不要把注册码写在命令行" >&2; exit 2;;
    --token-file) TOKEN_FILE="${2:-}"; shift 2;;
    --ca-file) CA_FILE="${2:-}"; shift 2;;
    --binary) BIN="${2:-}"; shift 2;;
    --unit-file) UNIT_SRC="${2:-}"; shift 2;;
    --only) ONLY="${2:-}"; shift 2;;
    --skip) SKIP="${2:-}"; shift 2;;
    --hostname) HOSTNAME_HINT="${2:-}"; shift 2;;
    --version) VERSION="${2:-}"; shift 2;;
    --dry-run) DRY=1; shift;;
    --allow-http) ALLOW_HTTP=1; shift;;
    --interfaces) shift 2;; # 兼容旧参数,探测会覆盖
    -h|--help) usage;;
    *) echo "未知参数: $1" >&2; usage;;
  esac
done

[ -n "$CENTER" ] || { echo "需要 --center URL" >&2; exit 2; }

script_dir=$(cd "$(dirname "$0")" && pwd)
repo_root=$(cd "$script_dir/.." && pwd)

backup() {
  local f="$1"
  if [ -f "$f" ]; then
    cp -a "$f" "$f.bak.$(date +%Y%m%d%H%M%S)"
  fi
}

need_root() {
  if [ -n "$ROOT" ] || [ "${METERLY_FORCE_ROOT:-}" = "1" ]; then
    return 0
  fi
  [ "$(id -u)" -eq 0 ] || { echo "安装需要 root" >&2; exit 1; }
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64;;
    aarch64|arm64) echo arm64;;
    *) echo "不支持的架构: $(uname -m)" >&2; return 2;;
  esac
}

# 从 GitHub Release 下载 meterly-agent 并校验 SHA256SUMS。输出落盘路径。
download_agent() {
  local arch tag base tmp asset expect got
  arch=$(detect_arch) || return 2
  if ! command -v curl >/dev/null 2>&1; then
    echo "下载二进制需要 curl,或改用 --binary PATH" >&2
    return 2
  fi
  if [ "$VERSION" = "latest" ] || [ -z "$VERSION" ]; then
    tag=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
    [ -n "$tag" ] || { echo "无法解析 ${REPO} 的 latest release" >&2; return 2; }
  else
    tag=$VERSION
  fi
  base="https://github.com/${REPO}/releases/download/${tag}"
  asset="meterly-agent-linux-${arch}"
  tmp=$(mktemp -d)
  echo "从 ${base} 下载 ${asset} ..." >&2
  curl -fsSL -o "$tmp/SHA256SUMS" "${base}/SHA256SUMS"
  curl -fsSL -o "$tmp/$asset" "${base}/${asset}"
  expect=$(awk -v f="$asset" '$2==f || $2=="*"f {print $1; exit}' "$tmp/SHA256SUMS")
  [ -n "$expect" ] || { echo "SHA256SUMS 中没有 $asset" >&2; return 2; }
  got=$(sha256sum "$tmp/$asset" | awk '{print $1}')
  if [ "$expect" != "$got" ]; then
    echo "sha256 校验失败: 期望 $expect 实际 $got" >&2
    return 2
  fi
  chmod 755 "$tmp/$asset"
  echo "$tmp/$asset"
}

resolve_binary() {
  if [ -n "${BIN:-}" ]; then
    [ -f "$BIN" ] || { echo "二进制不存在: $BIN" >&2; return 2; }
    echo "$BIN"
    return 0
  fi
  if [ -x "$BIN_DST" ]; then
    echo "$BIN_DST"
    return 0
  fi
  # 仓库内已构建的二进制
  local cand
  for cand in \
      "$repo_root/dist/meterly-agent-linux-$(detect_arch 2>/dev/null || echo amd64)" \
      "$repo_root/build/meterly-agent" \
      "$script_dir/../build/meterly-agent"; do
    if [ -x "$cand" ]; then
      echo "$cand"
      return 0
    fi
  done
  download_agent
}

legacy_present() {
  [ -f "$OLD_UNIT" ] || [ -x "$OLD_BIN" ] || [ -d "$OLD_CONFIG_DIR" ] || [ -d "$OLD_STATE_DIR" ]
}

move_if_missing() {
  local src="$1" dst="$2"
  if [ -e "$src" ] && [ ! -e "$dst" ]; then
    mv "$src" "$dst"
    echo "  迁移 $src → $dst"
  elif [ -e "$src" ] && [ -e "$dst" ]; then
    echo "  保留已有 $dst(旧文件 $src 未覆盖)"
  fi
}

# 把已迁走的 agent.json 里旧路径改成新路径(write-config 稍后还会重写一份)。
rewrite_legacy_paths() {
  local f="$1"
  [ -f "$f" ] || return 0
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$f" <<'PY'
import json, sys
p = sys.argv[1]
with open(p, encoding="utf-8") as f:
    c = json.load(f)
changed = False
repl = {
    "/etc/trafficstat/agent.token": "/etc/meterly/agent.token",
    "/etc/trafficstat/ca.crt": "/etc/meterly/ca.crt",
    "/var/lib/trafficstat": "/var/lib/meterly",
}
for k in ("token_file", "ca_file", "state_dir"):
    v = c.get(k)
    if isinstance(v, str) and v in repl:
        c[k] = repl[v]; changed = True
if changed:
    with open(p, "w", encoding="utf-8") as f:
        json.dump(c, f, indent=2, ensure_ascii=False)
        f.write("\n")
PY
  else
    sed -i \
      -e 's|/etc/trafficstat/agent.token|/etc/meterly/agent.token|g' \
      -e 's|/etc/trafficstat/ca.crt|/etc/meterly/ca.crt|g' \
      -e 's|/var/lib/trafficstat|/var/lib/meterly|g' \
      "$f"
  fi
}

migrate_legacy() {
  legacy_present || return 0
  echo "======= 检测到旧版 trafficstat-agent,迁移到 Meterly(保留 token 与状态) ======="
  "$SYSTEMCTL" stop trafficstat-agent 2>/dev/null || true
  "$SYSTEMCTL" disable trafficstat-agent 2>/dev/null || true
  install -d -m 700 "${ROOT}/etc/meterly"
  install -d -m 700 "$STATE_DIR"
  move_if_missing "$OLD_CONFIG_DIR/agent.token" "$TOKEN_DST"
  move_if_missing "$OLD_CONFIG_DIR/ca.crt" "$CA_DST"
  move_if_missing "$OLD_CONFIG_DIR/agent.json" "$CONFIG_PATH"
  if [ -d "$OLD_STATE_DIR" ]; then
    if [ -z "$(ls -A "$STATE_DIR" 2>/dev/null || true)" ]; then
      # 把旧状态目录的内容迁过去(含隐藏文件以外的常规文件)
      find "$OLD_STATE_DIR" -mindepth 1 -maxdepth 1 -exec mv {} "$STATE_DIR/" \;
      rmdir "$OLD_STATE_DIR" 2>/dev/null || true
      echo "  迁移状态目录 $OLD_STATE_DIR → $STATE_DIR"
    else
      echo "  保留已有 $STATE_DIR(旧目录 $OLD_STATE_DIR 未覆盖)"
    fi
  fi
  rewrite_legacy_paths "$CONFIG_PATH"
  chmod 600 "$TOKEN_DST" 2>/dev/null || true
  chmod 600 "$CONFIG_PATH" 2>/dev/null || true
  rm -f "$OLD_UNIT"
  rm -rf "$OLD_DROPIN"
  rm -f "$OLD_UNINSTALL"
  echo "旧 unit 已移除。token 已保留。"
}

detect_flags=()
[ -n "$ONLY" ] && detect_flags+=(-only "$ONLY")
[ -n "$SKIP" ] && detect_flags+=(-skip "$SKIP")

AGENT_BIN=$(resolve_binary) || exit 2
[ -f "$AGENT_BIN" ] || { echo "二进制不存在: $AGENT_BIN" >&2; exit 2; }

echo "======= 探测本机采集器 ======="
if [ ${#detect_flags[@]} -gt 0 ]; then
  "$AGENT_BIN" detect "${detect_flags[@]}"
else
  "$AGENT_BIN" detect
fi
if [ "$DRY" -eq 1 ]; then
  if legacy_present; then
    echo
    echo "[dry-run] 将迁移旧版 trafficstat-agent(停止旧 unit、迁移 /etc/trafficstat 与 /var/lib/trafficstat、启用 meterly-agent)。"
  fi
  echo
  echo "[dry-run] 未修改任何文件、未注册、未安装服务。"
  exit 0
fi

need_root

migrate_legacy

install -d -m 700 "${ROOT}/etc/meterly"
install -d -m 700 "$STATE_DIR"
install -d -m 755 "${ROOT}/usr/local/bin" "${ROOT}/usr/local/sbin" "${ROOT}/etc/systemd/system"
install -m 755 "$AGENT_BIN" "$BIN_DST"
AGENT_BIN="$BIN_DST"

if [ -n "$CA_FILE" ]; then
  install -m 644 "$CA_FILE" "$CA_DST"
  CA_ARG=(-ca-file "$CA_DST")
else
  CA_ARG=()
  if [ -f "$CA_DST" ]; then CA_ARG=(-ca-file "$CA_DST"); fi
  if [ ${#CA_ARG[@]} -eq 0 ] && [ -f "${OLD_CONFIG_DIR}/ca.crt" ]; then
    install -m 644 "${OLD_CONFIG_DIR}/ca.crt" "$CA_DST"
    CA_ARG=(-ca-file "$CA_DST")
  fi
fi

# 已有 token 则保留;否则用注册码换取
if [ -f "$TOKEN_DST" ]; then
  echo "保留已有 token 文件 $TOKEN_DST(不重新注册)"
elif [ -f "${OLD_CONFIG_DIR}/agent.token" ]; then
  install -m 600 "${OLD_CONFIG_DIR}/agent.token" "$TOKEN_DST"
  echo "从旧路径迁入 token"
elif [ -n "$TOKEN_FILE" ]; then
  [ -f "$TOKEN_FILE" ] || { echo "token 文件不存在" >&2; exit 2; }
  install -m 600 "$TOKEN_FILE" "$TOKEN_DST"
elif [ -n "$ENROLL_FILE" ]; then
  enroll_args=(-center "$CENTER" -code-file "$ENROLL_FILE" -out "$TOKEN_DST")
  [ ${#CA_ARG[@]} -gt 0 ] && enroll_args+=("${CA_ARG[@]}")
  case "$CENTER" in http://*) enroll_args+=(-allow-http);; esac
  [ "$ALLOW_HTTP" -eq 1 ] && enroll_args+=(-allow-http)
  [ -n "$HOSTNAME_HINT" ] && enroll_args+=(-hostname "$HOSTNAME_HINT")
  "$AGENT_BIN" enroll "${enroll_args[@]}"
else
  echo "需要 --enroll-code-file 或 --token-file(或已存在 $TOKEN_DST)" >&2
  exit 2
fi

backup "$CONFIG_PATH"
write_args=(-center "$CENTER" -config "$CONFIG_PATH" -token-file "$TOKEN_DST" -state-dir "$STATE_DIR")
[ ${#CA_ARG[@]} -gt 0 ] && write_args+=("${CA_ARG[@]}")
[ ${#detect_flags[@]} -gt 0 ] && write_args+=("${detect_flags[@]}")
case "$CENTER" in http://*) write_args+=(-allow-http);; esac
[ "$ALLOW_HTTP" -eq 1 ] && write_args+=(-allow-http)
"$AGENT_BIN" write-config "${write_args[@]}"
chmod 600 "$CONFIG_PATH"

# systemd unit
default_unit="$repo_root/deploy/systemd/meterly-agent.service"
if [ -n "$UNIT_SRC" ]; then
  backup "$UNIT_DST"
  install -m 644 "$UNIT_SRC" "$UNIT_DST"
elif [ -f "$default_unit" ]; then
  backup "$UNIT_DST"
  install -m 644 "$default_unit" "$UNIT_DST"
elif [ ! -f "$UNIT_DST" ]; then
  echo "缺少 --unit-file 且 $UNIT_DST 不存在(仓库内也没有 deploy/systemd/meterly-agent.service)" >&2
  exit 2
fi
# 测试根目录下,把 unit 里的绝对路径改到 ROOT 前缀,避免指到宿主
if [ -n "$ROOT" ] && [ -f "$UNIT_DST" ]; then
  sed -i \
    -e "s|/usr/local/bin/meterly-agent|${BIN_DST}|g" \
    -e "s|/etc/meterly/agent.json|${CONFIG_PATH}|g" \
    -e "s|StateDirectory=meterly|# StateDirectory=meterly|" \
    -e "s|ReadWritePaths=/var/lib/meterly|ReadWritePaths=${STATE_DIR}|" \
    -e "s|ReadOnlyPaths=/etc/meterly|ReadOnlyPaths=${ROOT}/etc/meterly|" \
    "$UNIT_DST"
fi

# drop-in:给站点日志 / 1Panel / x-ui 开只读路径,必要时加 CAP_DAC_READ_SEARCH
install -d -m 755 "$DROPIN_DIR"
rops=()
# 探测真实宿主路径(测试 ROOT 下若存在同样收录)
for d in /var/log/nginx /var/log/openresty /opt/1panel/www /opt/1panel/apps/openresty \
         /etc/sing-box /etc/xray /usr/local/etc/xray /root/x-ui /usr/local/x-ui; do
  if [ -n "$ROOT" ] && [ -d "${ROOT}$d" ]; then
    rops+=("${ROOT}$d")
  elif [ -d "$d" ]; then
    rops+=("$d")
  fi
done
{
  echo "[Service]"
  if [ ${#rops[@]} -gt 0 ]; then
    echo "ReadOnlyPaths=${rops[*]}"
  fi
  echo "CapabilityBoundingSet=CAP_DAC_READ_SEARCH"
  echo "AmbientCapabilities=CAP_DAC_READ_SEARCH"
  if getent group adm >/dev/null 2>&1 && { [ -d /var/log/nginx ] || [ -d "${ROOT}/var/log/nginx" ]; }; then
    echo "SupplementaryGroups=adm"
  fi
} > "$DROPIN_DIR/collectors.conf"
chmod 644 "$DROPIN_DIR/collectors.conf"

# 卸载脚本
if [ -f "$script_dir/uninstall-agent.sh" ]; then
  install -m 755 "$script_dir/uninstall-agent.sh" "$UNINSTALL_DST"
fi

# 旧二进制在新服务就绪后再删,避免中途失败无法回退
rm -f "$OLD_BIN"

"$AGENT_BIN" check -config "$CONFIG_PATH" || true
"$SYSTEMCTL" daemon-reload
"$SYSTEMCTL" enable --now meterly-agent
"$SYSTEMCTL" restart meterly-agent
"$SYSTEMCTL" --no-pager --full status meterly-agent | head -20 || true
echo
echo "======= 安装完成 ======="
if [ ${#detect_flags[@]} -gt 0 ]; then
  "$AGENT_BIN" detect "${detect_flags[@]}" | sed -n '1,20p'
else
  "$AGENT_BIN" detect | sed -n '1,20p'
fi
echo "配置: $CONFIG_PATH"
echo "卸载: $UNINSTALL_DST   (加 --purge 才删配置和数据)"
