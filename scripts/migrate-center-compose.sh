#!/usr/bin/env bash
# 把旧版 trafficstat 中心(docker compose,默认 /opt/trafficstat)迁移到 Meterly(/opt/meterly)。
# 默认 --dry-run,只打印计划,不改任何东西。加 --apply 才执行。
# 数据库文件名保持为 /data/trafficstat.db,不重命名。
#
# 不会在本脚本里推送镜像或改远程服务器;请在中心主机上、确认备份成功后再 --apply。
set -euo pipefail

DRY=1
PURGE_OLD=0
OLD_ROOT=${OLD_ROOT:-/opt/trafficstat}
NEW_ROOT=${NEW_ROOT:-/opt/meterly}
SRC_TREE=${SRC_TREE:-}
APPLY_IMAGE=${METERLY_VERSION:-latest}

usage() {
  sed -n '2,10p' "$0" | sed 's/^# \?//'
  echo "用法: $0 [--dry-run|--apply] [--purge-old] [--old-root DIR] [--new-root DIR] [--src DIR]"
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1; shift;;
    --apply) DRY=0; shift;;
    --purge-old) PURGE_OLD=1; shift;;
    --old-root) OLD_ROOT="$2"; shift 2;;
    --new-root) NEW_ROOT="$2"; shift 2;;
    --src) SRC_TREE="$2"; shift 2;;
    -h|--help) usage;;
    *) echo "未知参数: $1" >&2; usage;;
  esac
done

script_dir=$(cd "$(dirname "$0")" && pwd)
repo_root=$(cd "$script_dir/.." && pwd)
[ -n "$SRC_TREE" ] || SRC_TREE="$repo_root"

OLD_COMPOSE="$OLD_ROOT/deploy/compose"
NEW_COMPOSE="$NEW_ROOT/deploy/compose"
OLD_VOL=${OLD_VOLUME:-trafficstat_trafficstat-data}
NEW_VOL=${NEW_VOLUME:-meterly_meterly-data}

log() { printf '%s\n' "$*"; }
run() {
  if [ "$DRY" -eq 1 ]; then
    log "  [dry-run] $*"
  else
    log "  + $*"
    eval "$@"
  fi
}

log "======= Meterly 中心迁移计划 ======="
log "旧目录: $OLD_ROOT"
log "新目录: $NEW_ROOT"
log "新代码: $SRC_TREE"
log "数据卷: $OLD_VOL → $NEW_VOL(数据库文件名保持 trafficstat.db)"
log "模式: $([ "$DRY" -eq 1 ] && echo dry-run || echo APPLY)"
log

if [ ! -d "$OLD_COMPOSE" ] && [ ! -d "$NEW_COMPOSE" ]; then
  log "未找到旧安装 $OLD_COMPOSE。若这是全新安装,请直接按 README 使用 compose,不必跑迁移。"
  [ "$DRY" -eq 1 ] && exit 0
fi

# 1. 备份
log "1. 备份当前数据库(在线 VACUUM,不停旧服务)"
if [ -d "$OLD_COMPOSE" ]; then
  ts=$(date +%Y%m%d-%H%M%S)
  bak_dir="$OLD_ROOT/backups"
  run "install -d -m 700 '$bak_dir'"
  run "(cd '$OLD_COMPOSE' && docker compose exec -T server /trafficstat-server backup -out /data/backups/pre-meterly-$ts.db) || (cd '$OLD_COMPOSE' && docker compose exec -T server /meterly-server backup -out /data/backups/pre-meterly-$ts.db) || true"
  log "  备份也会落在卷内 /data/backups/;建议再 docker run 拷一份到 $bak_dir"
else
  log "  没有旧 compose 目录,跳过在线备份"
fi

# 2. 停旧栈(先备份再停)
log "2. 停止旧 compose 栈(不删卷)"
if [ -d "$OLD_COMPOSE" ]; then
  run "cd '$OLD_COMPOSE' && (docker compose --profile caddy down || docker compose down || true)"
fi
run "systemctl disable --now trafficstat-backup.timer 2>/dev/null || true"

# 3. 同步文件,保留 secrets / .env / Caddy 数据
log "3. 把新代码同步到 $NEW_ROOT,保留已有 .env、secrets、Caddy 证书"
run "install -d -m 755 '$NEW_ROOT'"
run "cp -a '$SRC_TREE/deploy' '$NEW_ROOT/'"
run "cp -a '$SRC_TREE/scripts' '$NEW_ROOT/' 2>/dev/null || true"
if [ -d "$OLD_COMPOSE" ]; then
  run "install -d -m 700 '$NEW_COMPOSE/secrets'"
  run "cp -an '$OLD_COMPOSE/secrets/.' '$NEW_COMPOSE/secrets/' 2>/dev/null || true"
  if [ -f "$OLD_COMPOSE/.env" ]; then
    run "cp -n '$OLD_COMPOSE/.env' '$NEW_COMPOSE/.env' || true"
  fi
fi

# 4. 改写 .env 变量名
log "4. 将 .env 中的 TS_* / 旧镜像名改成 METERLY_* 与 ghcr.io/domoxiaojun/meterly-server"
rewrite_env() {
  local envf="$NEW_COMPOSE/.env"
  if [ ! -f "$envf" ]; then
    if [ -f "$NEW_COMPOSE/.env.example" ]; then
      cp "$NEW_COMPOSE/.env.example" "$envf"
    else
      return 0
    fi
  fi
  python3 - "$envf" "$APPLY_IMAGE" <<'PY'
import pathlib, sys
p = pathlib.Path(sys.argv[1])
ver = sys.argv[2]
text = p.read_text()
repls = [
    ("TS_VERSION", "METERLY_VERSION"),
    ("TS_COMMIT", "METERLY_COMMIT"),
    ("TS_BUILD_DATE", "METERLY_BUILD_DATE"),
    ("TS_BIND", "METERLY_BIND"),
    ("TS_PORT", "METERLY_PORT"),
    ("TS_TZ", "METERLY_TZ"),
    ("TS_CPUS", "METERLY_CPUS"),
    ("TS_MEMORY", "METERLY_MEMORY"),
    ("TS_DOMAIN", "METERLY_DOMAIN"),
    ("TS_TLS", "METERLY_TLS"),
    ("TS_CADDY_BIND", "METERLY_CADDY_BIND"),
    ("TS_CADDY_PORT", "METERLY_CADDY_PORT"),
    ("TS_PANEL_ALLOW", "METERLY_PANEL_ALLOW"),
    ("trafficstat.internal", "meterly.internal"),
]
for a,b in repls:
    text = text.replace(a,b)
# 不改数据库文件;只确保版本变量存在
if "METERLY_VERSION=" not in text:
    text = "METERLY_VERSION=" + ver + "\n" + text
p.write_text(text)
print("  已改写", p)
PY
}
if [ "$DRY" -eq 1 ]; then
  log "  [dry-run] rewrite $NEW_COMPOSE/.env (TS_* → METERLY_*, trafficstat.internal → meterly.internal)"
else
  rewrite_env
fi

# 5. 数据卷
log "5. 复制 Docker 卷 $OLD_VOL → $NEW_VOL(若新卷已有数据则跳过)"
run "docker volume create '$NEW_VOL' >/dev/null"
run "docker volume inspect '$OLD_VOL' >/dev/null 2>&1 && docker run --rm -v '$OLD_VOL':/from:ro -v '$NEW_VOL':/to alpine sh -c 'if [ -z \"\$(ls -A /to 2>/dev/null)\" ]; then cp -a /from/. /to/; else echo new volume already has data, skip; fi' || echo '旧卷不存在,跳过复制'"
log "  注意: db 文件名保持 /data/trafficstat.db,与 deploy/compose/server.json 一致,不要改。"

# 6. 备份 timer
log "6. 安装新的备份 timer,停用旧 timer"
if [ -f "$NEW_COMPOSE/meterly-backup.service" ]; then
  run "install -m 644 '$NEW_COMPOSE/meterly-backup.service' /etc/systemd/system/"
  run "install -m 644 '$NEW_COMPOSE/meterly-backup.timer' /etc/systemd/system/"
  run "systemctl daemon-reload"
  run "systemctl enable --now meterly-backup.timer"
fi
run "systemctl disable --now trafficstat-backup.timer 2>/dev/null || true"

# 7. 启动
log "7. 启动新 compose 栈"
run "cd '$NEW_COMPOSE' && docker compose pull || docker compose build"
run "cd '$NEW_COMPOSE' && docker compose --profile caddy up -d || docker compose up -d"
run "cd '$NEW_COMPOSE' && docker compose exec -T server /meterly-server version || true"
run "cd '$NEW_COMPOSE' && docker compose exec -T server /meterly-server healthcheck || true"

log "8. 旧目录 $OLD_ROOT 默认保留,确认面板与 agent 上报正常后再删除。"
if [ "$PURGE_OLD" -eq 1 ]; then
  run "rm -rf '$OLD_ROOT'"
  log "  已按 --purge-old 删除旧目录(卷 $OLD_VOL 仍在,确认无误后可 docker volume rm $OLD_VOL)"
fi

log
log "接下来请:"
log "  - 用 SSH 隧道打开面板,确认主机列表与近期数据还在"
log "  - 按 docs/migrate.md 升级各 agent(install-agent.sh 会自动迁 token)"
log "  - 若 agent 的 server_url 仍指向 trafficstat.internal,要么保留该 hosts 项并反代到新栈,要么改成 meterly.internal 并更新 /etc/hosts 与证书 CN"
if [ "$DRY" -eq 1 ]; then
  log
  log "以上为 dry-run。确认后在中心主机执行: $0 --apply"
fi
