import test from 'node:test';
import assert from 'node:assert/strict';
import {bytes,number,trafficTable} from '../web/modules/components.js';
test('overflow metrics are unavailable instead of precise numbers or percentages',()=>{
 assert.equal(bytes(2**62-1),'超出统计范围');assert.equal(number(2**62-1),'超出统计范围');assert.equal(bytes(1024),'1 KiB');
 const html=trafficTable({rows:[{key:'a.test',total:2**62-1,tx:2**62-1,requests:2**62-1,requests_known:true,overflow:true}],totals:{total:2**62-1,overflow:true}},{},{group:'site'});
 assert.ok(html.includes('超出统计范围'));assert.ok(!html.includes('100.0%'));assert.ok(html.includes('不可用'));
});
