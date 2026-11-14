import { createContext, useContext, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Activity, LoaderCircle, LogOut } from 'lucide-react'
import { z } from './validation'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader } from '@/components/ui/card'
import { APIError, request } from './api'

const sessionSchema = z.object({ ok: z.literal(true), version: z.string(), tz: z.string(), auth: z.boolean(), now: z.number() })
const okSchema = z.object({ ok: z.literal(true) })
type Session = z.infer<typeof sessionSchema>
const demo = import.meta.env.MODE === 'demo'
const SessionContext = createContext<(Session & { demo: boolean; logout: () => Promise<void> }) | null>(null)

export function useSession() {
  const session = useContext(SessionContext)
  if (!session) throw new Error('Session is not ready')
  return session
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const client = useQueryClient()
  const [session, setSession] = useState<Session | null>()
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [elapsed, setElapsed] = useState(0)
  const receivedAt = useRef(Date.now())

  function accept(value: Session) { receivedAt.current = Date.now(); setElapsed(0); setSession(value); setError('') }
  function clear() { void client.cancelQueries(); client.clear(); setSession(null); setError('') }
  useEffect(() => {
    const controller = new AbortController()
    request('/api/v2/me', sessionSchema, controller.signal).then(accept).catch(e => {
      if (controller.signal.aborted) return
      if (e instanceof APIError && e.status === 401) setSession(null)
      else setError(e instanceof Error ? e.message : '无法连接中心，请重试')
    })
    return () => controller.abort()
  }, [attempt])
  useEffect(() => {
    window.addEventListener('meterly:unauthorized', clear)
    return () => window.removeEventListener('meterly:unauthorized', clear)
  }, [client])
  useEffect(() => {
    const timer = setInterval(() => setElapsed(Math.floor((Date.now() - receivedAt.current) / 1000)), 30_000)
    return () => clearInterval(timer)
  }, [])

  async function login(token: string) {
    await request('/api/login', okSchema, undefined, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token }) })
    const value = await request('/api/v2/me', sessionSchema)
    await client.cancelQueries(); client.clear(); accept(value)
  }
  async function logout() {
    await request('/api/logout', okSchema, undefined, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' })
    clear()
  }
  if (session === null) return <Login onLogin={login} />
  if (!session) return <div className="boot-state"><span className="brand-mark"><Activity /></span><h1>Meterly</h1>{error ? <><p role="alert">{error}</p><Button onClick={() => { setError(''); setAttempt(n => n + 1) }}>重新连接</Button></> : <p role="status">正在连接流量工作台…</p>}</div>
  return <SessionContext value={{ ...session, now: session.now + (demo ? 0 : elapsed), demo, logout }}>{children}</SessionContext>
}

function Login({ onLogin }: { onLogin: (token: string) => Promise<void> }) {
  const [token, setToken] = useState(''), [pending, setPending] = useState(false), [error, setError] = useState('')
  const input = useRef<HTMLInputElement>(null)
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (pending || !token) return
    setPending(true); setError('')
    try { await onLogin(token); setToken('') }
    catch (e) { setError(e instanceof APIError && e.status === 401 ? '面板口令不正确，请重新输入' : e instanceof Error ? e.message : '登录失败，请重试'); input.current?.focus() }
    finally { setPending(false) }
  }
  return <main className="boot-state"><Card className="w-full max-w-sm text-left"><CardHeader><span className="brand-mark mb-5"><Activity /></span><h1 className="text-2xl font-semibold">登录 Meterly</h1><CardDescription>查看流量，管理采集主机。</CardDescription></CardHeader><CardContent><form className="space-y-4" onSubmit={submit}><label className="grid gap-2 text-sm font-medium" htmlFor="panel-token">面板口令<Input ref={input} id="panel-token" name="token" type="password" autoComplete="current-password" autoFocus required value={token} onChange={e => setToken(e.target.value)} aria-invalid={!!error} aria-describedby={error ? 'login-error' : undefined} className="h-11" /></label>{error && <p id="login-error" role="alert" className="text-sm text-destructive">{error}</p>}<Button type="submit" className="h-11 w-full" disabled={pending || !token}>{pending && <LoaderCircle className="animate-spin" />}{pending ? '正在登录…' : '登录'}</Button></form></CardContent></Card></main>
}

export function LogoutButton() {
  const { auth, logout } = useSession()
  const [pending, setPending] = useState(false), [error, setError] = useState('')
  if (!auth) return null
  return <div><Button variant="ghost" size="icon" aria-label="退出登录" disabled={pending} onClick={async () => { setPending(true); setError(''); try { await logout() } catch { setError('退出失败，请重试') } finally { setPending(false) } }}><LogOut className="size-4" /></Button>{error && <span role="alert" className="text-xs text-destructive">{error}</span>}</div>
}
