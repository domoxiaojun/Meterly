import {href} from './router.js';
export const esc = v => String(v ?? '').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
export function bytes(value) {let n=Number(value)||0;if(n>=2**62-1)return '超出统计范围';const units=['B','KiB','MiB','GiB','TiB','PiB'];let i=0;while(Math.abs(n)>=1024&&i<units.length-1){n/=1024;i++;}return `${n.toLocaleString('zh-CN',{maximumFractionDigits:i?2:0})} ${units[i]}`;}
export const number = n => Number(n)>=2**62-1?'超出统计范围':Number(n||0).toLocaleString('zh-CN');
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
export function locationText(row) {
 if(row.scope)return row.scope+'地址';
 const parts=[row.country_name||row.country,row.region,row.city].filter(Boolean);
 return [...new Set(parts)].join(' · ')||'暂无归属地信息';
}
export function siteLabel(site) {
 return site.startsWith('unidentified:')?'未识别网站 · '+(site.split(':')[1]||'未知主机'):site;
}
export function trafficTable(data,state,{group='ip',path='/ips',site='',compact=false,detailIP='',sortable=true}={}) {
 const rows=data.rows||[];const label={ip:'客户端 IP',site:'网站',host:'主机',status:'状态码',country:'国家/地区',asn:'ASN',inbound:'入站'}[group]||'维度';
 const geography=group==='ip'&&!compact;
 const sortHead=(key,label)=>`<th scope="col" class="${key==='key'?'':'num'}"${sortable?` aria-sort="${state.sort===key?(state.order==='asc'?'ascending':'descending'):'none'}"`:''}>${sortable?`<button class="sort" data-sort="${key}" title="按${label}排序">${label}<span aria-hidden="true">${state.sort===key?(state.order==='asc'?'↑':'↓'):'↕'}</span></button>`:label}</th>`;
 const content=rows.map((r,i)=>{
  let name=esc(r.key||'未知');
  if(group==='site')name=link(`/sites/${encodeURIComponent(r.key)}`,state,siteLabel(r.key),{site:'',ip:detailIP||state.ip||'',search:'',page:1,source:'site'});
  if(group==='ip'&&!r.special)name=link(`/ips/${encodeURIComponent(r.key)}`,state,r.key,{site:site||state.site||'',ip:'',search:'',page:1});
  if(group==='status')name=link(path,state,r.key||'未知',{status:r.key,page:1});
  if(group==='host')name=link(path,state,r.key,{host:r.key,page:1});
  if(r.special)name='<span class="muted">未能识别的来源</span>';
  const total=data.totals?.total||0,share=total>0?r.total*100/total:0;
  const geo=`${esc(locationText(r))}${r.asn||r.org?`<small>${r.asn?'AS'+esc(r.asn):''} ${esc(r.org)}</small>`:''}`;
  return `<tr><td class="rank">${(Number(data.page||1)-1)*Number(data.page_size||50)+i+1}</td><th scope="row" class="identity">${name}${group==='ip'&&compact&&!r.special?`<small>${geo}</small>`:''}</th>${geography?`<td class="geography">${r.special?'—':geo}</td>`:''}<td class="num">${bytes(r.total)}</td><td class="num">${bytes(r.tx)}</td><td class="num">${r.rx_known?bytes(r.rx):'<span class="muted" title="日志未完整提供请求长度">不可用</span>'}</td><td class="num">${r.requests_known?number(r.requests):'—'}</td>${compact?'':`<td class="num">${r.requests_known&&!r.overflow?Number(r.error_rate||0).toFixed(1)+'%':'—'}</td>`}<td class="num">${r.overflow||data.totals?.overflow?'不可用':(share>0&&share<0.1?'&lt;0.1':share.toFixed(1))+'%'}</td></tr>`;
 }).join('');
 return `<p class="table-hint">左右滑动表格，查看完整流量明细</p><div class="table-scroll" role="region" aria-label="${label}流量表" tabindex="0"><table class="traffic-table${compact?' compact':''}"><thead><tr><th scope="col" class="rank">#</th>${sortHead('key',label)}${geography?'<th scope="col">归属地 / 运营商</th>':''}${sortHead('total','流量合计')}${sortHead('tx',state.source==='singbox'?'下载':'下行')}${sortHead('rx',state.source==='singbox'?'上传':'上行')}${sortHead('requests','请求数')}${compact?'':sortHead('error_rate','错误率')}<th scope="col" class="num">流量占比</th></tr></thead><tbody>${content||`<tr><td colspan="${(compact?7:8)+(geography?1:0)}" class="empty">没有符合条件的数据。可调整日期或检查主机采集状态。</td></tr>`}</tbody></table></div>`;
}
export function advanced(state, {source=false,site=false,detail=false}={}) {
 return `<form class="advanced" data-advanced aria-label="详细筛选" novalidate>${detail?'':`<label class="search-field">搜索<input name="search" type="search" value="${esc(state.search)}" placeholder="${site?'网站名称':'客户端 IP'}"></label><label class="ip-field">IP / CIDR<input name="ip" value="${esc(state.ip)}" placeholder="IP 地址或网段" spellcheck="false"></label>`}${source?`<label>统计来源<select name="source"><option value="site"${state.source==='site'?' selected':''}>源站日志</option><option value="singbox"${state.source==='singbox'?' selected':''}>sing-box 载荷</option></select></label>`:''}<label>国家 / 地区<input name="country" maxlength="2" value="${esc(state.country)}" placeholder="全部，如 CN" spellcheck="false"></label><label>ASN<input name="asn" value="${esc(state.asn)}" placeholder="全部，如 AS13335" spellcheck="false"></label>${state.source!=='singbox'?`<label>状态码<input name="status" type="number" min="100" max="599" value="${esc(state.status)}" placeholder="全部" inputmode="numeric"></label>`:''}<button class="button quiet" type="button" data-clear-filters>清除条件</button><p class="filter-error" data-filter-error role="status" hidden></p></form>`;
}
let charts=[];
export function clearCharts(){for(const c of charts)c.destroy();charts=[];}
export function drawChart(id,points,{tz='Asia/Singapore',gran='hour',site=true}={}){
 const canvas=document.getElementById(id);if(!canvas)return;
 const formatter=new Intl.DateTimeFormat('zh-CN',{timeZone:tz,month:'2-digit',day:'2-digit',...(gran==='day'?{}:{hour:'2-digit',minute:'2-digit'}),hour12:false});
 const color=getComputedStyle(document.documentElement).getPropertyValue('--muted').trim();
 const datasets=[{label:site?'源站下行':'下载 / 出站',data:points.map(p=>({x:p.ts*1000,y:p.tx>=2**62-1?null:p.tx})),borderColor:'#52b5d9',backgroundColor:'#52b5d930',fill:true,tension:0,borderWidth:2,pointRadius:points.length===1?3:0}];
 if(points.some(p=>!site||p.rx_known!==false))datasets.push({label:site?'源站上行（已知部分）':'上传 / 入站',data:points.map(p=>({x:p.ts*1000,y:(site&&p.rx_known===false)||p.rx>=2**62-1?null:p.rx})),borderColor:'#a78bfa',backgroundColor:'#a78bfa25',fill:false,borderWidth:2,pointRadius:points.length===1?3:0});
 if(!points.length){canvas.parentElement.innerHTML='<div class="empty chart-empty">当前范围没有已记录的流量，请调整日期或筛选条件。</div>';return;}
 const unit=gran==='minute'?60000:gran==='day'?86400000:3600000;
 const first=points[0].ts*1000,last=points.at(-1).ts*1000;
 const steps=gran==='minute'?[1,5,10,15,30,60,120,360]:[1,2,3,6,12,24,48,96];
 const step=(steps.find(n=>n*unit>=(last-first)/7)||Math.ceil((last-first)/unit/7))*unit;
 const chart=new window.Chart(canvas,{type:gran==='day'?'bar':'line',data:{datasets},options:{maintainAspectRatio:false,animation:matchMedia('(prefers-reduced-motion: reduce)').matches?false:{duration:150},interaction:{mode:'index',intersect:false},plugins:{legend:{labels:{color,boxWidth:12,boxHeight:12,padding:18}},tooltip:{callbacks:{title:items=>items.length?formatter.format(items[0].parsed.x):'',label:c=>`${c.dataset.label}: ${bytes(c.parsed.y)}`}}},scales:{x:{type:'linear',min:first,max:last>first?last:first+unit,offset:gran==='day',afterBuildTicks:scale=>{const ticks=[];for(let value=first;value<=last;value+=step)ticks.push({value});if(last>first&&ticks.at(-1).value!==last)ticks.push({value:last});scale.ticks=ticks;},ticks:{color,maxTicksLimit:8,callback:v=>formatter.format(v)},grid:{display:false}},y:{beginAtZero:true,ticks:{color,callback:v=>bytes(v)},grid:{color:'#88888815'}}}}});charts.push(chart);
}
export const chartPanel=(title,id='trend',expanded=false)=>`<section class="panel"><div class="section-title"><h2>${esc(title)}</h2><span class="muted">可在筛选栏切换时间粒度</span></div><div class="chart"><canvas id="${id}" role="img" aria-label="${esc(title)}"></canvas></div><details${expanded?' open':''}><summary>按时段流量明细</summary><div id="${id}-data"></div></details></section>`;
export function chartData(id,points,tz){const el=document.getElementById(id+'-data');if(el)el.innerHTML=`<div class="table-scroll time-details" tabindex="0" role="region" aria-label="按时段流量明细"><table><thead><tr><th scope="col">时间（${esc(tz)}）</th><th scope="col" class="num">下行 / 出站</th><th scope="col" class="num">上行 / 入站</th><th scope="col" class="num">请求数</th></tr></thead><tbody>${points.map(p=>`<tr><td>${esc(new Date(p.ts*1000).toLocaleString('zh-CN',{timeZone:tz}))}</td><td class="num">${bytes(p.tx)}</td><td class="num">${p.rx_known===false?'不可用':bytes(p.rx)}</td><td class="num">${p.requests_known?number(p.requests):'—'}</td></tr>`).join('')||'<tr><td colspan="4" class="empty">当前范围无已记录数据</td></tr>'}</tbody></table></div>`;}
export function toast(message){const el=document.getElementById('toast');el.textContent=message;el.hidden=false;clearTimeout(toast.timer);toast.timer=setTimeout(()=>{el.hidden=true},4500);}
