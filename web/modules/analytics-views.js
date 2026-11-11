import {api,query} from './api.js';
import {esc,bytes,number,link,metric,banner,cards,pager,trafficTable,advanced,chartPanel,drawChart,chartData} from './components.js';
const read=(path,state,signal)=>api(path+'?'+query(state),{signal});
const exportLink=state=>`<a class="button" href="/api/v2/analytics/export?${esc(query(state))}">导出全部 CSV</a>`;
function seriesAfter(series){return ()=>{drawChart('trend',series.points,{tz:series.meta.tz,gran:series.meta.gran,site:series.source!=='singbox'});chartData('trend',series.points,series.meta.tz);};}
function proxyCards(t,m){if(!t)return '';return `<div class="metrics">${metric('下载载荷',bytes(t.tx))}${metric('上传载荷',bytes(t.rx))}${metric('已知客户端 IP',m?.ips_available===false?'不可用':number(t.ips))}${metric('涉及主机',number(t.hosts))}</div>`;}
export async function overview({state,signal}){
 const d=await read('/overview',state,signal);const growth=d.growth_pct===null?'暂无可比较数据':`${d.growth_pct>=0?'+':''}${d.growth_pct.toFixed(1)}%`;
 return {title:'流量总览',subtitle:'从总量找到网站，再追查具体 IP · 源站日志流量',meta:d.meta,html:banner(d.meta)+cards(d.site,d.meta)+`<section class="overview-strip"><div><span class="muted">活跃网站</span><strong>${number(d.site.sites)}</strong></div><div><span class="muted">在线主机</span><strong>${d.online} / ${d.host_count}</strong></div><div><span class="muted">较上一等长时段</span><strong>${esc(growth)}</strong></div><div><span class="muted">计费网卡 · 独立口径</span><strong>${bytes(d.network.rx+d.network.tx)}</strong></div></section>
 <div class="two-columns"><section class="panel"><div class="section-title"><h2>流量最多的网站</h2>${link('/sites',state,'查看全部',{source:'site',page:1})}</div>${trafficTable({rows:d.top_sites,totals:d.site},state,{group:'site',compact:true})}</section><section class="panel"><div class="section-title"><h2>流量最多的 IP</h2>${link('/ips',state,'查看全部',{source:'site',page:1})}</div>${trafficTable({rows:d.top_ips,totals:d.site},state,{compact:true})}</section></div>
 <section class="panel"><h2>需要关注</h2>${d.alerts.length?`<ul class="alert-list">${d.alerts.map(a=>`<li>${link('/hosts/'+encodeURIComponent(a.host),state,a.host)}<span>${esc(a.message)}</span></li>`).join('')}</ul>`:'<p class="muted">当前没有主机告警。</p>'}</section>`};
}
export async function sites(ctx){
 const {path,state,signal,options}=ctx;const detail=path.startsWith('/sites/');const site=detail?decodeURIComponent(path.slice(7)):'';
 const filters={...state,source:'site',group:detail?'ip':'site',site};
 const d=await read('/analytics/rank',filters,signal);
 let html=detail?`<div class="breadcrumbs">${link('/sites',state,'网站',{site:'',page:1})}<span>/</span><span>${esc(site)}</span></div>`:'';
 html+=banner(d.meta)+cards(d.totals,d.meta)+advanced({...state,source:'site'},{site:!detail});
 if(!detail && options.sites?.length)html+=`<details class="panel"><summary>已发现网站 · ${options.sites.length} 个</summary><div class="status-chips">${options.sites.map(x=>link('/sites/'+encodeURIComponent(x.site),state,x.label||x.site,{site:'',page:1})).join('')}</div></details>`;
 let series=null,status=null;
 if(detail){[series,status]=await Promise.all([read('/analytics/series',filters,signal),read('/analytics/rank',{...filters,group:'status',status:'',page:1,page_size:500,search:''},signal)]);html+=chartPanel('网站流量趋势')+`<section class="panel"><h2>请求状态</h2><div class="status-chips">${status.rows.map(r=>link(path,state,`${r.key||'未知'} · ${number(r.requests)} 次`,{status:r.key,page:1})).join('')||'<span class="muted">暂无状态码数据</span>'}</div></section>`;}
 html+=`<section class="panel"><div class="section-title"><div><h2>${detail?'客户端 IP 排行':'网站排行'}</h2><p class="muted">${detail?'按当前日期和条件统计，完整明细不受排行榜数量限制':'点击网站，查看指定日期的 IP 流量排行'}</p></div>${exportLink(filters)}</div>${trafficTable(d,state,{group:filters.group,path,site})}${pager(d,state,path)}</section>`;
 return {title:detail?site:'网站分析',subtitle:'源站日志流量 · 按日期查网站，再按流量找 IP',meta:d.meta,html,afterRender:series?seriesAfter(series):undefined};
}
export async function ips(ctx){
 const {path,state,signal}=ctx;const detail=path.startsWith('/ips/');const ip=detail?decodeURIComponent(path.slice(5)):'';
 const filters={...state,group:'ip',ip:ip||state.ip,...(detail?{page:1}: {})};
 const d=await read('/analytics/rank',filters,signal);let html='';
 if(detail)html=`<div class="breadcrumbs">${state.site?link('/sites/'+encodeURIComponent(state.site),state,state.site,{site:'',ip:'',page:1}):link('/ips',state,'IP 分析',{ip:'',page:1})}<span>/</span><span>${esc(ip)}</span></div>`;
 html+=banner(d.meta)+(state.source==='singbox'?proxyCards(d.totals,d.meta):cards(d.totals,d.meta));
 if(!detail)html+=advanced(state,{source:true});
 let series;
 if(detail){
  if(state.source==='site'){
   const status=await read('/analytics/rank',{...filters,group:'status',status:'',page:1,page_size:500,search:''},signal);
   html+=`<section class="panel"><h2>请求状态</h2><div class="status-chips">${status.rows.map(r=>link(path,state,r.key+' · '+number(r.requests)+' 次',{status:r.key,page:1})).join('')||'暂无请求状态'}</div></section>`;
  }
  const group=state.source==='site'?'site':'inbound';let associated;
  [series,associated]=await Promise.all([read('/analytics/series',filters,signal),read('/analytics/rank',{...filters,site:'',group,search:'',page:state.page,page_size:state.page_size},signal)]);
  html+=chartPanel('IP 流量趋势')+`<section class="panel"><div class="section-title"><div><h2>${group==='site'?'关联网站':'关联入站'}</h2><p class="muted">相同日期、主机及请求条件，显示该 IP 的所有关联来源</p></div>${exportLink({...filters,site:'',group})}</div>${trafficTable(associated,state,{group,path})}${pager(associated,state,path)}</section>`;
 }
 html+=`<section class="panel"><div class="section-title"><h2>${detail?'当前筛选的流量':'IP 流量排行'}</h2>${exportLink(filters)}</div>${trafficTable(d,state,{path,site:state.site})}${detail?'':pager(d,state,path)}</section>`;
 if(series)series.source=state.source;
 return {title:detail?ip:'IP 分析',subtitle:state.source==='singbox'?'sing-box 载荷 · 不与源站或网卡流量相加':'源站日志流量 · 支持 IP/CIDR、国家、ASN 和状态码筛选',html,meta:d.meta,afterRender:series?seriesAfter(series):undefined};
}
