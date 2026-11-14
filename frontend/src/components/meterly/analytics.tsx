import { useState, type ReactNode } from 'react'
import { ArrowDownLeft, ArrowUpRight, ArrowUpDown, ChevronLeft, ChevronRight, Download, Globe2, Layers3, Network, Activity, AlertCircle, Check, Info, LoaderCircle, Server, Inbox } from 'lucide-react'
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip as RechartsTooltip, XAxis, YAxis } from 'recharts'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Skeleton } from '@/components/ui/skeleton'
import { Progress } from '@/components/ui/progress'
import { useAPI, query, sessionExpired } from '@/lib/api'
import { overviewSchema, rankSchema, seriesSchema, type Metrics, type Meta, type Rank, type Row, type Series, type Overview } from '@/lib/models'
import { href, type Filters } from '@/lib/filters'
import { bytes, number, compact, dateLabel } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useSession } from '@/lib/session'

export function EmptyState({ title = '没有符合条件的数据', children }: { title?: string; children?: ReactNode }) {
  return <div className="empty-state"><div className="empty-icon"><Inbox /></div><h3>{title}</h3><p>{children || '试试调整日期或筛选条件。'}</p></div>
}
export function QueryState({ error, loading, onRetry }: { error?: Error | null; loading?: boolean; onRetry?: () => void }) {
  if (error) return <Card><CardContent className="py-10 text-center"><AlertCircle className="mx-auto mb-3 text-destructive" /><h3 className="font-medium">暂时无法加载</h3><p className="my-3 text-sm text-muted-foreground">{error.message}</p><Button variant="outline" onClick={onRetry}>重新加载</Button></CardContent></Card>
  if (loading) return <div className="space-y-5" aria-label="正在加载" role="status"><div className="grid grid-cols-2 gap-4 xl:grid-cols-4">{[0, 1, 2, 3].map(i => <Skeleton key={i} className="h-36 rounded-xl" />)}</div><Skeleton className="h-88 rounded-xl" /></div>
  return null
}
export function MetaNotice({ meta }: { meta: Meta }) {
  if (meta.complete && meta.state === 'available') return null
  return <div className="data-notice" role="status"><Info className="size-4 shrink-0" /><span><strong>{({ expired: '明细已过期', partial: '部分数据不可用', not_collected: '尚未采集', zero: '当前范围暂无流量' })[meta.state] || '数据精度说明'}</strong><span className="ml-2">{meta.reasons.join('；')}</span></span></div>
}
export function MetricGrid({ totals, meta, growth, source = 'site' }: { totals: Metrics | null; meta: Meta; growth?: number | null; source?: string }) {
  const unavailable = !totals || meta.state === 'not_collected' || meta.state === 'expired'
  const proxy = source === 'singbox'
  const items = [
    { label: proxy ? '代理载荷' : '已知流量', value: unavailable ? '—' : bytes(totals.total), icon: Layers3, note: proxy ? '上传与下载载荷，含未归因' : '下行 + 已知上行', primary: true },
    { label: proxy ? '下载载荷' : '源站请求', value: unavailable ? '—' : proxy ? bytes(totals.tx) : compact(totals.requests), icon: proxy ? ArrowDownLeft : Activity, note: proxy ? '内核记录的下载字节' : '实际到达源站的请求' },
    { label: '客户端 IP', value: unavailable || meta.ips_available === false ? '—' : number(totals.ips), icon: Network, note: '去重地址 · 不等于访客人数' },
    { label: proxy ? '涉及主机' : '活跃网站', value: unavailable ? '—' : number(proxy ? totals.hosts : totals.sites), icon: proxy ? Server : Globe2, note: proxy ? '当前条件下的采集主机' : `来自 ${totals?.hosts || 0} 台采集主机` },
  ]
  return <div className="metric-grid">{items.map(item => <Card key={item.label} className={cn('metric-card', item.primary && 'metric-primary')}><CardContent><div className="metric-top"><span>{item.label}</span><item.icon className="size-4" /></div><div className="metric-value">{item.value.split(' ').map((part, i) => i === 0 ? <span key={i}>{part}</span> : <small key={i}>{part}</small>)}</div><div className="metric-note">{item.primary && growth != null && !unavailable ? <><span className="growth"><ArrowUpRight className="size-3.5" />{growth > 0 ? '+' : ''}{growth.toFixed(1)}%</span><span>较上一等长时段</span></> : item.note}</div></CardContent></Card>)}</div>
}

export function TrafficChart({ series, filter, onChange }: { series?: Series; filter: Filters; onChange: (patch: Partial<Filters>) => void }) {
  const { tz } = useSession()
  const data = series?.points.map(p => ({ ...p, tx: p.overflow ? null : p.tx, rx: p.overflow || !p.rx_known ? null : p.rx })) || []
  const gran = series?.meta.gran
  return <Card className="chart-card"><CardHeader className="card-heading"><div><CardTitle>流量趋势</CardTitle><CardDescription>{series ? `${dateLabel(series.meta.from, false, tz)} — ${dateLabel(series.meta.to, false, tz)} · ${gran === 'day' ? '按日' : gran === 'minute' ? '按分钟' : '按小时'}汇总` : '正在载入时段数据'}</CardDescription></div><div className="segmented-control" aria-label="图表粒度">{([{ value: 'auto', label: '自动' }, { value: 'day', label: '按日' }] as const).map(item => <Button key={item.value} variant="ghost" size="sm" aria-pressed={filter.gran === item.value} onClick={() => onChange({ gran: item.value })}>{item.label}</Button>)}</div></CardHeader><CardContent><div className="chart-legend"><span><i className="legend-dot bg-primary" />{filter.source === 'singbox' ? '下载' : '源站下行'}</span><span><i className="legend-dot bg-chart-2" />{filter.source === 'singbox' ? '上传' : '已知上行'}</span><span className="ml-auto text-xs">{series?.meta.tz || 'Asia/Singapore'}</span></div><div className="traffic-chart" role="img" aria-label="当前筛选范围的流量趋势">{!series ? <Skeleton className="size-full" /> : !data.length ? <EmptyState /> : <ResponsiveContainer width="100%" height="100%"><AreaChart data={data} margin={{ top: 16, right: 12, bottom: 0, left: -12 }}><defs><linearGradient id="traffic-fill" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stopColor="var(--primary)" stopOpacity={0.2} /><stop offset="96%" stopColor="var(--primary)" stopOpacity={0.01} /></linearGradient></defs><CartesianGrid vertical={false} stroke="var(--border)" strokeDasharray="3 5" /><XAxis dataKey="ts" axisLine={false} tickLine={false} minTickGap={55} tickMargin={13} tick={{ fill: 'var(--muted-foreground)', fontSize: 11 }} tickFormatter={n => dateLabel(n, filter.period === 'today' || filter.period === 'day', tz)} /><YAxis axisLine={false} tickLine={false} tick={{ fill: 'var(--muted-foreground)', fontSize: 11 }} tickFormatter={n => bytes(n)} width={80} /><RechartsTooltip contentStyle={{ borderRadius: 12, background: 'var(--popover)', border: '1px solid var(--border)', color: 'var(--foreground)', fontSize: 12 }} labelFormatter={n => dateLabel(Number(n), true, tz)} formatter={(n, name) => [bytes(Number(n)), name === 'tx' ? '下行 / 下载' : '已知上行 / 上传']} /><Area type="monotone" dataKey="tx" stroke="var(--primary)" fill="url(#traffic-fill)" strokeWidth={2} isAnimationActive={false} connectNulls={false} /><Area type="monotone" dataKey="rx" stroke="var(--chart-2)" fill="transparent" strokeWidth={1.5} isAnimationActive={false} connectNulls={false} /></AreaChart></ResponsiveContainer>}</div>{series && <details className="chart-details"><summary>查看按时段明细</summary><div className="max-h-52 overflow-auto"><Table><TableHeader><TableRow><TableHead>时间</TableHead><TableHead>下行 / 下载</TableHead><TableHead>上行 / 上传</TableHead></TableRow></TableHeader><TableBody>{series.points.map(p => <TableRow key={p.ts}><TableCell>{dateLabel(p.ts, true, tz)}</TableCell><TableCell>{bytes(p.overflow ? null : p.tx)}</TableCell><TableCell>{p.rx_known ? bytes(p.rx) : '不可用'}</TableCell></TableRow>)}</TableBody></Table></div></details>}</CardContent></Card>
}

export function ExportButton({ filter, group }: { filter: Record<string, string | number>; group: string }) {
  const [pending, setPending] = useState(false), [error, setError] = useState('')
  async function download() {
    setPending(true); setError('')
    try {
      const response = await fetch(`/api/v2/analytics/export?${query({ ...filter, group })}`, { credentials: 'same-origin' })
      if (response.status === 401) sessionExpired()
      if (!response.ok) throw new Error(response.status === 410 ? '明细已过期，无法导出' : '导出失败，请重试')
      const blob = await response.blob(), url = URL.createObjectURL(blob), a = document.createElement('a')
      a.href = url; a.download = 'meterly-traffic.csv'; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000)
    } catch (e) { setError(e instanceof Error ? e.message : '导出失败') } finally { setPending(false) }
  }
  return <div><Button variant="outline" className="h-9 gap-2" disabled={pending} onClick={download}>{pending ? <LoaderCircle className="animate-spin" /> : <Download />}导出 CSV</Button>{error && <p role="alert" className="mt-1 text-xs text-destructive">{error}</p>}</div>
}

export function TrafficTable({ data, filter, group, onChange, compact: small = false }: { data: Rank; filter: Filters; group: string; onChange: (patch: Partial<Filters>) => void; compact?: boolean }) {
  const pages = Math.max(1, Math.ceil(data.count / data.page_size))
  const label = group === 'site' ? '网站' : group === 'ip' ? '客户端 IP' : group === 'host' ? '主机' : '入站'
  function rowHref(row: Row) {
    if (row.special) return undefined
    if (group === 'site') return href(`/sites/${encodeURIComponent(row.key)}`, filter, { page: 1, search: '', source: 'site', site: '' })
    if (group === 'ip') return href(`/ips/${encodeURIComponent(row.key)}`, filter, { page: 1, search: '', ip: '' })
    if (group === 'host') return href(`/hosts/${encodeURIComponent(row.key)}`, filter, { page: 1 })
  }
  return <><div className="table-wrap"><Table><TableHeader><TableRow><TableHead className="w-12 pl-5">#</TableHead><TableHead>{label}</TableHead><TableHead className="text-right" aria-sort={small ? undefined : filter.order === 'asc' ? 'ascending' : 'descending'}>{small ? '已知流量' : <Button variant="ghost" size="sm" className="-mr-2" onClick={() => onChange({ sort: 'total', order: filter.order === 'asc' ? 'desc' : 'asc', page: 1 })}>已知流量<ArrowUpDown className="size-3!" /></Button>}</TableHead>{!small && <><TableHead className="text-right">下行 / 下载</TableHead><TableHead className="text-right">上行 / 上传</TableHead><TableHead className="text-right">请求数</TableHead></>}<TableHead className="w-40 pr-5 text-right">占比</TableHead><TableHead className="w-8" /></TableRow></TableHeader><TableBody>{data.rows.map((r, i) => {
    const share = data.totals && !data.totals.overflow && !r.overflow && data.totals.total > 0 ? r.total / data.totals.total * 100 : null
    const destination = rowHref(r)
    return <TableRow key={r.key} className="traffic-row"><TableCell className="pl-5 text-xs text-muted-foreground">{String((data.page - 1) * data.page_size + i + 1).padStart(2, '0')}</TableCell><TableCell><div className="table-identity">{group === 'site' && <span className={cn('site-letter', `site-tone-${i % 5}`)}>{r.key.charAt(0).toUpperCase()}</span>}<div>{destination ? <a className="table-link" href={destination}>{r.key}</a> : <span>{r.special ? '未归因流量' : r.key}</span>}<small>{group === 'site' ? `${number(r.ips)} 个 IP · ${number(r.hosts)} 台主机` : r.special ? '无法对应到具体来源' : r.scope ? `${r.scope}地址` : [r.country_name, r.region, r.city].filter(Boolean).join(' · ') || '暂无归属地'}</small></div></div></TableCell><TableCell className="text-right font-medium tabular-nums">{bytes(r.overflow ? null : r.total)}</TableCell>{!small && <><TableCell className="text-right text-muted-foreground tabular-nums">{bytes(r.overflow ? null : r.tx)}</TableCell><TableCell className="text-right text-muted-foreground tabular-nums">{r.rx_known ? bytes(r.rx) : <span className="unknown-value">不可用</span>}</TableCell><TableCell className="text-right text-muted-foreground tabular-nums">{r.requests_known ? number(r.requests) : '—'}</TableCell></>}<TableCell className="pr-5"><div className="share-cell"><span>{share === null ? '—' : `${share.toFixed(1)}%`}</span><div className="share-track"><i style={{ width: `${Math.min(100, share || 0)}%` }} /></div></div></TableCell><TableCell>{destination && <a href={destination} className="row-open" aria-label={`查看 ${r.key}`}><ChevronRight className="size-4" /></a>}</TableCell></TableRow>
  })}</TableBody></Table></div>{!data.rows.length && <EmptyState />}{!small && <div className="table-footer"><span>共 {number(data.count)} 条结果 · 每页 {data.page_size} 条</span><div className="flex items-center gap-2"><span className="mr-2">{data.page} / {pages}</span><Button aria-label="上一页" variant="outline" size="icon" disabled={data.page <= 1} onClick={() => onChange({ page: data.page - 1 })}><ChevronLeft /></Button><Button aria-label="下一页" variant="outline" size="icon" disabled={data.page >= pages} onClick={() => onChange({ page: data.page + 1 })}><ChevronRight /></Button></div></div>}</>
}

function HealthCard({ overview, filter }: { overview: Overview; filter: Filters }) {
  return <Card className="health-card"><CardHeader><CardTitle>采集概况</CardTitle><CardDescription>看清数据背后的运行状态</CardDescription></CardHeader><CardContent><div className="online-number">{overview.online}<span>/ {overview.host_count}</span><Badge className="status-badge ml-auto" variant="outline"><span className="status-dot" />主机在线</Badge></div><Progress aria-label="在线主机比例" value={overview.host_count ? overview.online / overview.host_count * 100 : 0} className="my-5" /><div className="health-detail"><span>时间覆盖</span><span className={overview.meta.complete ? 'status-positive' : 'status-warning'}>{overview.meta.complete ? <Check className="size-3.5" /> : <Info className="size-3.5" />}{overview.meta.complete ? '完整' : '部分缺失'}</span></div><div className="health-detail"><span>日志上行字段</span><span className={overview.site.rx_known ? 'status-positive' : 'status-warning'}>{overview.site.rx_known ? '已知' : '部分不可用'}</span></div><div className="health-detail"><span>分析时区</span><span>{overview.meta.tz}</span></div><a className="health-link" href={href('/hosts', filter)}>查看所有主机<ArrowUpRight className="size-4" /></a></CardContent></Card>
}
export function OverviewPage({ filter, onChange }: { filter: Filters; onChange: (patch: Partial<Filters>) => void }) {
  const f = { ...filter, source: 'site' as const }
  const overview = useAPI(`/api/v2/overview?${query(f)}`, overviewSchema)
  const series = useAPI(`/api/v2/analytics/series?${query(f)}`, seriesSchema)
  if (!overview.data) return <QueryState loading={overview.isPending} error={overview.error} onRetry={() => overview.refetch()} />
  const d = overview.data
  return <div className="page-sections"><MetaNotice meta={d.meta} /><MetricGrid totals={d.site} meta={d.meta} growth={d.growth_pct} /><div className="overview-charts"><div>{series.error ? <QueryState error={series.error} onRetry={() => series.refetch()} /> : <TrafficChart series={series.data} filter={f} onChange={onChange} />}</div><HealthCard overview={d} filter={filter} /></div><div className="overview-bottom"><Card className="overflow-hidden"><CardHeader className="card-heading"><div><CardTitle>流量最多的网站</CardTitle><CardDescription>从主要来源开始，继续追查 IP</CardDescription></div><a className="text-link" href={href('/sites', filter, { source: 'site', page: 1 })}>查看全部<ArrowUpRight className="size-3.5" /></a></CardHeader><TrafficTable data={{ rows: d.top_sites, totals: d.site, count: d.top_sites.length, page: 1, page_size: 5, meta: d.meta }} filter={filter} group="site" onChange={onChange} compact /></Card><Card><CardHeader className="card-heading"><CardTitle>需要关注</CardTitle><Badge variant="secondary">{d.alerts.length}</Badge></CardHeader><CardContent className="alerts-list">{d.alerts.length ? d.alerts.map(a => <a key={a.host + a.kind} href={href(`/hosts/${a.host}`, filter)} className="alert-item"><span className={cn('alert-icon', a.kind === 'offline' ? 'alert-offline' : '')}><AlertCircle className="size-4" /></span><div><strong>{a.kind === 'offline' ? '主机离线' : a.kind === 'backlog' ? '数据等待补发' : '采集异常'}</strong><span>{a.host}</span><p>{a.message}</p></div><ChevronRight className="ml-auto size-4 text-muted-foreground" /></a>) : <div className="py-6 text-sm text-muted-foreground">当前没有采集告警。</div>}<div className="quiet-tip"><Info className="size-4 shrink-0" /><p>源站日志、代理载荷与网卡计费口径不同，请分开查看。</p></div></CardContent></Card></div></div>
}

export function AnalysisPage({ filter, path, onChange }: { filter: Filters; path: string; onChange: (patch: Partial<Filters>) => void }) {
  const sitePage = path.startsWith('/sites'), detail = path.split('/').length > 2
  const identity = detail ? decodeURIComponent(path.split('/').slice(2).join('/')) : ''
  const source = sitePage ? 'site' : filter.source
  const group = sitePage && !detail ? 'site' : 'ip'
  const f = { ...filter, source, group, page_size: 10, ...(sitePage && detail ? { site: identity } : {}), ...(!sitePage && detail ? { ip: identity, page: 1, search: '' } : {}) }
  const rank = useAPI(`/api/v2/analytics/rank?${query(f)}`, rankSchema)
  const series = useAPI(`/api/v2/analytics/series?${query(f)}`, seriesSchema)
  const associated = useAPI(`/api/v2/analytics/rank?${query({ ...f, group: sitePage ? 'status' : source === 'singbox' ? 'inbound' : 'site', page: !sitePage && detail ? filter.page : 1, ...(sitePage ? { status: '' } : {}) })}`, rankSchema)
  if (!rank.data) return <QueryState loading={rank.isPending} error={rank.error} onRetry={() => rank.refetch()} />
  const d = rank.data
  const row = !sitePage && detail ? d.rows[0] : undefined
  return <div className="page-sections">{detail && <div className="detail-breadcrumb"><a href={href(sitePage ? '/sites' : '/ips', filter, { page: 1, ip: '' })}><ChevronLeft className="size-4" />返回{sitePage ? '网站' : 'IP'}列表</a><span>/</span><strong>{identity}</strong></div>}<MetaNotice meta={d.meta} /><MetricGrid totals={d.totals} meta={d.meta} source={source} />{row && <Card><CardContent className="ip-profile"><Network className="size-7 text-primary" /><div><small>地址归属</small><strong>{row.scope ? `${row.scope}地址` : row.country_name || '暂无归属地'}</strong></div><div><small>运营商 / 组织</small><strong>{row.org || '暂无信息'}</strong></div><div><small>ASN</small><strong>{row.asn ? `AS${row.asn}` : '—'}</strong></div></CardContent></Card>}{detail && <TrafficChart series={series.data} filter={{ ...filter, source }} onChange={onChange} />}<Card className="overflow-hidden"><CardHeader className="card-heading"><div><CardTitle>{!sitePage && detail ? source === 'singbox' ? '关联入站' : '关联网站' : group === 'site' ? '网站流量排行' : '客户端 IP 排行'}</CardTitle><CardDescription>{source === 'singbox' ? '内核载荷 · 未归因量单独保留' : '服务端分页 · 合计仅包含已知流量'}</CardDescription></div><ExportButton filter={f} group={!sitePage && detail ? source === 'singbox' ? 'inbound' : 'site' : group} /></CardHeader>{!sitePage && detail ? associated.data ? <TrafficTable data={associated.data} filter={{ ...filter, ip: identity }} group={source === 'singbox' ? 'inbound' : 'site'} onChange={onChange} /> : <QueryState loading={associated.isPending} error={associated.error} onRetry={() => associated.refetch()} /> : <TrafficTable data={d} filter={sitePage && detail ? { ...filter, site: identity } : filter} group={group} onChange={onChange} />}</Card>{sitePage && detail && associated.data && <Card><CardHeader><CardTitle>请求状态</CardTitle></CardHeader><CardContent className="flex flex-wrap gap-3">{associated.data.rows.map(r => <Button key={r.key} variant={filter.status === r.key ? 'default' : 'outline'} className="h-10" onClick={() => onChange({ status: filter.status === r.key ? '' : r.key, page: 1 })}>{r.key}<span className="ml-2 opacity-70">{number(r.requests)} 次</span></Button>)}</CardContent></Card>}</div>
}
