#!/usr/bin/env bash
# 只读静态检查；不依赖 Docker、数据库服务或 Python。
set -euo pipefail
cd "$(dirname "$0")/.."
shellcheck scripts/install-agent.sh scripts/install-agent_test.sh scripts/package.sh scripts/build.sh scripts/uninstall-agent.sh scripts/check-deploy.sh
actionlint .github/workflows/ci.yml .github/workflows/release.yml
for path in deploy/config/*.json deploy/compose/server.json; do jq empty "$path"; done
node --test tests/deploy.test.js
if command -v caddy >/dev/null; then caddy adapt --config deploy/compose/Caddyfile --adapter caddyfile --validate >/dev/null; fi
if command -v systemd-analyze >/dev/null && [ "$(uname -s)" = Linux ]; then
  systemd-analyze verify --man=no deploy/systemd/meterly-agent.service deploy/systemd/meterly-agent-updater.service deploy/systemd/meterly-server.service
fi
