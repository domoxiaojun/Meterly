import { z } from 'zod'

export const metricsSchema = z.object({ rx: z.number(), tx: z.number(), total: z.number(), requests: z.number(), ips: z.number(), sites: z.number(), hosts: z.number(), errors: z.number(), error_rate: z.number(), rx_known: z.boolean(), requests_known: z.boolean(), overflow: z.boolean() })
export const metaSchema = z.object({ from: z.number(), to: z.number(), tz: z.string(), gran: z.string(), resolution: z.string(), state: z.string(), complete: z.boolean(), reasons: z.array(z.string()), ips_available: z.boolean().optional(), live: z.boolean().optional() })
export const rowSchema = metricsSchema.extend({ key: z.string(), country: z.string(), country_name: z.string(), region: z.string().optional(), city: z.string().optional(), scope: z.string().optional(), asn: z.number(), org: z.string(), special: z.boolean() })
export const rankSchema = z.object({ rows: z.array(rowSchema), totals: metricsSchema.nullable(), meta: metaSchema, count: z.number(), page: z.number(), page_size: z.number() })
export const seriesSchema = z.object({ meta: metaSchema, points: z.array(metricsSchema.extend({ ts: z.number() })) })
export const alertSchema = z.object({ host: z.string(), kind: z.string(), message: z.string() })
export const overviewSchema = z.object({ site: metricsSchema, previous: metricsSchema.nullable(), meta: metaSchema, growth_pct: z.number().nullable(), host_count: z.number(), online: z.number(), top_sites: z.array(rowSchema), top_ips: z.array(rowSchema), alerts: z.array(alertSchema), network: z.object({ rx: z.number(), tx: z.number() }) })
const hostSchema = z.object({ name: z.string(), agent_version: z.string(), billing_iface: z.string(), quota_bytes: z.number(), last_seen: z.number(), tz: z.string() })
export const optionsSchema = z.object({ hosts: z.array(hostSchema), sites: z.array(z.object({ site: z.string(), label: z.string() })), tz: z.string() })
export const hostsSchema = z.object({ hosts: z.array(z.object({ host: hostSchema, online: z.boolean(), alerts: z.array(alertSchema), desired: z.object({ version: z.number() }), meta: z.object({ buffered: z.number(), dropped: z.number(), config_version: z.number(), collectors: z.record(z.string(), z.object({ enabled: z.boolean(), fails: z.number(), last_ok: z.number() })), inventory: z.object({ os: z.string(), arch: z.string(), interfaces: z.array(z.string()), sources: z.array(z.object({ site: z.string(), path: z.string(), available: z.boolean(), disabled: z.boolean(), ip_mode: z.string() })) }).optional() }) })) })
export const billingSchema = z.object({ hosts: z.array(z.object({ name: z.string(), online: z.boolean(), billed: z.number(), quota: z.number(), quota_pct: z.number(), forecast: z.number(), billing_iface: z.string(), billing_mode: z.string(), period: z.object({ start: z.string(), end: z.string(), tz: z.string() }) })), totals: z.object({ billed: z.number(), iface: z.object({ rx: z.number(), tx: z.number() }) }) })
export const systemSchema = z.object({ storage: z.object({ db_bytes: z.number(), backup_bytes: z.number(), free_bytes: z.number(), total_bytes: z.number() }), retention: z.object({ MinuteDetailHours: z.number(), HourDays: z.number() }), backup_time: z.string(), backup_keep: z.number(), estimated_days_left: z.number().nullable(), tz: z.string(), alerts: z.array(z.string()), maintenance: z.array(z.object({ kind: z.string(), status: z.string(), message: z.string(), started_at: z.number() })) })
export const previewSchema = z.object({ demo: z.literal(true), label: z.string(), as_of: z.number(), tz: z.string() })
export type Metrics = z.infer<typeof metricsSchema>
export type Meta = z.infer<typeof metaSchema>
export type Rank = z.infer<typeof rankSchema>
export type Row = z.infer<typeof rowSchema>
export type Overview = z.infer<typeof overviewSchema>
export type Options = z.infer<typeof optionsSchema>
export type HostEntry = z.infer<typeof hostsSchema>['hosts'][number]
export type Series = z.infer<typeof seriesSchema>
