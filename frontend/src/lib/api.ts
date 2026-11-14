import { z } from './validation'
import { useQuery } from '@tanstack/react-query'

export class APIError extends Error {
  constructor(message: string, public status: number) { super(message) }
}
export function sessionExpired() { window.dispatchEvent(new Event('meterly:unauthorized')) }
export async function request<T>(path: string, schema: z.ZodType<T>, signal?: AbortSignal, init?: RequestInit): Promise<T> {
  const response = await fetch(path, { ...init, signal, credentials: 'same-origin' })
  if (response.status === 401 && path !== '/api/login') sessionExpired()
  const data: unknown = await response.json().catch(() => { throw new APIError('服务返回了无效响应，请稍后重试', response.status) })
  if (!response.ok) {
    const error = z.object({ error: z.string() }).safeParse(data)
    throw new APIError(response.status === 401 ? '会话已过期，请重新登录' : error.success ? error.data.error : `请求失败 (${response.status})`, response.status)
  }
  const parsed = schema.safeParse(data)
  if (!parsed.success) throw new APIError('响应数据不完整，暂时无法显示', response.status)
  return parsed.data
}
export function useAPI<T>(path: string, schema: z.ZodType<T>) {
  const period = new URL(path, 'http://meterly.invalid').searchParams.get('period')
  const historical = ['day', 'custom', 'yesterday', 'previous'].includes(period || '')
  return useQuery({ queryKey: ['meterly', path], queryFn: ({ signal }) => request(path, schema, signal), staleTime: 30_000, refetchInterval: historical ? false : 30_000, retry: false, refetchOnWindowFocus: false })
}
export function query(values: Record<string, string | number | undefined>) {
  return new URLSearchParams(Object.entries(values).filter(([, value]) => value !== undefined && value !== '').map(([key, value]) => [key, String(value)])).toString()
}
