import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..');
const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'meterly-install-review-'));
const lines = [];
try {
  // 使用独立脚本目录及假根，不调用真实 systemctl，不访问网络。
  const standalone = path.join(temp, 'standalone/scripts');
  fs.mkdirSync(standalone, { recursive: true });
  const script = path.join(standalone, 'install-agent.sh');
  fs.copyFileSync(path.join(root, 'scripts/install-agent.sh'), script);
  const stub = path.join(temp, 'agent-stub');
  fs.writeFileSync(stub, '#!/bin/sh\ncase "$1" in\n  detect) echo "review: existing old binary";;\n  write-config) shift; while [ "$#" -gt 0 ]; do case "$1" in -config) printf "{}\\n" > "$2"; exit 0;; esac; shift; done;;\nesac\n', { mode: 0o755 });
  const token = path.join(temp, 'fixture.token');
  fs.writeFileSync(token, 'review-placeholder\n', { mode: 0o600 });
  const run = (name, fakeRoot, args) => {
    const result = spawnSync('bash', [script, '--center', 'https://example.invalid', ...args], {
      env: { ...process.env, METERLY_ROOT: fakeRoot, SYSTEMCTL: path.join(temp, 'no-systemctl') },
      encoding: 'utf8', timeout: 10000,
    });
    lines.push(`CASE ${name}: exit=${result.status}`, (result.stdout + result.stderr).replaceAll(temp, '<temporary-root>'));
    return result;
  };
  const fresh = path.join(temp, 'fresh');
  const missing = run('standalone missing unit', fresh, ['--binary', stub, '--token-file', token]);
  if (missing.status !== 2 || !missing.stderr.includes('缺少 --unit-file')) throw new Error('missing-unit reproduction did not reach target branch');

  const existing = path.join(temp, 'existing');
  const existingBin = path.join(existing, 'usr/local/bin/meterly-agent');
  fs.mkdirSync(path.dirname(existingBin), { recursive: true });
  fs.copyFileSync(stub, existingBin);
  const version = run('explicit version reuses installed binary', existing, ['--version', 'v999.0.0-review', '--dry-run']);
  if (version.status !== 0 || !version.stdout.includes('review: existing old binary')) throw new Error('existing-binary reproduction failed');
  const reinstall = run('existing install copies binary onto itself', existing, ['--version', 'v999.0.0-review', '--token-file', token]);
  if (reinstall.status === 0) throw new Error('unexpected reinstall success');
} finally {
  fs.rmSync(temp, { recursive: true, force: true });
  const output = lines.join('\n');
  fs.writeFileSync(path.join(root, 'plans/project-review-20261111/install-repro.txt'), output + '\n');
  console.log(output);
}
