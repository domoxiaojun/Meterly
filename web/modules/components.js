import {href} from './router.js';
export const esc = v => String(v ?? '').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
export function bytes(value) {let n=Number(value)||0;const units=['B','KiB','MiB','GiB','TiB','PiB'];let i=0;while(Math.abs(n)>=1024&&i<units.length-1){n/=1024;i++;}return `${n.toLocaleString('zh-CN',{maximumFractionDigits:i?2:0})} ${units[i]}`;}
export const number = n => Number(n||0).toLocaleString('zh-CN');
export const link = (path,state,label,patch={})=>`<a href="${esc(href(path,state,patch))}">${esc(label)}</a>`;
export const metric = (title,value,note='')=>`<article class="metric"><span class="metric-label">${esc(title)}</span><strong>${esc(value)}</strong><span class="muted">${esc(note)}</span></article>`;
export function banner(meta) {
 if(!meta)return '';
 const labels={not_collected:'尚未采集',partial:'部分数据不可用',expired:'明细已过期',zero:'当前筛选暂无流量',available:'数据可用'};
 if(meta.complete&&meta.state==='available')return '';
 return `<div class="notice ${meta.state==='expired'?'warning':''}"><strong>${labels[meta.state]||'精度说明'}</strong><span>${esc((meta.reasons||[]).join('；')||'此条件下没有已记录的请求。')}</span></div>`;
}
export function cards(totals, meta) {
 if(!totals)return '';
 if(meta?.state==='not_collected')return `<div class="metrics">${['源站下行','源站上行','请求数','客户端 IP 数'].map(k=>metric(k,'—','尚未采集')).join('')}</div>`;
 return `<div class="metrics">${metric('源站下行',bytes(totals.tx),'发送给客户端的响应字节')}${metric('源站上行',totals.rx_known?bytes(totals.rx):'不可用',totals.rx_known?'日志记录的请求长度':'日志未完整提供请求长度')}${metric('请求数',number(totals.requests),'到达源站的请求')}${metric('客户端 IP',meta?.ips_available===false?'不可用':number(totals.ips),'IP 数不等于访客人数')}</div>`;
}
export function pager(data,state,path) {
 const count=Number(data.count||0),size=Number(data.page_size||50),page=Number(data.page||1),pages=Math.max(1,Math.ceil(count/size));
 return `<div class="pager"><span class="muted">${number(count)} 条 · 第 ${page} / ${pages} 页</span><div>${page>1?link(path,state,'上一页',{page:page-1}):'<span class="disabled">上一页</span>'}${page<pages?link(path,state,'下一页',{page:page+1}):'<span class="disabled">下一页</span>'}</div></div>`;
}
export function trafficTable(data,state,{group='ip',path='/ips',site='',compact=false}={}) {
 const rows=data.rows||[];const label={ip:'客户端 IP',site:'网站',host:'主机',status:'状态码',country:'国家/地区',asn:'ASN',inbound:'入站'}[group]||'维度';
 const sortHead=(key,label)=>`<th scope="col" aria-sort="${state.sort===key?(state.order==='asc'?'ascending':'descending'):'none'}"><button class="sort" data-sort="${key}">${label}${state.sort===key?(state.order==='asc'?' ↑':' ↓'):''}</button></th>`;
 const content=rows.map((r,i)=>{
  let name=esc(r.key||'未知');
  if(group==='site')name=link(`/sites/${encodeURIComponent(r.key)}`,state,r.key,{site:'',ip:'',status:'',search:'',page:1,source:'site'});
  if(group==='ip'&&!r.special)name=link(`/ips/${encodeURIComponent(r.key)}`,state,r.key,{site:site||state.site||'',ip:'',search:'',page:1});
  if(r.special)name='<span class="muted">未能识别的来源</span>';
  const total=data.totals?.total||0,share=total>0?r.total*100/total:0;
  return `<tr><td class="rank">${(Number(data.page||1)-1)*Number(data.page_size||50)+i+1}</td><th scope="row" class="identity">${name}${group==='ip'&&r.country?`<small>${esc(r.country_name||r.country)}${r.asn?' · AS'+esc(r.asn):''} ${esc(r.org||'')}</small>`:''}</th><td class="num">${bytes(r.total)}</td><td class="num">${bytes(r.tx)}</td><td class="num">${r.rx_known?bytes(r.rx):'<span class="muted">不可用</span>'}</td><td class="num">${r.requests_known?number(r.requests):'—'}</td>${compact?'':`<td class="num">${r.requests_known?r.error_rate.toFixed(1)+'%':'—'}</td>`}<td class="num">${share.toFixed(1)}%</td></tr>`;
 }).join('');
 return `<div class="table-scroll"><table><thead><tr><th>#</th>${sortHead('key',label)}${sortHead('total','已知流量')}${sortHead('tx',state.source==='singbox'?'下载':'下行')}${sortHead('rx',state.source==='singbox'?'上传':'上行')}${sortHead('requests','请求数')}${compact?'':sortHead('error_rate','错误率')}<th>流量占比</th></tr></thead><tbody>${content||`<tr><td colspan="${compact?7:8}" class="empty">没有符合条件的数据。可调整日期或检查主机采集状态。</td></tr>`}</tbody></table></div>`;
}
export function advanced(state, {source=false,site=false}={}) {
 return `<form class="advanced" data-advanced><label>搜索<input name="search" value="${esc(state.search)}" placeholder="${site?'网站名称':'IP / 维度名称'}"></label><label>IP / CIDR<input name="ip" value="${esc(state.ip)}" placeholder="例如 192.0.2.1 或 192.0.2.0/24"></label>${source?`<label>统计来源<select name="source"><option value="site"${state.source==='site'?' selected':''}>源站日志</option><option value="singbox"${state.source==='singbox'?' selected':''}>sing-box 载荷</option></select></label>`:''}<label>国家代码<input name="country" maxlength="2" value="${esc(state.country)}" placeholder="例如 CN"></label><label>ASN<input name="asn" value="${esc(state.asn)}" placeholder="例如 AS13335"></label>${state.source!=='singbox'?`<label>状态码<input name="status" type="number" min="100" max="599" value="${esc(state.status)}" placeholder="全部"></label>`:''}<button class="button" type="submit">筛选</button><button class="button quiet" type="button" data-clear-filters>清除条件</button></form>`;
}
let charts=[];
export function clearCharts(){for(const c of charts)c.destroy();charts=[];}
export function drawChart(id,points,{tz='Asia/Singapore',gran='hour',site=true}={}){
 const canvas=document.getElementById(id);if(!canvas)return;
 const formatter=new Intl.DateTimeFormat('zh-CN',{timeZone:tz,month:'2-digit',day:'2-digit',...(gran==='day'?{}:{hour:'2-digit',minute:'2-digit'}),hour12:false});
 const color=getComputedStyle(document.documentElement).getPropertyValue('--muted').trim();
 const chart=new window.Chart(canvas,{type:gran==='day'?'bar':'line',data:{labels:points.map(p=>formatter.format(p.ts*1000)),datasets:[{label:site?'源站下行':'下载 / 出站',data:points.map(p=>p.tx),borderColor:'#52b5d9',backgroundColor:'#52b5d930',fill:true,tension:.2,borderWidth:2,pointRadius:0},{label:site?'源站上行（已知部分）':'上传 / 入站',data:points.map(p=>site&&p.rx_known===false?null:p.rx),borderColor:'#a78bfa',backgroundColor:'#a78bfa25',fill:false,borderWidth:2,pointRadius:0}]},options:{maintainAspectRatio:false,animation:matchMedia('(prefers-reduced-motion: reduce)').matches?false:{duration:150},interaction:{mode:'index',intersect:false},plugins:{legend:{labels:{color}},tooltip:{callbacks:{label:c=>`${c.dataset.label}: ${bytes(c.parsed.y)}`}}},scales:{x:{ticks:{color,maxTicksLimit:8},grid:{display:false}},y:{beginAtZero:true,ticks:{color,callback:v=>bytes(v)},grid:{color:'#88888815'}}}}});charts.push(chart);
}
export const chartPanel=(title,id='trend')=>`<section class="panel"><div class="section-title"><h2>${esc(title)}</h2><span class="muted">可在筛选栏切换时间粒度</span></div><div class="chart"><canvas id="${id}" role="img" aria-label="${esc(title)}"></canvas></div><details><summary>查看数值表</summary><div id="${id}-data"></div></details></section>`;
export function chartData(id,points,tz){const el=document.getElementById(id+'-data');if(el)el.innerHTML=`<div class="table-scroll"><table><thead><tr><th>时间</th><th>下行 / 出站</th><th>上行 / 入站</th></tr></thead><tbody>${points.map(p=>`<tr><td>${esc(new Date(p.ts*1000).toLocaleString('zh-CN',{timeZone:tz}))}</td><td>${bytes(p.tx)}</td><td>${p.rx_known===false?'不可用':bytes(p.rx)}</td></tr>`).join('')}</tbody></table></div>`;}
export function toast(message){const el=document.getElementById('toast');el.textContent=message;el.hidden=false;clearTimeout(toast.timer);toast.timer=setTimeout(()=>{el.hidden=true},4500);}
