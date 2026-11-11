#!/usr/bin/env bash
# 在本机测量 agent 全采集器开启、以约 2000 行/秒追加日志时的 RSS/CPU。不访问网络。
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
BIN=${1:-$ROOT/dist/meterly-agent}
DIR=$(mktemp -d /tmp/ts-bench-XXXX)
AGENT_PID=; WRITER_PID=; trap 'rm -rf "$DIR"; kill ${AGENT_PID:-} ${WRITER_PID:-} 2>/dev/null || true' EXIT

mkdir -p "$DIR/state"
LOG=$DIR/access.log
: > "$LOG"
cat > "$DIR/agent.json" << JSON
{
  "server_url": "http://127.0.0.1:9",
  "token": "bench-token-not-used",
  "allow_http": true,
  "state_dir": "$DIR/state",
  "interval": "1s",
  "http_timeout": "1s",
  "collectors": {
    "netdev": {"enabled": true, "interfaces": ["lo"]},
    "singbox": {"enabled": false, "url": "http://127.0.0.1:9", "secret_file": "$DIR/empty.secret", "poll_interval": "1s"},
    "site": {"enabled": true, "from_start": true, "allow_dirs": ["$DIR"], "sources": [{"path": "$LOG", "site": "bench.example", "format": "main"}]},
    "netbird": {"enabled": true, "command": "true", "timeout": "1s"},
    "xray": {"enabled": true, "bin": "true", "server": "127.0.0.1:9", "timeout": "1s"}
  }
}
JSON
chmod 600 "$DIR/agent.json"
umask 077
echo dummy > "$DIR/empty.secret"
chmod 600 "$DIR/empty.secret"

# 先写一点,再启动
python3 - << PY
from pathlib import Path
p = Path("$LOG")
line = '192.0.2.1 - - [11/Nov/2026:12:00:00 +0000] "GET / HTTP/1.1" 200 123 "-" "ua" "-"\n'
p.write_text(line*100)
PY

"$BIN" run -config "$DIR/agent.json" >"$DIR/agent.log" 2>&1 &
AGENT_PID=$!
sleep 0.4
if ! kill -0 $AGENT_PID 2>/dev/null; then
  echo "agent 未能启动:" >&2
  cat "$DIR/agent.log" >&2
  exit 1
fi

# 2000 行/秒写 8 秒
python3 - << PY &
import time, os
path = "$LOG"
line = '10.0.0.%d - - [11/Nov/2026:12:00:01 +0000] "GET /stream HTTP/1.1" 200 4096 "-" "Emby" "-"\n'
end = time.time() + 8
i = 0
with open(path, "a") as f:
    while time.time() < end:
        batch = "".join(line % (i % 250) for _ in range(200))
        f.write(batch)
        f.flush()
        i += 200
        time.sleep(0.1)  # 2000 lines/s
PY
WRITER_PID=$!

rss_max=0
samples=0
# 等 agent 跑起来再采 8 秒
for n in $(seq 1 16); do
  sleep 0.5
  if [ -r /proc/$AGENT_PID/status ]; then
    rss=$(awk '/VmRSS:/ {print $2}' /proc/$AGENT_PID/status)
    if [ "${rss:-0}" -gt "$rss_max" ]; then rss_max=$rss; fi
    samples=$((samples+1))
  fi
done
wait $WRITER_PID || true
# 再读一次 tick 后的 RSS
sleep 1
if [ -r /proc/$AGENT_PID/status ]; then
  rss=$(awk '/VmRSS:/ {print $2}' /proc/$AGENT_PID/status)
  if [ "${rss:-0}" -gt "$rss_max" ]; then rss_max=$rss; fi
fi
# CPU: 用 /proc/pid/stat 的 utime+stime
stat=$(awk '{print $14+ $15}' /proc/$AGENT_PID/stat 2>/dev/null || echo 0)
clk=$(getconf CLK_TCK)
# 启动至今大约 10s
cpu_pct=$(python3 -c "print(round($stat * 100 / $clk / 10, 2))" 2>/dev/null || echo "?")
lines=$(wc -l < "$LOG")
echo "agent_pid=$AGENT_PID"
echo "log_lines=$lines"
echo "rss_max_kib=$rss_max"
echo "rss_max_mib=$(python3 -c "print(round($rss_max/1024, 2))")"
echo "cpu_approx_pct_over_10s=$cpu_pct"
echo "samples=$samples"
kill $AGENT_PID 2>/dev/null || true
wait $AGENT_PID 2>/dev/null || true
