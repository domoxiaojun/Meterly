import {api,APIError} from './modules/api.js';
import {parseRoute,navigate,update,startRouter} from './modules/router.js';
import {esc,clearCharts,toast,bytes} from './modules/components.js';
import {overview,sites,ips} from './modules/analytics-views.js';
import {hosts,billing,system} from './modules/manage-views.js';
import {syncFilters,syncFilterOptions} from './modules/filters.js';

let options={hosts:[],sites:[],tz:'Asia/Singapore'},controller,currentMeta,refreshTimer;
const page=document.getElementById('page');
let renderedPath='';
function scheduleRefresh(){clearTimeout(refreshTimer);refreshTimer=setTimeout(()=>{if(!document.hidden&&!document.activeElement.closest('form')&&!document.getElementById('installGuide')?.textContent)render();else scheduleRefresh();},30000);}
function showLogin(){document.getElementById('login').hidden=false;document.getElementById('loginToken').focus();}
async function render(route= parseRoute(location.hash),scroll=null){
 scroll ??= window.scrollY;
 controller?.abort();controller=new AbortController();const signal=controller.signal;currentMeta=null;clearTimeout(refreshTimer);
 syncFilters(route,options);
 page.setAttribute('aria-busy','true');document.getElementById('freshness').textContent='正在更新…';
 if(renderedPath!==route.path||!page.hasChildNodes())page.innerHTML='<div class="loading"><span></span><p>正在读取当前范围的数据…</p></div>';
 try{
  const me=await api('/me',{signal});document.getElementById('version').textContent=`Meterly ${me.version}`;document.getElementById('logout').hidden=!me.auth;
  options=await api('/analytics/options',{signal});if(signal.aborted)return;syncFilterOptions(options);
  const section=route.path.split('/')[1]||'overview';const views={overview,sites,ips,hosts,billing,system};if(!views[section])throw new Error('页面不存在');
  document.querySelectorAll('[data-nav]').forEach(a=>{const active=a.dataset.nav===section;a.classList.toggle('active',active);if(active)a.setAttribute('aria-current','page');else a.removeAttribute('aria-current');});
  const result=await views[section]({...route,signal,options});if(signal.aborted)return;
  clearCharts();document.getElementById('pageTitle').textContent=result.title;document.title=`${result.title} · Meterly`;document.getElementById('pageSubtitle').textContent=result.subtitle;
  page.innerHTML=result.html;renderedPath=route.path;page.removeAttribute('aria-busy');result.afterRender?.();currentMeta=result.meta;
  document.getElementById('freshness').textContent=`更新于 ${new Date().toLocaleTimeString('zh-CN',{timeZone:options.tz,hour12:false})}`;
  if(scroll!==null)window.scrollTo(0,scroll);
  if(result.meta?.live||['hosts','system'].includes(section))refreshTimer=setTimeout(()=>{if(!document.hidden&&!document.activeElement.closest('form')&&!document.getElementById('installGuide')?.textContent)render(parseRoute(location.hash));else scheduleRefresh();},30000);
 }catch(e){if(signal.aborted||e.name==='AbortError')return;page.removeAttribute('aria-busy');document.getElementById('freshness').textContent='更新失败';if(e instanceof APIError&&e.status===401){showLogin();page.innerHTML='';return;}page.innerHTML=`<section class="panel error-state"><h2>暂时无法显示数据</h2><p>${esc(e.message)}</p><button class="button" data-retry>重试</button></section>`;}
}
document.getElementById('refresh').addEventListener('click',()=>render());
document.getElementById('loginForm').addEventListener('submit',async e=>{
 e.preventDefault();const button=e.target.querySelector('button');button.disabled=true;document.getElementById('loginError').textContent='';
 try{const r=await fetch('/api/login',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'same-origin',body:JSON.stringify({token:document.getElementById('loginToken').value})});const d=await r.json();if(!r.ok)throw new Error(d.error||'登录失败');document.getElementById('loginToken').value='';document.getElementById('login').hidden=true;render();}catch(err){document.getElementById('loginError').textContent=err.message;}finally{button.disabled=false;}
});
document.getElementById('logout').addEventListener('click',async()=>{await fetch('/api/logout',{method:'POST',credentials:'same-origin'});controller?.abort();clearTimeout(refreshTimer);showLogin();});
function theme(value){localStorage.setItem('meterly.theme',value);document.documentElement.dataset.theme=value==='system'?(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'):value;}
const themeSelect=document.getElementById('theme');themeSelect.value=localStorage.getItem('meterly.theme')||'system';theme(themeSelect.value);themeSelect.addEventListener('change',()=>{theme(themeSelect.value);render();});matchMedia('(prefers-color-scheme: dark)').addEventListener('change',()=>{if(themeSelect.value==='system'){theme('system');render();}});
async function act(button,operation){button.disabled=true;try{await operation();}catch(e){toast(e.message);if(e.status===401)showLogin();}finally{if(button.isConnected)button.disabled=false;}}
document.addEventListener('click',e=>{
 if(e.target.closest('.skip-link')){e.preventDefault();document.getElementById('content').focus();return;}
 const nav=e.target.closest('[data-nav]');if(nav){e.preventDefault();navigate('/'+nav.dataset.nav,{search:'',ip:'',site:'',status:'',country:'',asn:'',page:1,source:'site',sort:'total'});return;}
 const sort=e.target.closest('[data-sort]');if(sort){const s=parseRoute(location.hash).state;update({sort:sort.dataset.sort,order:s.sort===sort.dataset.sort&&s.order==='desc'?'asc':'desc',page:1});return;}
 if(e.target.closest('[data-retry]')){render();return;}
 const job=e.target.closest('[data-job]');if(job)act(job,async()=>{await api('/system/jobs/'+job.dataset.job,{method:'POST',body:{}});toast('任务已提交，可在维护记录查看结果');setTimeout(()=>render(),500);});
});
document.addEventListener('submit',e=>{
 const form=e.target;
 if(!form.matches('[data-config-host],[data-host-settings],[data-upgrade-host],[data-provider-host],#enrollment'))return;e.preventDefault();const b=form.querySelector('button[type=submit],button'),f=new FormData(form);
 act(b,async()=>{
  if(form.dataset.configHost){const collectors={};for(const k of ['netdev','site','singbox','netbird','xray']){const v=f.get('collector_'+k);if(v!=='auto')collectors[k]=v==='true';}
   const csv=k=>String(f.get(k)||'').split(',').map(x=>x.trim()).filter(Boolean);
   await api('/hosts/'+encodeURIComponent(form.dataset.configHost)+'/config',{method:'PUT',body:{version:Number(form.dataset.version),policy:{collectors,interfaces:csv('interfaces').length?csv('interfaces'):null,trusted_proxies:csv('trusted_proxies'),ip_header:f.get('ip_header'),disabled_sources:f.getAll('disabled_sources')}}});toast('配置已保存，等待 agent 在采集周期边界应用');render();
  }else if(form.dataset.hostSettings){const body={...Object.fromEntries(f),version:Number(form.dataset.version)};for(const k of ['reset_day','quota'])body[k]=Number(body[k]);await api('/hosts/'+encodeURIComponent(form.dataset.hostSettings),{method:'PUT',body});toast('计费设置已保存');render();
  }else if(form.dataset.upgradeHost){const version=f.get('version');if(!confirm(`将 ${form.dataset.upgradeHost} 升级到 ${version}，会重启 agent。继续吗？`))return;await api('/hosts/'+encodeURIComponent(form.dataset.upgradeHost)+'/upgrade',{method:'POST',body:{version}});toast('升级任务已提交，结果见系统页面');
  }else if(form.dataset.providerHost){await api('/provider',{method:'POST',body:{host:form.dataset.providerHost,cycle_start:form.dataset.cycle,value:f.get('value'),note:f.get('note')}});toast('服务商用量已保存');render();
  }else if(form.id==='enrollment'){
   const center=new URL(f.get('center'));if(!['https:','http:'].includes(center.protocol))throw new Error('接入地址必须是 HTTPS 或 HTTP');if(center.username||center.password||center.search||center.hash)throw new Error('接入地址不能包含凭据、查询或片段');
   const d=await api('/enroll',{method:'POST',body:{host_name:f.get('host_name'),ttl_hours:24,max_uses:1,reissue:f.has('reissue')}});
   const shellQuote=v=>"'"+v.replaceAll("'","'\\''")+"'";
   const command=`curl -fsSL -o install-agent.sh https://raw.githubusercontent.com/domoxiaojun/Meterly/main/scripts/install-agent.sh\nchmod +x install-agent.sh\nsudo ./install-agent.sh --center ${shellQuote(center.toString().replace(/\/$/,''))} --enroll-code-file ./enroll.code${center.protocol==='http:'?' --allow-http':''}`;
   document.getElementById('installGuide').innerHTML=`<div class="notice"><strong>注册码只显示这一次</strong><span>先把下方注册码保存为 enroll.code，并设置 chmod 600 enroll.code。自签证书需在命令末尾加 --ca-file ./ca.crt。</span></div><label>注册码<input readonly value="${esc(d.code)}" aria-label="注册码"></label><pre><code>${esc(command)}</code></pre>`;
  }
 });
});
document.addEventListener('change',e=>{
 if(e.target.matches('[data-iface]'))update({iface:e.target.value});
 const input=e.target;if(input.matches('[data-vnstat-host]')&&input.files[0]){
  const host=input.dataset.vnstatHost;act(input,async()=>{const body=JSON.parse(await input.files[0].text());const d=await api('/vnstat/check?host='+encodeURIComponent(host),{method:'POST',body});const target=[...document.querySelectorAll('[data-vnstat-result]')].find(el=>el.dataset.vnstatResult===host);if(target)target.textContent=JSON.stringify(d.results,null,2);toast('vnstat 比对完成');});
 }
});
document.addEventListener('visibilitychange',()=>{if(!document.hidden&&(currentMeta?.live||['hosts','system'].includes(parseRoute(location.hash).path.split('/')[1])))render();});
startRouter(render);
