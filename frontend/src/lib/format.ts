export function bytes(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '—'
  if (value >= 2 ** 62 - 1 || value < 0) return '超出统计范围'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let scaled = value, index = 0
  while (scaled >= 1024 && index < units.length - 1) { scaled /= 1024; index++ }
  return `${scaled.toLocaleString('en-US', { maximumFractionDigits: index ? 2 : 0 })} ${units[index]}`
}
export const number = (n: number | undefined) => n == null || n >= 2 ** 62 - 1 ? '—' : n.toLocaleString('en-US')
export function compact(n: number) { return n >= 10_000 ? `${(n / 10_000).toLocaleString('en-US', { maximumFractionDigits: 2 })} 万` : number(n) }
export function dateLabel(ts: number, time = false, tz = 'Asia/Singapore') { return new Intl.DateTimeFormat('zh-CN', { timeZone: tz, month: '2-digit', day: '2-digit', ...(time ? { hour: '2-digit', minute: '2-digit', hour12: false } : {}) }).format(ts * 1000) }

export function calendarDate(ts: number, tz: string) {
  const parts = new Intl.DateTimeFormat('en-US', { timeZone: tz, year: 'numeric', month: 'numeric', day: 'numeric' }).formatToParts(ts * 1000)
  const value = (key: string) => Number(parts.find(p => p.type === key)?.value)
  return new Date(value('year'), value('month') - 1, value('day'), 12)
}
