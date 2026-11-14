import test from 'node:test';import assert from 'node:assert/strict';import fs from 'node:fs';
test('TLS ingress exposes enrollment, push and outbound control; panel denies agent paths',()=>{
 const text=fs.readFileSync('deploy/compose/Caddyfile','utf8');const [agent,panel]=text.split('# panel-only');
 for(const path of ['/api/v2/ingest','/api/v2/enroll/redeem','/api/v2/agent/upgrade/status','/api/v2/agent/control'])assert.ok(agent.includes(path),path);
 for(const path of ['/api/v2/ingest','/api/v2/enroll/redeem','/api/v2/agent/*'])assert.ok(panel.includes(path),path);
 assert.ok(!text.includes('/api/v1/'));assert.ok(panel.includes('handle @ingest {\n\t\trespond "forbidden" 403'));
});
test('fresh database, one scheduler, isolated updater and complete release assets',()=>{
 for(const path of ['deploy/config/server.example.json','deploy/compose/server.json']){const c=JSON.parse(fs.readFileSync(path));assert.ok(c.db_path.endsWith('meterly-v2.db'));assert.equal(c.retention.HourDays,365);assert.equal(c.retention.MinuteDetailHours,48);assert.equal(c.maintenance.keep,7);}
 const unit=fs.readFileSync('deploy/systemd/meterly-agent-updater.service','utf8');assert.ok(unit.includes('apply-upgrade'));assert.ok(unit.includes('/var/lib/meterly-v2'));
 const release=fs.readFileSync('.github/workflows/release.yml','utf8');for(const a of ['amd64','arm64'])assert.ok(release.includes(`dist/meterly-linux-${a}.tar.gz`));
});

test('firewall rules restrict DNAT on the configured published address',async()=>{
 const {mkdtempSync,writeFileSync,readFileSync,rmSync}=fs;
 const {tmpdir}=await import('node:os');const {join}=await import('node:path');const {spawnSync}=await import('node:child_process');
 const dir=mkdtempSync(join(tmpdir(),'meterly-firewall-'));const log=join(dir,'calls');
 try {
  // PATH sandbox: this test can only invoke our fake iptables, never the host firewall.
  writeFileSync(join(dir,'iptables'),`#!/bin/sh\nprintf '%s\\n' "$*" >> "$FIREWALL_LOG"\ncase "$*" in '-S FORWARD') echo '-A FORWARD -j DOCKER-USER';; esac\n`,{mode:0o755});
  const env={PATH:dir+':/usr/bin:/bin',FIREWALL_LOG:log,AGENT_ALLOW_IPS:'203.0.113.10',INGEST_BIND_IP:'192.0.2.10',PANEL_PUBLIC_PORT:'8444',PANEL_BIND_IP:'192.0.2.20',PANEL_ALLOW_IPS:'203.0.113.30'};
  const run=spawnSync('/bin/sh',['deploy/compose/meterly-docker-user.sh'],{env,encoding:'utf8'});assert.equal(run.status,0,run.stderr);
  const rules=readFileSync(log,'utf8').split('\n').filter(x=>x.startsWith('-I DOCKER-USER'));
  assert.equal(rules.length,4);
  for(const r of rules){assert.ok(r.includes('--ctstate DNAT'));assert.ok(r.includes('--ctdir ORIGINAL'));assert.ok(r.includes('--ctorigdstport'));assert.ok(r.includes('--ctorigdst 192.0.2.'));}
  assert.ok(rules.some(r=>r.includes('--ctorigdst 192.0.2.10 --ctorigdstport 8443')));
  assert.ok(rules.some(r=>r.includes('--ctorigdst 192.0.2.20 --ctorigdstport 8444')));
  const before=readFileSync(log,'utf8');
  const bad=spawnSync('/bin/sh',['deploy/compose/meterly-docker-user.sh'],{env:{...env,INGEST_BIND_IP:''},encoding:'utf8'});assert.notEqual(bad.status,0);assert.equal(readFileSync(log,'utf8'),before,'invalid binding mutated firewall');
  const clear=spawnSync('/bin/sh',['deploy/compose/meterly-docker-user.sh'],{env:{PATH:env.PATH,FIREWALL_LOG:log,CLEAR_INGEST:'1'},encoding:'utf8'});assert.equal(clear.status,0);assert.ok(!readFileSync(log,'utf8').slice(before.length).includes('-I DOCKER-USER'));
 } finally {rmSync(dir,{recursive:true,force:true});}
});
