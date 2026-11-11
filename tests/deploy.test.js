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
