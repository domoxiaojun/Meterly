#!/usr/bin/env bash
# 安装器隔离测试：真实本机 agent，假的发行下载及 systemctl，不修改宿主系统。
set -euo pipefail
cd "$(dirname "$0")/.."
repo=$PWD
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/releases" "$tmp/standalone"
export METERLY_ROOT="$tmp/root" METERLY_TEST_RELEASES="$tmp/releases" METERLY_TEST_LOG="$tmp/calls"
cp scripts/install-agent.sh "$tmp/standalone/"
for tag in v2.0.0 v2.0.1; do
  stage="$tmp/$tag"
  mkdir -p "$stage/deploy/systemd" "$tmp/releases/$tag"
  go build -ldflags "-X github.com/domoxiaojun/Meterly/internal/version.Version=$tag" -o "$stage/meterly-agent" ./cmd/meterly-agent
  cp deploy/systemd/meterly-agent*.service "$stage/deploy/systemd/"
  case "$(uname -m)" in arm64|aarch64) arch=arm64;; *) arch=amd64;; esac
  tar -czf "$tmp/releases/$tag/meterly-linux-$arch.tar.gz" -C "$stage" meterly-agent deploy
  (cd "$tmp/releases/$tag" && shasum -a 256 ./*.tar.gz | sed 's|  ./|  |' > SHA256SUMS)
done
cat > "$tmp/bin/curl" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
out=''; url=''
while [ $# -gt 0 ]; do case "$1" in -o) out=$2;shift 2;; --proto|--max-time|--cacert) shift 2;; -*) shift;; *) url=$1;shift;; esac;done
printf '%s\n' "$url" >> "$METERLY_TEST_LOG"
case "$url" in
 */healthz) printf '{"ok":true}\n';;
 */releases/latest) printf '{"tag_name":"v2.0.1"}\n';;
 */releases/download/*) path=${url##*/download/}; cp "$METERLY_TEST_RELEASES/$path" "$out";;
 *) exit 99;;
esac
MOCK
cat > "$tmp/bin/systemctl" <<'MOCK'
#!/usr/bin/env bash
printf 'systemctl %s\n' "$*" >> "$METERLY_TEST_LOG"
exit 0
MOCK
chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH" SYSTEMCTL="$tmp/bin/systemctl"
printf 'installer-test-placeholder\n' > "$tmp/token"
chmod 600 "$tmp/token"
installer="$tmp/standalone/install-agent.sh"
# standalone: only the installer exists locally; all units come from the verified bundle.
"$installer" --center https://fixture.invalid --token-file "$tmp/token" --version v2.0.0 > "$tmp/first.log"
grep -q "已安装 v2.0.0" "$tmp/first.log"
test -f "$METERLY_ROOT/etc/systemd/system/meterly-agent-updater.service"
test -f "$METERLY_ROOT/etc/systemd/system/meterly-agent.service"
"$METERLY_ROOT/usr/local/bin/meterly-agent" version | grep -q 'v2.0.0'
# Preserve user overrides, original token, and config during an explicit-version update.
node - "$METERLY_ROOT/etc/meterly/agent.json" <<'JS'
const fs=require('fs'),p=process.argv[2],c=JSON.parse(fs.readFileSync(p));c.overrides={collectors:{site:false}};fs.writeFileSync(p,JSON.stringify(c));
JS
before=$(shasum -a 256 "$METERLY_ROOT/etc/meterly/agent.token")
"$installer" --center https://fixture.invalid --version v2.0.1 > "$tmp/second.log"
"$METERLY_ROOT/usr/local/bin/meterly-agent" version | grep -q 'v2.0.1'
test "$before" = "$(shasum -a 256 "$METERLY_ROOT/etc/meterly/agent.token")"
node - "$METERLY_ROOT/etc/meterly/agent.json" <<'JS'
const fs=require('fs');if(JSON.parse(fs.readFileSync(process.argv[2])).overrides.collectors.site!==false)process.exit(1);
JS
# Same-file source must stage atomically, not fail with “same file”.
"$repo/scripts/install-agent.sh" --center https://fixture.invalid --binary "$METERLY_ROOT/usr/local/bin/meterly-agent" --version v2.0.1 > "$tmp/repeat.log"
# Missing package files fail before enrollment or systemd activation.
rm -f "$tmp/calls"
if "$installer" --center https://fixture.invalid --binary "$METERLY_ROOT/usr/local/bin/meterly-agent" --enroll-code-file - < /dev/null > "$tmp/fail.log" 2>&1; then echo 'missing units should fail'; exit 1; fi
test ! -e "$tmp/calls"
# Do not implicitly downgrade transport security.
if "$installer" --center http://fixture.invalid --token-file "$tmp/token" > /dev/null 2>&1;then echo 'implicit HTTP accepted';exit 1;fi
echo 'PASS: standalone bundle, explicit version, repeated install, preserved config/token, preflight and HTTP policy'
