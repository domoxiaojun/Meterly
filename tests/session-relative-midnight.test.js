import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import {createRequire} from 'node:module';
const req=createRequire(import.meta.url);
const {transformSync}=await import(req.resolve('rolldown/experimental'));
const {QueryClient,QueryObserver}=req('@tanstack/react-query');
const {z}=req('zod');
const file=new URL('../frontend/src/lib/session.tsx',import.meta.url);
function load(demo=false){
 let cursor=0,stateCursor=0,effects=[]; const refs=[]; let state=[null,'',0,0];
 const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:Infinity}}});
 const noop=()=>{};
 const ctx={console,URL,Intl,Date,z,React:{createElement:()=>null},createContext:()=>({}),useContext:noop,useQueryClient:()=>client,
 useState:()=>{const i=stateCursor++;return [state[i],noop]},useRef:x=>{const i=cursor++;return refs[i]??=( {current:x})},useEffect:fn=>effects.push(fn),
 request:()=>{throw Error('unexpected network')},window:{addEventListener:noop,removeEventListener:noop},setInterval:()=>0,clearInterval:noop};
 let source=fs.readFileSync(file,'utf8').replace(/^import .*$/gm,'').replace('import.meta.env.MODE === \'demo\'',String(demo)).replace(/export /g,'');
 const out=transformSync('session.tsx',source,{jsx:{runtime:'classic'}});assert.deepEqual(out.errors,[]);
 vm.createContext(ctx);vm.runInContext(out.code,ctx);
 return {ctx,client,render(session,elapsed=0){cursor=0;stateCursor=0;effects=[];state=[session,'',0,elapsed];ctx.SessionProvider({children:null});const chosen=effects.filter(fn=>fn.toString().includes('invalidateQueries'));assert.equal(chosen.length,1);chosen[0]()}};
}
const epoch=s=>Date.parse(s)/1000;
const session={ok:true,version:'test',tz:'Asia/Shanghai',auth:true,now:epoch('2026-11-15T23:59:59+08:00')};
test('actual calendar helper respects Shanghai midnight and explicit non-default zone',()=>{
 const h=load();assert.equal(h.ctx.calendarDayKey(session.now,'Asia/Shanghai'),'2026-11-15');assert.equal(h.ctx.calendarDayKey(session.now+1,'Asia/Shanghai'),'2026-11-16');assert.equal(h.ctx.calendarDayKey(session.now+1,'America/New_York'),'2026-11-15');h.client.clear();
});
test('relative predicate preserves fixed historical queries and rejects other namespaces/endpoints',()=>{
 const h=load();const p=h.ctx.isRelativeQueryKey;
 for(const endpoint of ['/api/v2/overview','/api/v2/analytics/rank','/api/v2/analytics/series']){
  for(const period of ['today','yesterday','previous','last7d','last30d'])assert.equal(p(['meterly',`${endpoint}?period=${period}&host=h1&source=site`]),true);
  assert.equal(p(['meterly',endpoint]),true);
  for(const period of ['day','custom','invalid'])assert.equal(p(['meterly',`${endpoint}?period=${period}&start=2026-01-01`]),false);
 }
 for(const key of [[],['other','/api/v2/overview?period=yesterday'],['meterly',null],['meterly','/api/v2/analytics/options?period=yesterday'],['meterly','/api/v2/me'],['meterly','/api/v2/hosts?period=yesterday']])assert.equal(p(key),false);
 h.client.clear();
});
test('actual provider effect invalidates once on rollover and refetches only enabled relative observers',async()=>{
 const h=load();const counts={};const stops=[];
 const paths={active:'/api/v2/analytics/series?period=yesterday&host=h1&source=site&site=a.test',fixed:'/api/v2/analytics/series?period=day&start=2026-11-10',custom:'/api/v2/analytics/rank?period=custom&start=2026-10-01&end=2026-10-02',disabled:'/api/v2/analytics/rank?period=yesterday&group=status',inactive:'/api/v2/analytics/rank?period=yesterday&host=h2'};
 for(const [name,path] of Object.entries(paths)){
  const key=['meterly',path];h.client.setQueryData(key,{meta:{live:false},value:'cached'});counts[name]=0;
  if(name==='inactive')continue;
  const observer=new QueryObserver(h.client,{queryKey:key,queryFn:async()=>{counts[name]++;return {meta:{live:false},value:'fresh'}},enabled:name!=='disabled',staleTime:Infinity});stops.push(observer.subscribe(()=>{}));
 }
 h.render(session,0);assert.equal(counts.active,0);h.render(session,1);await new Promise(setImmediate);
 assert.equal(counts.active,1);assert.equal(counts.fixed,0);assert.equal(counts.custom,0);assert.equal(counts.disabled,0);assert.equal(counts.inactive,0);
 assert.equal(h.client.getQueryState(['meterly',paths.inactive]).isInvalidated,true);assert.equal(h.client.getQueryState(['meterly',paths.fixed]).isInvalidated,false);
 h.render(session,31);await new Promise(setImmediate);assert.equal(counts.active,1);
 h.render(null);h.render({...session,now:session.now+86400},0);await new Promise(setImmediate);assert.equal(counts.active,1,'fresh session must not re-invalidate');
 for(const stop of stops)stop();h.client.clear();
});
test('server timezone not browser timezone determines rollover; demo clock remains frozen',()=>{
 for(const demo of [false,true]){const h=load(demo);let calls=0;h.client.invalidateQueries=()=>{calls++;return Promise.resolve()};h.render({...session,tz:'America/New_York'},0);h.render({...session,tz:'America/New_York'},1);assert.equal(calls,0);h.client.clear();}
 const h=load(true);let calls=0;h.client.invalidateQueries=()=>{calls++;return Promise.resolve()};h.render(session,0);h.render(session,86401);assert.equal(calls,0);h.client.clear();
});
