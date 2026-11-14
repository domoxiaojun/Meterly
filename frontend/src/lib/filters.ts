import { z } from 'zod'

const filterSchema = z.object({
  period: z.enum(['today', 'yesterday', 'last7d', 'last30d', 'day', 'custom']).catch('last7d'),
  host: z.string().catch('all'), source: z.enum(['site', 'singbox']).catch('site'),
  start: z.string().catch(''), end: z.string().catch(''), search: z.string().catch(''),
  ip: z.string().catch(''), country: z.string().catch(''), asn: z.string().catch(''), status: z.string().catch(''),
  site: z.string().catch(''), page: z.coerce.number().int().min(1).catch(1),
  sort: z.enum(['total', 'rx', 'tx', 'requests', 'error_rate', 'key']).catch('total'),
  order: z.enum(['asc', 'desc']).catch('desc'), gran: z.enum(['auto', 'hour', 'day', 'minute']).catch('auto'),
})
export type Filters = z.infer<typeof filterSchema>
export const parseFilters = (value: unknown) => filterSchema.parse(value)
export const defaults = parseFilters({})
export function href(path: string, state: Filters, patch: Partial<Filters> = {}) {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries({ ...state, ...patch })) if (value !== '') params.set(key, String(value))
  return `#${path}?${params}`
}
export function validateFilters(s: Filters): string {
  if (s.ip) {
    const [ip, prefix, extra] = s.ip.split('/')
    const v4 = z.ipv4().safeParse(ip).success, v6 = z.ipv6().safeParse(ip).success
    if ((!v4 && !v6) || extra !== undefined || (prefix !== undefined && (!/^\d+$/.test(prefix) || Number(prefix) > (v4 ? 32 : 128)))) return '请输入有效的 IP 地址或 CIDR 网段'
  }
  if (s.country && !/^[a-zA-Z]{2}$/.test(s.country)) return '国家代码须为两位字母，例如 SG'
  if (s.asn && !/^(AS)?\d+$/i.test(s.asn)) return 'ASN 应为数字或 AS 开头的编号'
  if (s.status && (!/^\d{3}$/.test(s.status) || +s.status < 100 || +s.status > 599)) return '状态码应在 100–599 之间'
  if (s.period === 'custom' || s.period === 'day') {
    const valid = (date: string) => /^\d{4}-\d{2}-\d{2}$/.test(date) && new Date(date + 'T00:00:00Z').toISOString().slice(0, 10) === date
    try { if (!valid(s.start) || (s.period === 'custom' && (!valid(s.end) || s.end < s.start))) return '请选择有效的日期范围，结束日期不能早于开始日期' } catch { return '请选择有效的日期' }
  }
  return ''
}
export const periodLabels: Record<Filters['period'], string> = { today: '今天', yesterday: '昨天', last7d: '最近 7 天', last30d: '最近 30 天', day: '指定日期', custom: '自定义日期' }
