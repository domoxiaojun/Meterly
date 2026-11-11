#!/usr/bin/env bash
# 与 Makefile 等价的构建脚本(无 make 的环境用):
#   scripts/build.sh          本机构建到 build/
#   scripts/build.sh dist     linux amd64+arm64 静态二进制到 dist/ + SHA256SUMS
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}
DATE=${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
LDFLAGS="-s -w -buildid= -X github.com/domoxiaojun/Meterly/internal/version.Version=$VERSION -X github.com/domoxiaojun/Meterly/internal/version.Commit=$COMMIT -X github.com/domoxiaojun/Meterly/internal/version.Date=$DATE"
export CGO_ENABLED=0
case "${1:-build}" in
  build)
    mkdir -p build
    for b in meterly-agent meterly-server mockclash demoseed; do
      go build -trimpath -ldflags "$LDFLAGS" -o "build/$b" "./cmd/$b"
    done
    echo "已构建 build/ ($VERSION)";;
  dist)
    mkdir -p dist
    for a in amd64 arm64; do for b in meterly-agent meterly-server; do
      echo "→ dist/$b-linux-$a"
      GOOS=linux GOARCH=$a go build -trimpath -ldflags "$LDFLAGS" -o "dist/$b-linux-$a" "./cmd/$b"
    done; done
    (cd dist && sha256sum meterly-*-linux-* > SHA256SUMS && cat SHA256SUMS);;
  *) echo "用法: $0 [build|dist]"; exit 2;;
esac
