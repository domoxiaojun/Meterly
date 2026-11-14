import {api,query} from './api.js';
import {esc,bytes,number,link,metric,banner,cards,pager,trafficTable,locationText,siteLabel,chartPanel,drawChart,chartData} from './components.js';
const read=(path,state,signal)=>api(path+'?'+query(state),{signal});
const exportLink=state=>`<a class="button" href="/api/v2/analytics/export?${esc(query(state))}">导出全部 CSV</a>`;
function seriesAfter(series){return ()=>{drawChart('trend',series.points,{tz:series.meta.tz,gran:series.meta.gran,site:series.source!=='singbox'});chartData('trend',series.points,series.meta.tz);};}
function proxyCards(t,m){if(!t)return '';return `<div class="metrics">${metric('下载载荷',bytes(t.tx))}${metric('上传载荷',bytes(t.rx))}${metric('已知客户端 IP',m?.ips_available===false?'不可用':number(t.ips))}${metric('涉及主机',number(t.hosts))}</div>`;}
function geoNotice(options){return options.geo?.city?.loaded===false?'<div class="notice"><strong>省市归属地暂不可用</strong><span>归属地库尚未加载，可在系统页面更新归属地库。</span><a href="#/system">前往系统</a></div>':'';}
function scopeNote(state){return `<p class="scope-note">${state.site?'网站：'+esc(siteLabel(state.site))+' · ':''}${state.status?'状态码：'+esc(state.status)+' · ':''}以下明细均使用当前日期和主机条件。</p>`;}
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
 let html=detail?`<div class="breadcrumbs">${link('/sites',state,'网站',{site:'',page:1})}<span>/</span><span>${esc(siteLabel(site))}</span></div>`:'';
 html+=banner(d.meta)+cards(d.totals,d.meta)+(detail?geoNotice(options):'');
 if(!detail && options.sites?.length)html+=`<details class="panel discovery"><summary>已发现网站 · ${options.sites.length} 个</summary><div class="status-chips">${options.sites.map(x=>link('/sites/'+encodeURIComponent(x.site),state,x.label||siteLabel(x.site),{site:'',search:'',page:1})).join('')}</div></details>`;
 let series=null,status=null;
 if(detail){[series,status]=await Promise.all([read('/analytics/series',filters,signal),read('/analytics/rank',{...filters,group:'status',status:'',page:1,page_size:500,search:''},signal)]);html+=chartPanel('网站流量趋势')+`<section class="panel"><h2>请求状态</h2><div class="status-chips">${status.rows.map(r=>link(path,state,`${r.key||'未知'} · ${number(r.requests)} 次`,{status:r.key,page:1})).join('')||'<span class="muted">暂无状态码数据</span>'}</div></section>`;}
 html+=`<section class="panel"><div class="section-title"><div><h2>${detail?'客户端 IP 排行':'网站排行'}</h2><p class="muted">${detail?'点击 IP 查看归属地、流量详情和关联网站':'点击网站，查看指定日期的 IP 流量排行'} · 合计仅含已知流量</p></div>${exportLink(filters)}</div>${trafficTable(d,state,{group:filters.group,path,site})}${pager(d,state,path)}</section>`;
 return {title:detail?siteLabel(site):'网站分析',subtitle:'源站日志流量 · 按日期查网站，再按流量找 IP',meta:d.meta,html,afterRender:series?seriesAfter(series):undefined};
}
export async function ips(ctx){
 const {path,state,signal,options}=ctx;const detail=path.startsWith('/ips/');const ip=detail?decodeURIComponent(path.slice(5)):'';
 const filters={...state,group:'ip',ip:ip||state.ip,...(detail?{page:1,search:''}: {})};
 const d=await read('/analytics/rank',filters,signal);let html='';
 if(detail)html=`<div class="breadcrumbs">${state.site?link('/sites/'+encodeURIComponent(state.site),state,siteLabel(state.site),{site:'',ip:'',page:1}):link('/ips',state,'IP 分析',{ip:'',page:1})}<span>/</span><span>${esc(ip)}</span></div>`;
 html+=banner(d.meta)+geoNotice(options);
 if(!detail)html+=state.source==='singbox'?proxyCards(d.totals,d.meta):cards(d.totals,d.meta);
 let series;
 if(detail){
  const row=d.rows[0]||{},t=d.totals,proxy=state.source==='singbox';
  html+=`<section class="panel ip-profile"><div><span class="muted">IP 归属地</span><h2>${esc(locationText(row))}</h2>${!row.region&&!row.city&&!row.scope?'<p class="muted">暂无省市信息</p>':''}</div><div><span class="muted">运营商 / 组织</span><strong>${esc(row.org||'暂无信息')}</strong></div><div><span class="muted">ASN</span><strong>${row.asn?'AS'+esc(row.asn):'—'}</strong></div></section>`;
  if(t)html+=`<div class="metrics">${metric('流量合计',bytes(t.total),'下行 + 已知上行')}${metric(proxy?'下载载荷':'源站下行',bytes(t.tx))}${metric(proxy?'上传载荷':'源站上行',t.rx_known?bytes(t.rx):'不可用',t.rx_known?'':'日志未完整提供请求长度')}${metric(proxy?'涉及主机':'请求数',number(proxy?t.hosts:t.requests),proxy?'当前范围的采集主机':'到达源站的请求')}</div>`;
  html+=scopeNote(state);
  const group=proxy?'inbound':'site';let associated,hostTraffic;
  [series,associated,hostTraffic]=await Promise.all([read('/analytics/series',filters,signal),read('/analytics/rank',{...filters,group,page:state.page,page_size:state.page_size},signal),read('/analytics/rank',{...filters,group:'host',page:1,page_size:500},signal)]);
  html+=`<section class="panel"><div class="section-title"><div><h2>${proxy?'入站流量详情':'网站流量详情'}</h2><p class="muted">${proxy?'按入站查看该 IP 的上传与下载':'点击网站，继续查看该 IP 在网站内的流量'}${state.site?' · '+link(path,state,'查看所有关联网站',{site:'',page:1}):''}</p></div>${exportLink({...filters,group})}</div>${trafficTable(associated,state,{group,path,detailIP:ip})}${pager(associated,state,path)}</section>`;
  html+=chartPanel('IP 流量趋势','trend',true);
  html+=`<section class="panel"><div class="section-title"><h2>主机流量分布</h2><span class="muted">点击主机可缩小当前范围</span></div>${trafficTable(hostTraffic,state,{group:'host',path,compact:true,sortable:false})}</section>`;
  if(state.source==='site'){
   const status=await read('/analytics/rank',{...filters,group:'status',page:1,page_size:500},signal);
   html+=`<section class="panel"><div class="section-title"><h2>请求状态</h2>${state.status?link(path,state,'查看全部状态',{status:'',page:1}):''}</div>${trafficTable(status,state,{group:'status',path,compact:true,sortable:false})}</section>`;
  }
 }
 if(!detail)html+=`<section class="panel"><div class="section-title"><div><h2>IP 流量排行</h2><p class="muted">点击 IP 查看归属地、网站与时段明细 · 合计仅含已知流量</p></div>${exportLink(filters)}</div>${trafficTable(d,state,{path,site:state.site})}${pager(d,state,path)}</section>`;
 if(series)series.source=state.source;
 return {title:detail?ip:'IP 分析',subtitle:state.source==='singbox'?'sing-box 载荷 · 不与源站或网卡流量相加':'源站日志流量 · 支持 IP/CIDR、国家、ASN 和状态码筛选',html,meta:d.meta,afterRender:series?seriesAfter(series):undefined};
}
