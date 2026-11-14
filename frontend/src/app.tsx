import { useEffect, useRef, useState, type CSSProperties } from 'react'
import { useRouterState } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Activity, ArrowUpRight, Bell, CalendarDays, Check, ChevronDown, ChevronRight, Clock3, Command, Globe2, LayoutDashboard, ListFilter, Monitor, Moon, Network, RefreshCw, Search, Server, Settings2, Sun, Wallet, X, Zap } from 'lucide-react'
import { zhCN } from 'date-fns/locale'
import type { DateRange } from 'react-day-picker'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Calendar } from '@/components/ui/calendar'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { Sidebar, SidebarContent, SidebarFooter, SidebarGroup, SidebarGroupContent, SidebarGroupLabel, SidebarHeader, SidebarInset, SidebarMenu, SidebarMenuButton, SidebarMenuItem, SidebarProvider, SidebarTrigger, useSidebar } from '@/components/ui/sidebar'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useIsMobile } from '@/hooks/use-mobile'
import { OverviewPage, AnalysisPage, QueryState } from '@/components/meterly/analytics'
import { BillingPage, HostsPage, SystemPage } from '@/components/meterly/management'
import { useAPI } from '@/lib/api'
import { optionsSchema, overviewSchema, previewSchema, type Options } from '@/lib/models'
import { parseFilters, href, validateFilters, periodLabels, type Filters } from '@/lib/filters'
import { cn } from '@/lib/utils'

const navigation = [
  { title: '分析', items: [{ path: '/', name: '流量总览', icon: LayoutDashboard }, { path: '/sites', name: '网站分析', icon: Globe2 }, { path: '/ips', name: 'IP 分析', icon: Network }] },
  { title: '管理', items: [{ path: '/hosts', name: '主机管理', icon: Server }, { path: '/billing', name: '计费与对账', icon: Wallet }, { path: '/system', name: '系统设置', icon: Settings2 }] },
]
const pageCopy: Record<string, { title: string; subtitle: string }> = {
  '/': { title: '流量总览', subtitle: '从网站到 IP，看清流量的来处。' },
  '/sites': { title: '网站分析', subtitle: '发现主要流量来源，深入每一个网站。' },
  '/ips': { title: 'IP 分析', subtitle: '从客户端地址追查网站、入站与流量分布。' },
  '/hosts': { title: '主机管理', subtitle: '让每台主机的采集状态清晰可见。' },
  '/billing': { title: '计费与对账', subtitle: '掌握当前周期用量，提前了解配额。' },
  '/system': { title: '系统设置', subtitle: '数据保留、运行维护与新主机接入。' },
}
function WorkspaceSidebar({ path, filter, hostCount }: { path: string; filter: Filters; hostCount: number }) {
  const { setOpenMobile } = useSidebar()
  return <Sidebar collapsible="icon" className="workspace-sidebar"><SidebarHeader className="px-5 pt-6 pb-5 group-data-[collapsible=icon]:px-2"><a href={href('/', filter)} className="brand"><span className="brand-mark"><Activity /></span><span className="group-data-[collapsible=icon]:hidden">Meterly<span className="brand-dot">.</span></span></a><div className="workspace-chip group-data-[collapsible=icon]:hidden"><span className="workspace-avatar">M</span><div><strong>演示工作区</strong><small>流量分析工作台</small></div><Badge variant="outline">预览</Badge></div></SidebarHeader><SidebarContent className="px-3">{navigation.map(group => <SidebarGroup key={group.title} className="p-0 pb-5"><SidebarGroupLabel className="mb-2 px-3 text-[11px] tracking-widest">{group.title}</SidebarGroupLabel><SidebarGroupContent><SidebarMenu className="gap-1">{group.items.map(item => <SidebarMenuItem key={item.path}><SidebarMenuButton render={<a href={href(item.path, filter, { page: 1, search: '', ip: '', site: '', country: '', asn: '', status: '', ...(item.path === '/sites' ? { source: 'site' } : {}) })} />} onClick={() => setOpenMobile(false)} isActive={item.path === '/' ? path === '/' : path.startsWith(item.path)} tooltip={item.name} className="nav-button h-11 gap-3 px-3"><item.icon /><span>{item.name}</span>{item.path === '/hosts' && <span className="nav-count ml-auto group-data-[collapsible=icon]:hidden">{hostCount}</span>}</SidebarMenuButton></SidebarMenuItem>)}</SidebarMenu></SidebarGroupContent></SidebarGroup>)}</SidebarContent><SidebarFooter className="gap-4 p-4 group-data-[collapsible=icon]:p-2"><div className="sidebar-note group-data-[collapsible=icon]:hidden"><span><Zap className="size-4" />轻量采集，清晰洞察</span><p>网站、代理与网卡口径分开呈现，数据限制明确可见。</p></div><a className="sidebar-footer-link" href={href('/system', filter)}><span className="footer-status" /><span className="group-data-[collapsible=icon]:hidden">本地演示环境<span className="ml-auto text-xs">v0.1 预览</span></span></a></SidebarFooter></Sidebar>
}
function ThemePicker() {
  const [theme, setTheme] = useState(() => localStorage.getItem('meterly-theme') || 'light')
  useEffect(() => {
    const media = window.matchMedia('(prefers-color-scheme: dark)')
    const apply = () => document.documentElement.classList.toggle('dark', theme === 'dark' || (theme === 'system' && media.matches))
    apply(); localStorage.setItem('meterly-theme', theme); media.addEventListener('change', apply)
    return () => media.removeEventListener('change', apply)
  }, [theme])
  return <DropdownMenu><DropdownMenuTrigger render={<Button variant="ghost" size="icon" className="size-9" aria-label="切换主题" />}><Sun className="size-4 dark:hidden" /><Moon className="hidden size-4 dark:block" /></DropdownMenuTrigger><DropdownMenuContent align="end">{[{ value: 'light', label: '浅色主题', icon: Sun }, { value: 'dark', label: '深色主题', icon: Moon }, { value: 'system', label: '跟随系统', icon: Monitor }].map(item => <DropdownMenuItem key={item.value} onClick={() => setTheme(item.value)}><item.icon />{item.label}{theme === item.value && <Check className="ml-auto" />}</DropdownMenuItem>)}</DropdownMenuContent></DropdownMenu>
}
function QuickSearch({ options, filter }: { options: Options; filter: Filters }) {
  const [open, setOpen] = useState(false), [search, setSearch] = useState('')
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => { if ((event.metaKey || event.ctrlKey) && event.key === 'k') { event.preventDefault(); setOpen(value => !value) } }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
  const sites = options.sites.filter(s => s.site.includes(search)).slice(0, 5), hosts = options.hosts.filter(h => h.name.includes(search)).slice(0, 5)
  return <Dialog open={open} onOpenChange={setOpen}><DialogTrigger render={<Button variant="ghost" className="quick-search" />}><Search className="size-4" /><span>快速查找</span><kbd>⌘ K</kbd></DialogTrigger><DialogContent className="search-dialog"><DialogHeader><DialogTitle>快速查找</DialogTitle><DialogDescription>跳转到网站或主机的详情</DialogDescription></DialogHeader><Input aria-label="搜索网站或主机" placeholder="输入网站或主机名称…" value={search} onChange={e => setSearch(e.target.value)} /><div className="search-results"><p>网站</p>{sites.map(s => <a key={s.site} href={href(`/sites/${s.site}`, filter, { page: 1, site: '', source: 'site', search: '' })} onClick={() => setOpen(false)}><Globe2 className="size-4" />{s.site}<ArrowUpRight className="ml-auto size-3.5" /></a>)}<p>主机</p>{hosts.map(h => <a key={h.name} href={href(`/hosts/${h.name}`, filter)} onClick={() => setOpen(false)}><Server className="size-4" />{h.name}<ArrowUpRight className="ml-auto size-3.5" /></a>)}{!sites.length && !hosts.length && <span className="block p-4 text-sm text-muted-foreground">没有找到匹配结果</span>}</div></DialogContent></Dialog>
}
function DatePicker({ filter, onChange }: { filter: Filters; onChange: (patch: Partial<Filters>) => void }) {
  const [open, setOpen] = useState(false), [range, setRange] = useState<DateRange | undefined>()
  const mobile = useIsMobile()
  const ymd = (date: Date) => `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
  return <Popover open={open} onOpenChange={setOpen}><PopoverTrigger render={<Button variant="outline" className="filter-select" />}><CalendarDays className="size-4" />{filter.period === 'custom' ? `${filter.start} → ${filter.end}` : periodLabels[filter.period]}<ChevronDown className="ml-3 size-3.5 text-muted-foreground" /></PopoverTrigger><PopoverContent align="end" className="date-popover"><div className="date-presets">{(['today', 'yesterday', 'last7d', 'last30d'] as const).map(period => <Button variant={filter.period === period ? 'secondary' : 'ghost'} key={period} onClick={() => { onChange({ period, start: '', end: '', page: 1, gran: 'auto' }); setOpen(false) }}>{periodLabels[period]}</Button>)}</div><Calendar mode="range" locale={zhCN} numberOfMonths={mobile ? 1 : 2} defaultMonth={new Date(2026, mobile ? 10 : 9, 1)} selected={range} onSelect={setRange} disabled={{ after: new Date(2026, 10, 14) }} /><div className="date-apply"><span>{range?.from ? ymd(range.from) : '选择开始日期'}{range?.to ? ` → ${ymd(range.to)}` : ''}</span><Button disabled={!range?.from || !range?.to} onClick={() => { if (range?.from && range.to) { onChange({ period: 'custom', start: ymd(range.from), end: ymd(range.to), gran: 'auto', page: 1 }); setOpen(false) } }}>应用日期</Button></div></PopoverContent></Popover>
}
function FilterBar({ path, filter, options, onChange }: { path: string; filter: Filters; options: Options; onChange: (patch: Partial<Filters>) => void }) {
  const analysis = path === '/' || path.startsWith('/sites') || path.startsWith('/ips'), list = path === '/sites' || path === '/ips'
  const field = path === '/ips' ? 'ip' : 'search'
  const [draft, setDraft] = useState(filter[field]), [error, setError] = useState('')
  const [advanced, setAdvanced] = useState({ country: filter.country, asn: filter.asn, status: filter.status })
  const composing = useRef(false)
  const [isComposing, setIsComposing] = useState(false)
  useEffect(() => { setDraft(filter[field]); setError('') }, [field, filter[field]])
  useEffect(() => { setAdvanced({ country: filter.country, asn: filter.asn, status: filter.status }) }, [filter.country, filter.asn, filter.status])
  useEffect(() => {
    if (!list || isComposing || composing.current || draft === filter[field]) return
    const timeout = setTimeout(() => { const next = { ...filter, [field]: draft.trim(), page: 1 }; const invalid = validateFilters(next); setError(invalid); if (!invalid) onChange({ [field]: draft.trim(), page: 1 }) }, 450)
    return () => clearTimeout(timeout)
  }, [draft, isComposing, field, filter, list, onChange])
  const queryClient = useQueryClient()
  const [refreshing, setRefreshing] = useState(false)
  async function refresh() { setRefreshing(true); await queryClient.invalidateQueries({ queryKey: ['meterly'] }); setRefreshing(false) }
  const activeCount = [filter.country, filter.asn, filter.status].filter(Boolean).length
  return <div className="filters-wrap"><div className="filter-bar"><Select value={filter.host} onValueChange={value => value && onChange({ host: value, page: 1 })}><SelectTrigger aria-label="选择主机" className="filter-select"><Server className="size-3.5" /><SelectValue>{filter.host === 'all' ? '全部主机' : filter.host}</SelectValue></SelectTrigger><SelectContent><SelectItem value="all">全部主机</SelectItem>{options.hosts.map(h => <SelectItem key={h.name} value={h.name}>{h.name}</SelectItem>)}</SelectContent></Select>{list && <div className="search-field"><Search className="size-4" /><Input aria-label={field === 'ip' ? '筛选 IP 或 CIDR' : '搜索网站'} placeholder={field === 'ip' ? '搜索 IP 或 CIDR…' : '搜索网站…'} value={draft} onChange={e => setDraft(e.target.value)} onCompositionStart={() => { composing.current = true; setIsComposing(true) }} onCompositionEnd={e => { composing.current = false; setIsComposing(false); setDraft(e.currentTarget.value) }} /></div>}{list && <Popover><PopoverTrigger render={<Button variant="outline" className="filter-select" />}><ListFilter />筛选{activeCount > 0 && <Badge>{activeCount}</Badge>}</PopoverTrigger><PopoverContent align="start" className="p-4"><strong className="mb-2">更多筛选条件</strong>{([{ key: 'country', label: '国家代码', placeholder: '例如 SG' }, { key: 'asn', label: 'ASN', placeholder: '例如 AS64500' }, ...(filter.source === 'site' ? [{ key: 'status', label: '状态码', placeholder: '例如 200' }] : [])] as { key: keyof typeof advanced; label: string; placeholder: string }[]).map(item => <label className="filter-field" key={item.key}>{item.label}<Input value={advanced[item.key]} placeholder={item.placeholder} onChange={e => setAdvanced({ ...advanced, [item.key]: e.target.value })} /></label>)}<Button className="mt-2" onClick={() => { const next = { ...filter, ...advanced }; const invalid = validateFilters(next); setError(invalid); if (!invalid) onChange({ ...advanced, page: 1 }) }}>应用筛选</Button></PopoverContent></Popover>}<div className="filter-spacer" />{analysis ? <DatePicker filter={filter} onChange={onChange} /> : <span className="text-xs text-muted-foreground">{path === '/billing' ? '当前计费周期' : '采集运行状态'}</span>}<Tooltip><TooltipTrigger render={<Button variant="outline" size="icon" className="size-10" aria-label="刷新数据" disabled={refreshing} onClick={refresh} />}><RefreshCw className={cn('size-3.5', refreshing && 'animate-spin')} /></TooltipTrigger><TooltipContent>刷新当前数据</TooltipContent></Tooltip></div>{(activeCount > 0 || filter.ip || filter.search || filter.site) && <div className="active-filters">{Object.entries({ 网站: filter.site, IP: filter.ip, 关键字: filter.search, 国家: filter.country, ASN: filter.asn, 状态码: filter.status }).filter(([, value]) => value).map(([key, value]) => <Badge key={key} variant="secondary">{key}：{value}</Badge>)}<Button variant="ghost" size="sm" onClick={() => onChange({ site: '', ip: '', search: '', country: '', asn: '', status: '', page: 1 })}><X className="size-3" />清除筛选</Button></div>}{error && <p className="filter-error" role="alert">{error}</p>}</div>
}
export default function App() {
  const route = useRouterState({ select: state => state.location })
  const path = route.pathname, basePath = '/' + path.split('/')[1]
  const filter = parseFilters(route.search)
  const options = useAPI('/api/v2/analytics/options', optionsSchema)
  const preview = useAPI('/api/preview', previewSchema)
  const overview = useAPI('/api/v2/overview?period=last7d', overviewSchema)
  const onChange = (patch: Partial<Filters>) => { window.location.hash = href(path, filter, patch).slice(1) }
  const copy = pageCopy[basePath] || pageCopy['/']
  const invalid = validateFilters(filter)
  if (!options.data || !preview.data) return <div className="boot-state"><span className="brand-mark"><Activity /></span><h1>Meterly</h1><p>正在连接本地演示工作区</p><div className="w-full max-w-lg"><QueryState loading={options.isPending || preview.isPending} error={options.error || preview.error} onRetry={() => { options.refetch(); preview.refetch() }} /></div></div>
  return <SidebarProvider style={{ '--sidebar-width': '15rem', '--sidebar-width-icon': '4rem' } as CSSProperties}><a className="skip-link" href="#main-content" onClick={e => { e.preventDefault(); document.getElementById('main-content')?.focus() }}>跳转到内容</a><WorkspaceSidebar path={path} filter={filter} hostCount={options.data.hosts.length} /><SidebarInset className="min-w-0 bg-background"><header className="topbar"><SidebarTrigger aria-label="展开或收起导航" className="size-9" /><span className="header-divider" /><div className="breadcrumb"><span>工作区</span><ChevronRight className="size-3" /><strong>{copy.title}</strong></div><div className="topbar-actions"><QuickSearch options={options.data} filter={filter} /><ThemePicker /><Popover><PopoverTrigger render={<Button variant="ghost" size="icon" className="notification-trigger size-9" aria-label="查看通知" />}><Bell className="size-4" />{!!overview.data?.alerts.length && <i />}</PopoverTrigger><PopoverContent align="end" className="w-80 p-5"><strong>需要关注</strong>{overview.data?.alerts.map(a => <a key={a.kind + a.host} href={href(`/hosts/${a.host}`, filter)} className="notification-item"><span>{a.host}</span><p>{a.message}</p></a>)}</PopoverContent></Popover><span className="header-divider" /><span className="user-avatar" aria-label="演示工作区">M</span></div></header><main id="main-content" tabIndex={-1} className="workspace-main"><div className="page-title-row"><div><div className="page-eyebrow">METERLY ANALYTICS<Badge variant="outline">演示数据</Badge></div><h1>{copy.title}</h1><p>{copy.subtitle}</p></div><div className="page-date"><span className="status-dot" /><span>数据快照 · 11 月 14 日</span></div></div>{path.startsWith('/ips') && <div className="source-switch" aria-label="统计来源"><Button variant="ghost" aria-pressed={filter.source === 'site'} onClick={() => onChange({ source: 'site', page: 1 })}><Globe2 />源站日志</Button><Button variant="ghost" aria-pressed={filter.source === 'singbox'} onClick={() => onChange({ source: 'singbox', site: '', status: '', page: 1 })}><Network />代理载荷</Button></div>}{basePath !== '/system' && <FilterBar key={basePath} path={path} filter={filter} options={options.data} onChange={onChange} />}{invalid ? <div className="data-notice" role="alert">{invalid}<Button variant="outline" onClick={() => { window.location.hash = path }}>重置条件</Button></div> : basePath === '/sites' || basePath === '/ips' ? <AnalysisPage path={path} filter={filter} onChange={onChange} /> : basePath === '/hosts' ? <HostsPage path={path} filter={filter} /> : basePath === '/billing' ? <BillingPage filter={filter} /> : basePath === '/system' ? <SystemPage /> : <OverviewPage filter={filter} onChange={onChange} />}<footer className="page-footer"><span>Meterly · 让流量清晰可见</span><span><Clock3 className="size-3" />Asia/Singapore<span className="footer-dot">·</span>本地演示数据</span></footer></main></SidebarInset></SidebarProvider>
}
