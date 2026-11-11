import test from 'node:test';import assert from 'node:assert/strict';
import {parseRoute,href} from '../web/modules/router.js';
import {query} from '../web/modules/api.js';
import {esc,bytes} from '../web/modules/components.js';
test('URL filters preserve dates, IPv6, site and pagination',()=>{const state={period:'day',start:'2026-11-10',site:'a.test',ip:'2001:db8::1',page:3};const route=parseRoute(href('/ips',state));assert.equal(route.path,'/ips');for(const [k,v]of Object.entries(state))assert.equal(route.state[k],String(v));assert.equal(new URLSearchParams(query(state)).get('ip'),'2001:db8::1');});
test('untrusted keys escape HTML; byte units have defined boundaries',()=>{assert.equal(esc('<img src="x">'),'&lt;img src=&quot;x&quot;&gt;');assert.equal(bytes(1024),'1 KiB');assert.equal(bytes(0),'0 B');});
