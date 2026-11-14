import {esc,advanced} from './components.js';
import {parseRoute,update} from './router.js';

const form=document.getElementById('filters');
const advancedRoot=document.getElementById('analysisFilters');
let timer,shape='';
const today=tz=>new Intl.DateTimeFormat('en-CA',{timeZone:tz,year:'numeric',month:'2-digit',day:'2-digit'}).format(new Date());

function message(target,text,input){
 const output=target.querySelector('[data-filter-error]');
 output.textContent=text;output.hidden=!text;
 target.querySelectorAll('[aria-invalid]').forEach(el=>el.removeAttribute('aria-invalid'));
 if(text&&input)input.setAttribute('aria-invalid','true');
}
export function syncFilters({path,state},options){
 clearTimeout(timer);
 const section=path.split('/')[1];
 form.hidden=section==='system';
 for(const key of ['period','host','gran'])form.elements[key].value=state[key];
 form.elements.start.value=state.start||today(options.tz);
 form.elements.end.value=state.end||form.elements.start.value;
 document.getElementById('startLabel').hidden=!['custom','day'].includes(state.period);
 document.getElementById('endLabel').hidden=state.period!=='custom';
 message(form,'');
 const enabled=['ips','sites'].includes(section),detail=path.startsWith('/ips/');
 advancedRoot.hidden=!enabled;
 if(!enabled)return;
 const key=`${path}:${state.source}`;
 if(shape!==key){advancedRoot.innerHTML=advanced(state,{source:path==='/ips',site:path==='/sites',detail});shape=key;}
 const target=advancedRoot.querySelector('form');
 for(const input of target.elements){
  if(input.name&&input.value!==String(state[input.name]||''))input.value=state[input.name]||'';
 }
 message(target,'');
 target.querySelector('[data-clear-filters]').disabled=!['ip','search','country','asn','status'].some(k=>state[k]);
}
export function syncFilterOptions(options){
 const host=form.elements.host,selected=host.value;
 const html='<option value="all">全部主机</option>'+options.hosts.map(h=>`<option value="${esc(h.name)}">${esc(h.name)}</option>`).join('');
 if(host.innerHTML!==html){host.innerHTML=html;host.value=selected||parseRoute(location.hash).state.host;}
 document.getElementById('timezone').textContent=`时区：${options.tz}`;
}
function applyGlobal(event){
 const values=Object.fromEntries(new FormData(form));
 const dated=['day','custom'].includes(values.period);
 document.getElementById('startLabel').hidden=!dated;
 document.getElementById('endLabel').hidden=values.period!=='custom';
 if(dated&&!values.start)return message(form,'请选择开始日期',form.elements.start);
 if(values.period==='custom'&&(!values.end||values.end<values.start))return message(form,'结束日期不能早于开始日期',form.elements.end);
 if(!dated){values.start='';values.end='';}
 if(values.period==='day')values.end=values.start;
 message(form,'');
 // A new date range may exceed minute retention; let the server select a valid resolution.
 if(['period','start','end'].includes(event?.target.name))values.gran='auto';
 update({...values,from:'',to:'',page:1});
}
function validIP(value){
 const parts=value.split('/'),address=parts[0],v6=address.includes(':');
 if(parts.length>2)return false;
 if(v6){try{if(!/^[0-9a-f:.]+$/i.test(address))return false;new URL(`http://[${address}]/`);}catch{return false;}}
 else if(!/^(0|[1-9]\d{0,2})(\.(0|[1-9]\d{0,2})){3}$/.test(address)||address.split('.').some(n=>Number(n)>255))return false;
 if(parts.length===2&&(!/^\d+$/.test(parts[1])||Number(parts[1])>(v6?128:32)))return false;
 return true;
}
function applyAdvanced(target){
 clearTimeout(timer);
 if(!target?.isConnected)return;
 const fields=Object.fromEntries([...new FormData(target)].map(([k,v])=>[k,v.trim()]));
 if(fields.source==='singbox'){fields.status='';fields.site='';}
 const checks=[['ip',v=>validIP(v),'请输入完整的 IP 地址或 CIDR 网段'],['country',v=>/^[a-z]{2}$/i.test(v),'国家 / 地区请填写两位代码，例如 CN'],['asn',v=>/^(AS)?\d+$/i.test(v)&&Number(v.replace(/^AS/i,''))<=4294967295,'ASN 请填写数字或 AS 加数字'],['status',v=>/^\d{3}$/.test(v)&&Number(v)>=100&&Number(v)<=599,'状态码应在 100–599 之间']];
 for(const [name,valid,error] of checks)if(fields[name]&&!valid(fields[name]))return message(target,error,target.elements[name]);
 if(fields.country)fields.country=fields.country.toUpperCase();
 if(fields.asn)fields.asn=fields.asn.toUpperCase();
 if(fields.source==='singbox'){fields.status='';fields.site='';if(['requests','error_rate'].includes(parseRoute(location.hash).state.sort))fields.sort='total';}
 message(target,'');
 update({...fields,page:1});
}
form.addEventListener('change',applyGlobal);
form.addEventListener('submit',e=>{e.preventDefault();applyGlobal();});
advancedRoot.addEventListener('input',e=>{
 e.target.form.querySelector('[data-clear-filters]').disabled=false;
 clearTimeout(timer);if(e.isComposing)return;
 timer=setTimeout(()=>applyAdvanced(e.target.form),450);
});
advancedRoot.addEventListener('compositionend',e=>{clearTimeout(timer);timer=setTimeout(()=>applyAdvanced(e.target.form),450);});
advancedRoot.addEventListener('change',e=>{if(e.target.matches('select'))applyAdvanced(e.target.form);});
advancedRoot.addEventListener('submit',e=>{e.preventDefault();applyAdvanced(e.target);});
advancedRoot.addEventListener('click',e=>{
 if(!e.target.closest('[data-clear-filters]'))return;
 clearTimeout(timer);const patch={ip:'',country:'',asn:'',status:'',search:'',page:1};
 const target=e.target.form;
 for(const key of ['ip','country','asn','status','search'])if(target.elements[key])target.elements[key].value='';
 message(target,'');target.querySelector('[data-clear-filters]').disabled=true;update(patch);
});
