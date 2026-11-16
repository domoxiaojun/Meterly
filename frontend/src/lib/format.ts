export function bytes(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '—'
  if (value >= 2 ** 62 - 1 || value < 0) return '超出统计范围'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let scaled = value, index = 0
  while (scaled >= 1024 && index < units.length - 1) { scaled /= 1024; index++ }
  return `${scaled.toLocaleString('en-US', { maximumFractionDigits: index ? 2 : 0 })} ${units[index]}`
}
export const number = (n: number | undefined) => n == null || n >= 2 ** 62 - 1 ? '—' : n.toLocaleString('en-US')
export function locationText(row: { country_name?: string; region?: string; city?: string; scope?: string }) {
  if (row.scope) return `${row.scope}地址`
  return [...new Set([row.country_name, row.region, row.city].filter(Boolean))].join(' · ') || '暂无归属地'
}
export function compact(n: number) { return n >= 10_000 ? `${(n / 10_000).toLocaleString('en-US', { maximumFractionDigits: 2 })} 万` : number(n) }
export function dateLabel(ts: number, time = false, tz = 'Asia/Shanghai') { return new Intl.DateTimeFormat('zh-CN', { timeZone: tz, month: '2-digit', day: '2-digit', ...(time ? { hour: '2-digit', minute: '2-digit', hour12: false } : {}) }).format(ts * 1000) }
const maxDateTime = 8_640_000_000_000_000
export function displayTimeZone(tz?: string | null) {
  const zone = typeof tz === 'string' && tz ? tz : 'Asia/Shanghai'
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: zone })
    return zone
  } catch {
    return 'Asia/Shanghai'
  }
}
export function dateTimeLabel(ms?: number | null, tz = 'Asia/Shanghai') {
  if (typeof ms !== 'number' || !Number.isFinite(ms) || ms === 0 || ms < -maxDateTime || ms > maxDateTime) return '未知'
  const zone = displayTimeZone(tz)
  try {
    const parts = new Intl.DateTimeFormat('en-US', { timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }).formatToParts(ms)
    const pick = (type: Intl.DateTimeFormatPartTypes) => parts.find(part => part.type === type)?.value ?? ''
    return `${pick('year')}-${pick('month')}-${pick('day')} ${pick('hour') === '24' ? '00' : pick('hour')}:${pick('minute')}:${pick('second')}`
  } catch {
    return '未知'
  }
}

export function calendarDate(ts: number, tz: string) {
  const parts = new Intl.DateTimeFormat('en-US', { timeZone: tz, year: 'numeric', month: 'numeric', day: 'numeric' }).formatToParts(ts * 1000)
  const value = (key: string) => Number(parts.find(p => p.type === key)?.value)
  return new Date(value('year'), value('month') - 1, value('day'), 12)
}
