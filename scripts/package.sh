#!/usr/bin/env bash
# 把已构建二进制、服务 unit、模板和安装/卸载器打包。
set -euo pipefail
cd "$(dirname "$0")/.."
for arch in amd64 arm64; do
  stage=$(mktemp -d)
  trap 'rm -rf "$stage"' EXIT
  install -m 755 "dist/meterly-agent-linux-$arch" "$stage/meterly-agent"
  install -m 755 "dist/meterly-server-linux-$arch" "$stage/meterly-server"
  mkdir -p "$stage/deploy/systemd" "$stage/deploy/config" "$stage/scripts"
  cp deploy/systemd/meterly-agent.service deploy/systemd/meterly-agent-updater.service deploy/systemd/meterly-server.service "$stage/deploy/systemd/"
  cp deploy/config/*.json "$stage/deploy/config/"
  cp scripts/install-agent.sh scripts/uninstall-agent.sh "$stage/scripts/"
  tar -czf "dist/meterly-linux-$arch.tar.gz" -C "$stage" meterly-agent meterly-server deploy scripts
  rm -rf "$stage"
  trap - EXIT
done
(cd dist && sha256sum meterly-*-linux-* meterly-linux-*.tar.gz > SHA256SUMS)
