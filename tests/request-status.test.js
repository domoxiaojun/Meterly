// Node vm simulation of the React element tree, plus QueryObserver against the
// real useAPI query function. This does not open a browser, render DOM, or
// bypass localhost browser restrictions.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import { createRequire } from 'node:module'

const req = createRequire(import.meta.url)
const { transformSync } = await import(req.resolve('rolldown/experimental'))
const { QueryClient, QueryObserver } = req('@tanstack/react-query')

function load(relative, assign, proxy = false) {
  let source = fs.readFileSync(new URL(relative, import.meta.url), 'utf8').replace(/^import .*$/gm, '').replace(/export /g, '')
  if (assign) source += `\nObject.assign(globalThis, { ${assign} })`
  const out = transformSync(relative, source, { jsx: { runtime: 'classic' } })
  assert.deepEqual(out.errors, [])
  const base = {
    React: { createElement, Fragment: Symbol.for('react.fragment') },
    console, URL, URLSearchParams, Intl, Date, Math, Object, Array, String, Number, Error, JSON,
    decodeURIComponent, encodeURIComponent,
  }
  const ctx = proxy ? new Proxy(base, {
    has: () => true,
    get(target, prop, receiver) {
      if (prop in target) return Reflect.get(target, prop, receiver)
      if (typeof prop !== 'string') return undefined
      const fn = function stub() { return null }
      Object.defineProperty(fn, 'name', { value: prop })
      target[prop] = fn
      return fn
    },
    set(target, prop, value) { target[prop] = value; return true },
  }) : base
  vm.createContext(ctx)
  vm.runInContext(out.code, ctx)
  return base
}

function createElement(type, props, ...children) {
  return { type, props: props ?? {}, children }
}
function walk(node, acc = []) {
  if (Array.isArray(node)) { for (const item of node) walk(item, acc); return acc }
  if (node == null || typeof node !== 'object') return acc
  acc.push(node)
  walk(node.children, acc)
  return acc
}
function texts(node, acc = []) {
  if (Array.isArray(node)) { for (const item of node) texts(item, acc); return acc }
  if (typeof node === 'string' || typeof node === 'number') { acc.push(String(node)); return acc }
  if (node == null || typeof node !== 'object') return acc
  texts(node.children, acc)
  return acc
}
function component(name) {
  const fn = function stub() { return null }
  Object.defineProperty(fn, 'name', { value: name })
  return fn
}

const format = load('../frontend/src/lib/format.ts', 'bytes, number, locationText, compact, dateLabel, dateTimeLabel, displayTimeZone')
const api = load('../frontend/src/lib/api.ts', 'query')
const ui = {}
for (const name of ['Alert', 'AlertAction', 'AlertDescription', 'AlertTitle', 'Button', 'Card', 'CardContent', 'CardHeader', 'CardTitle', 'CardDescription', 'Badge']) ui[name] = component(name)
const pages = load('../frontend/src/components/meterly/analytics.tsx', '', true)
Object.assign(pages, ui, { query: api.query, dateTimeLabel: format.dateTimeLabel, displayTimeZone: format.displayTimeZone, number: format.number, locationText: format.locationText, href: (path) => `#${path}` })

const updatedAt = Date.parse('2026-11-15T15:59:59Z')
let session = { tz: 'Asia/Shanghai' }
let responses = []
const refetches = []
const mockUseAPI = (path, schema, options) => {
  mockUseAPI.calls.push({ path, schema, options })
  const matched = responses.find(item => item.match(path))
  const result = { data: undefined, error: null, isPending: false, isFetching: false, dataUpdatedAt: 0, ...(matched ? matched.result : {}) }
  return { ...result, refetch: () => { refetches.push(path) } }
}
mockUseAPI.calls = []
pages.useSession = () => session
pages.useAPI = mockUseAPI

const filter = { period: 'today', host: 'edge-a', source: 'singbox', start: '', end: '', search: '', ip: '', country: '', asn: '', status: '204', site: '', page: 3, sort: 'total', order: 'desc', gran: 'auto' }
const overviewMeta = { tz: 'Asia/Shanghai', state: 'available', complete: true, reasons: [] }
const seriesData = { meta: { from: 1, to: 2, tz: 'Asia/Shanghai', gran: 'hour' }, points: [{ ts: 1, tx: 5 }] }
const rankData = { rows: [{ key: '203.0.113.8', org: 'Cached Org', asn: 64500 }], totals: { total: 9 }, meta: overviewMeta, count: 1, page: 1, page_size: 10 }
const statusData = { rows: [{ key: '200', requests: 12 }, { key: '404', requests: 2 }], totals: null, meta: overviewMeta, count: 2, page: 1, page_size: 10 }
const emptyStatus = { ...statusData, rows: [], count: 0 }
const inboundData = { ...statusData, rows: [{ key: 'inbound-a', requests: 4 }], count: 1 }
function queryResult(data, error, extra = {}) {
  return { data, error, isPending: !data && !error, isFetching: false, dataUpdatedAt: data ? updatedAt : 0, ...extra }
}
function render(path, extra = {}) {
  refetches.length = 0
  pages.useAPI = mockUseAPI
  mockUseAPI.calls = []
  const changes = []
  const tree = pages.AnalysisPage({ filter: extra.filter ?? filter, path, onChange(patch) { changes.push(patch) } })
  return { tree, changes }
}
function statusCards(tree) {
  return walk(tree).filter(node => node.type === pages.Card && walk(node).some(child => child.type === pages.CardTitle && texts(child).join('') === '请求状态'))
}
function statusCard(tree) {
  const cards = statusCards(tree)
  assert.equal(cards.length, 1)
  return cards[0]
}
function warnings(tree) { return walk(tree).filter(node => node.type === pages.RefreshFailure) }
function warningText(node) { return texts(pages.RefreshFailure(node.props)).join('') }
function warningButton(node) { return walk(pages.RefreshFailure(node.props)).find(item => item.type === pages.Button) }
function buttons(node) { return walk(node).filter(item => item.type === pages.Button) }
function params(path) { return new URL(path, 'http://meterly.invalid').searchParams }
function statusCall() { return mockUseAPI.calls[2] }

test('rank first load still owns the page, including when status data is already available', () => {
  session = { tz: 'Asia/Shanghai' }
  responses = [
    { match: path => path.includes('/analytics/rank?') && path.includes('group=ip'), result: queryResult(undefined, new Error('排行暂不可用'), { isPending: false }) },
    { match: path => path.includes('/analytics/series?'), result: queryResult(seriesData, null) },
    { match: path => path.includes('group=status'), result: queryResult(statusData, null) },
  ]
  const { tree } = render('/sites/cached.example')
  assert.equal(tree.type, pages.QueryState)
  assert.equal(tree.props.error.message, '排行暂不可用')
  assert.equal(tree.props.loading, false)
  assert.equal(statusCards(tree).length, 0)
  assert.equal(texts(tree).includes('请求状态'), false)
  assert.equal(warnings(tree).length, 0)
  tree.props.onRetry()
  assert.deepEqual(refetches, [mockUseAPI.calls[0].path])
  responses[0].result = { data: undefined, error: null, isPending: true, isFetching: true, dataUpdatedAt: 0 }
  const pending = render('/sites/cached.example')
  assert.equal(pending.tree.type, pages.QueryState)
  assert.equal(pending.tree.props.loading, true)
  assert.equal(statusCards(pending.tree).length, 0)
})

test('site status region shows loading, then a local error that retries only that query', () => {
  session = { tz: 'Asia/Shanghai' }
  responses = [
    { match: path => path.includes('/analytics/rank?') && path.includes('group=ip'), result: queryResult(rankData, null) },
    { match: path => path.includes('/analytics/series?'), result: queryResult(seriesData, null) },
    { match: path => path.includes('group=status'), result: { data: undefined, error: null, isPending: true, isFetching: true, dataUpdatedAt: 0 } },
  ]
  const pending = render('/sites/cached.example')
  const calls = mockUseAPI.calls
  assert.equal(calls.length, 3)
  assert.equal(calls[0].options, undefined)
  assert.equal(calls[1].options.enabled, true)
  assert.equal(calls[2].options.enabled, true)
  assert.equal(params(calls[0].path).get('group'), 'ip')
  assert.equal(params(calls[0].path).get('status'), '204')
  assert.equal(params(calls[0].path).get('site'), 'cached.example')
  assert.equal(params(calls[0].path).get('source'), 'site')
  assert.equal(params(calls[1].path).get('group'), 'ip')
  const statusParams = params(calls[2].path)
  assert.equal(statusParams.get('group'), 'status')
  assert.equal(statusParams.get('site'), 'cached.example')
  assert.equal(statusParams.get('source'), 'site')
  assert.equal(statusParams.get('page'), '1')
  assert.equal(statusParams.get('page_size'), '10')
  assert.equal(statusParams.get('host'), 'edge-a')
  assert.equal(statusParams.get('period'), 'today')
  assert.equal(statusParams.get('sort'), 'total')
  assert.equal(statusParams.get('order'), 'desc')
  assert.equal(statusParams.get('gran'), 'auto')
  assert.equal(statusParams.get('status'), null)
  assert.equal(statusParams.get('search'), null)
  const card = statusCard(pending.tree)
  const loading = walk(card).find(node => node.props && node.props.role === 'status')
  assert.equal(String(loading.props['aria-busy']), 'true')
  assert.equal(loading.props['aria-live'], 'polite')
  assert.equal(texts(loading).join(''), '正在加载请求状态')
  assert.equal(buttons(card).length, 0)
  assert.equal(warnings(card).length, 0)
  assert.doesNotMatch(texts(card).join(''), /暂时无法加载|刷新失败/)
  assert.equal(walk(pending.tree).find(node => node.type === pages.TrafficTable).props.data, rankData)
  assert.equal(walk(pending.tree).find(node => node.type === pages.TrafficChart).props.series, seriesData)
  assert.equal(walk(pending.tree).find(node => node.type === pages.MetricGrid).props.totals, rankData.totals)

  responses[2].result = queryResult(undefined, new Error('状态暂不可用'), { isPending: false, isFetching: false })
  const failed = render('/sites/cached.example')
  const failedCard = statusCard(failed.tree)
  const alert = walk(failedCard).find(node => node.props && node.props.role === 'alert')
  assert.equal(texts(alert).join(''), '暂时无法加载状态暂不可用重新加载')
  assert.doesNotMatch(texts(failedCard).join(''), /刷新失败|上次成功|正在加载请求状态/)
  assert.equal(warnings(failed.tree).length, 0)
  assert.equal(walk(failed.tree).some(node => node.type === pages.QueryState), false)
  assert.equal(walk(failed.tree).find(node => node.type === pages.TrafficTable).props.data, rankData)
  assert.equal(walk(failed.tree).find(node => node.type === pages.TrafficChart).props.series, seriesData)
  assert.equal(walk(failedCard).some(node => node.props && node.props.dangerouslySetInnerHTML !== undefined), false)
  const retry = buttons(failedCard)[0]
  assert.equal(texts(retry).join(''), '重新加载')
  assert.equal(retry.props.disabled, false)
  const statusPath = statusCall().path
  retry.props.onClick()
  assert.deepEqual(refetches, [statusPath])
  assert.notEqual(statusPath, calls[0].path)
  assert.equal(statusPath.includes('/analytics/series?'), false)

  responses[2].result = queryResult(undefined, new Error('状态暂不可用'), { isPending: false, isFetching: true })
  const held = render('/sites/cached.example')
  const heldButton = buttons(statusCard(held.tree))[0]
  assert.equal(heldButton.props.disabled, true)
  assert.match(texts(statusCard(held.tree)).join(''), /状态暂不可用/)
  assert.equal(warnings(held.tree).length, 0)
  heldButton.props.onClick()
  assert.deepEqual(refetches, [])

  responses[1].result = queryResult(undefined, new Error('曲线暂不可用'), { isPending: false })
  responses[2].result = queryResult(undefined, new Error('状态暂不可用'), { isPending: false })
  const both = render('/sites/cached.example')
  const seriesState = walk(both.tree).filter(node => node.type === pages.QueryState)
  assert.equal(seriesState.length, 1)
  assert.equal(seriesState[0].props.error.message, '曲线暂不可用')
  seriesState[0].props.onRetry()
  assert.deepEqual(refetches, [mockUseAPI.calls[1].path])
  buttons(statusCard(both.tree))[0].props.onClick()
  assert.deepEqual(refetches, [mockUseAPI.calls[1].path, mockUseAPI.calls[2].path])
  assert.equal(walk(both.tree).find(node => node.type === pages.TrafficTable).props.data, rankData)

  responses[1].result = queryResult(seriesData, null)
  responses[2].result = queryResult(statusData, null)
  const recovered = render('/sites/cached.example')
  const recoveredCard = statusCard(recovered.tree)
  assert.equal(walk(recoveredCard).some(node => node.props && node.props.role === 'alert'), false)
  assert.equal(warnings(recovered.tree).length, 0)
  assert.deepEqual(buttons(recoveredCard).map(node => texts(node).join('')), ['20012 次', '4042 次'])
  assert.equal(walk(recovered.tree).find(node => node.type === pages.TrafficChart).props.series, seriesData)
})

test('status counts toggle the selected status and reset to page 1 without refetching', () => {
  session = { tz: 'Asia/Shanghai' }
  responses = [
    { match: path => path.includes('/analytics/rank?') && path.includes('group=ip'), result: queryResult(rankData, null) },
    { match: path => path.includes('/analytics/series?'), result: queryResult(seriesData, null) },
    { match: path => path.includes('group=status'), result: queryResult(statusData, null) },
  ]
  const selected = render('/sites/cached.example', { filter: { ...filter, status: '200' } })
  assert.equal(params(statusCall().path).get('status'), null)
  assert.equal(params(mockUseAPI.calls[0].path).get('status'), '200')
  const card = statusCard(selected.tree)
  const [ok, missing] = buttons(card)
  assert.equal(ok.props.variant, 'default')
  assert.equal(missing.props.variant, 'outline')
  ok.props.onClick()
  missing.props.onClick()
  assert.equal(selected.changes.length, 2)
  assert.equal(selected.changes[0].status, '')
  assert.equal(selected.changes[0].page, 1)
  assert.equal(selected.changes[1].status, '404')
  assert.equal(selected.changes[1].page, 1)
  assert.deepEqual(refetches, [])
  assert.equal(warnings(card).length, 0)
})

test('empty status data is explicit, and a later refresh failure keeps that empty view', () => {
  session = { tz: 'America/New_York' }
  responses = [
    { match: path => path.includes('/analytics/rank?') && path.includes('group=ip'), result: queryResult(rankData, null) },
    { match: path => path.includes('/analytics/series?'), result: queryResult(seriesData, null) },
    { match: path => path.includes('group=status'), result: queryResult(emptyStatus, null) },
  ]
  const empty = render('/sites/cached.example')
  const card = statusCard(empty.tree)
  const state = walk(card).find(node => node.type === pages.EmptyState)
  assert.equal(state.props.title, '暂无请求状态')
  assert.match(texts(card).join(''), /当前范围没有状态码数据/)
  assert.equal(buttons(card).length, 0)
  assert.equal(warnings(card).length, 0)
  assert.equal(walk(empty.tree).find(node => node.type === pages.TrafficTable).props.data, rankData)

  responses[2].result = queryResult(emptyStatus, new Error('空状态刷新失败'), { isFetching: true })
  const failed = render('/sites/cached.example')
  const failedCard = statusCard(failed.tree)
  assert.equal(warnings(failedCard).length, 1)
  assert.equal(warningText(warnings(failedCard)[0]), '刷新失败空状态刷新失败上次成功：2026-11-15 10:59:59（America/New_York）重试')
  assert.equal(walk(failedCard).find(node => node.type === pages.EmptyState).props.title, '暂无请求状态')
  assert.doesNotMatch(texts(failedCard).join(''), /暂时无法加载|正在加载请求状态/)
  assert.equal(warningButton(warnings(failedCard)[0]).props.disabled, true)
  warningButton(warnings(failedCard)[0]).props.onClick()
  assert.deepEqual(refetches, [])
  responses[2].result = queryResult(emptyStatus, new Error('空状态刷新失败'))
  const retryable = render('/sites/cached.example')
  warningButton(warnings(statusCard(retryable.tree))[0]).props.onClick()
  assert.deepEqual(refetches, [statusCall().path])
  assert.equal(walk(retryable.tree).find(node => node.type === pages.TrafficChart).props.series, seriesData)
  responses[2].result = queryResult(statusData, null)
  const recovered = render('/sites/cached.example')
  assert.equal(warnings(recovered.tree).length, 0)
  assert.deepEqual(buttons(statusCard(recovered.tree)).map(node => texts(node)[0]), ['200', '404'])
})

test('cached status rows keep their counts and retry only the status query after a refresh failure', () => {
  session = { tz: 'Asia/Shanghai' }
  responses = [
    { match: path => path.includes('/analytics/rank?') && path.includes('group=ip'), result: queryResult(rankData, new Error('排行刷新失败')) },
    { match: path => path.includes('/analytics/series?'), result: queryResult(seriesData, new Error('曲线刷新失败')) },
    { match: path => path.includes('group=status'), result: queryResult(statusData, new Error('状态刷新失败')) },
  ]
  const { tree } = render('/sites/cached.example')
  const card = statusCard(tree)
  assert.deepEqual(warnings(tree).map(node => node.props.error.message), ['排行刷新失败', '曲线刷新失败', '状态刷新失败'])
  assert.match(warningText(warnings(card)[0]), /状态刷新失败/)
  assert.match(warningText(warnings(card)[0]), /上次成功：2026-11-15 23:59:59（Asia\/Shanghai）/)
  assert.doesNotMatch(warningText(warnings(card)[0]), /暂时无法加载/)
  assert.deepEqual(buttons(card).map(node => texts(node).join('')), ['20012 次', '4042 次'])
  assert.equal(walk(card).some(node => node.type === pages.EmptyState), false)
  assert.equal(walk(tree).find(node => node.type === pages.TrafficTable).props.data, rankData)
  assert.equal(walk(tree).find(node => node.type === pages.TrafficChart).props.series, seriesData)
  warningButton(warnings(card)[0]).props.onClick()
  assert.deepEqual(refetches, [statusCall().path])
  responses[2].result = queryResult(statusData, null)
  const recovered = render('/sites/cached.example')
  assert.equal(warnings(statusCard(recovered.tree)).length, 0)
  assert.deepEqual(warnings(recovered.tree).map(node => node.props.error.message), ['排行刷新失败', '曲线刷新失败'])
  assert.deepEqual(buttons(statusCard(recovered.tree)).map(node => texts(node)[0]), ['200', '404'])
})

test('site list, IP list, and IP detail do not render the site request-status card', () => {
  session = { tz: 'UTC' }
  responses = [
    { match: path => path.includes('/analytics/rank?') && path.includes('group=site'), result: queryResult(rankData, new Error('列表刷新失败')) },
    { match: path => path.includes('/analytics/series?'), result: queryResult(seriesData, new Error('不应显示')) },
    { match: path => path.includes('group=status') || path.includes('group=inbound'), result: queryResult(statusData, new Error('不应显示')) },
  ]
  const sites = render('/sites')
  assert.equal(mockUseAPI.calls[1].options.enabled, false)
  assert.equal(mockUseAPI.calls[2].options.enabled, false)
  assert.equal(params(mockUseAPI.calls[2].path).get('group'), 'status')
  assert.equal(statusCards(sites.tree).length, 0)
  assert.equal(texts(sites.tree).includes('请求状态'), false)
  assert.equal(warnings(sites.tree).map(node => node.props.error.message).join('|'), '列表刷新失败')
  assert.equal(walk(sites.tree).find(node => node.type === pages.TrafficTable).props.data, rankData)

  responses[0] = { match: path => path.includes('/analytics/rank?') && path.includes('group=ip'), result: queryResult(rankData, null) }
  const ips = render('/ips')
  assert.equal(mockUseAPI.calls[1].options.enabled, false)
  assert.equal(mockUseAPI.calls[2].options.enabled, false)
  assert.equal(params(mockUseAPI.calls[2].path).get('group'), 'inbound')
  assert.equal(statusCards(ips.tree).length, 0)

  responses = [
    { match: path => path.includes('/analytics/rank?') && path.includes('group=ip'), result: queryResult(rankData, new Error('IP 排行刷新失败')) },
    { match: path => path.includes('/analytics/series?'), result: queryResult(seriesData, null) },
    { match: path => path.includes('group=inbound'), result: queryResult(inboundData, new Error('入站刷新失败')) },
  ]
  const ip = render('/ips/203.0.113.8')
  assert.equal(mockUseAPI.calls[1].options.enabled, true)
  assert.equal(mockUseAPI.calls[2].options.enabled, true)
  assert.equal(params(mockUseAPI.calls[0].path).get('ip'), '203.0.113.8')
  assert.equal(params(mockUseAPI.calls[0].path).get('page'), '1')
  assert.equal(params(mockUseAPI.calls[2].path).get('group'), 'inbound')
  assert.equal(params(mockUseAPI.calls[2].path).get('page'), '3')
  assert.equal(params(mockUseAPI.calls[2].path).get('status'), '204')
  assert.equal(statusCards(ip.tree).length, 0)
  assert.equal(texts(ip.tree).includes('请求状态'), false)
  const table = walk(ip.tree).find(node => node.type === pages.TrafficTable)
  assert.equal(table.props.data, inboundData)
  assert.equal(table.props.group, 'inbound')
  assert.deepEqual(warnings(ip.tree).map(node => node.props.error.message), ['IP 排行刷新失败', '入站刷新失败'])
  assert.match(warningText(warnings(ip.tree)[1]), /2026-11-15 15:59:59（UTC）/)
  warningButton(warnings(ip.tree)[1]).props.onClick()
  assert.deepEqual(refetches, [mockUseAPI.calls[2].path])

  responses[2].result = queryResult(undefined, new Error('入站暂不可用'), { isPending: false })
  const missing = render('/ips/203.0.113.8')
  assert.equal(walk(missing.tree).find(node => node.type === pages.QueryState).props.error.message, '入站暂不可用')
  assert.equal(statusCards(missing.tree).length, 0)
  assert.equal(warnings(missing.tree).map(node => node.props.error.message).join('|'), 'IP 排行刷新失败')
  assert.equal(walk(missing.tree).find(node => node.type === pages.TrafficChart).props.series, seriesData)
})

const pause = () => new Promise(setImmediate)
function harness(initial = '/sites/cached.example') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
  const observers = []
  let cursor = 0
  let path = initial
  let currentFilter = { ...filter }
  let mode = 'success'
  let failure = () => false
  let statusBody = statusData
  const calls = []
  let deferred = []
  const changes = []
  session = { tz: 'Asia/Shanghai' }
  api.useQuery = (opts) => {
    const index = cursor++
    let observer = observers[index]
    if (!observer) {
      observer = new QueryObserver(client, opts)
      observers[index] = observer
      observer.reviewStop = observer.subscribe(() => {})
    } else observer.setOptions(opts)
    return observer.getCurrentResult()
  }
  api.request = async (url) => {
    calls.push(url)
    if (mode === 'held') return await new Promise((resolve, reject) => deferred.push({ url, resolve, reject }))
    if (mode === 'failed' || failure(url)) throw new Error(`状态请求失败 ${url}`)
    return payload(url)
  }
  function payload(url) {
    const parsed = new URL(url, 'http://meterly.invalid')
    if (parsed.pathname.endsWith('/series')) return seriesData
    const group = parsed.searchParams.get('group')
    if (group === 'status') return statusBody
    if (group === 'inbound' || (group === 'site' && path.startsWith('/ips/'))) return inboundData
    return rankData
  }
  function render() {
    cursor = 0
    return pages.AnalysisPage({
      filter: currentFilter,
      path,
      onChange(patch) { changes.push(patch) },
    })
  }
  return {
    calls, changes, observers, render,
    async settle() { for (let i = 0; i < 6; i++) await pause() },
    setMode(next) { mode = next },
    setFailure(next) { failure = next },
    setStatus(next) { statusBody = next },
    setPath(next) { path = next },
    resetCalls() { calls.length = 0 },
    resolveWhere(predicate) {
      for (const item of deferred.filter(item => predicate(item.url))) item.resolve(payload(item.url))
      deferred = deferred.filter(item => !predicate(item.url))
    },
    rejectWhere(predicate, error) {
      for (const item of deferred.filter(item => predicate(item.url))) item.reject(error)
      deferred = deferred.filter(item => !predicate(item.url))
    },
    close() {
      for (const observer of observers) observer.reviewStop?.()
      client.clear()
    },
  }
}
async function withObservers(path, fn) {
  const previous = pages.useAPI
  const h = harness(path)
  pages.useAPI = api.useAPI
  try { await fn(h) } finally { pages.useAPI = previous; h.close() }
}
const isStatus = (url) => url.includes('group=status')

test('real QueryObserver: status loading, local error, held retry, and recovery leave rank and series in place', async () => {
  await withObservers('/sites/cached.example', async (h) => {
    h.setMode('held')
    const blocked = h.render()
    await h.settle()
    assert.equal(blocked.type, pages.QueryState)
    assert.equal(statusCards(blocked).length, 0)
    h.resolveWhere(url => !isStatus(url))
    await h.settle()
    const loading = h.render()
    const loadingCard = statusCard(loading)
    assert.equal(texts(walk(loadingCard).find(node => node.props && node.props.role === 'status')).join(''), '正在加载请求状态')
    assert.equal(warnings(loading).length, 0)
    assert.equal(walk(loading).find(node => node.type === pages.TrafficTable).props.data, rankData)
    assert.equal(walk(loading).find(node => node.type === pages.TrafficChart).props.series, seriesData)
    const statusKey = h.observers[2].options.queryKey[1]
    assert.equal(params(statusKey).get('group'), 'status')
    assert.equal(params(statusKey).get('status'), null)
    assert.equal(h.observers[2].options.enabled, true)
    h.rejectWhere(isStatus, new Error('状态请求失败'))
    await h.settle()
    const failed = h.render()
    const failedCard = statusCard(failed)
    assert.match(texts(failedCard).join(''), /暂时无法加载/)
    assert.match(texts(failedCard).join(''), /状态请求失败/)
    assert.doesNotMatch(texts(failedCard).join(''), /刷新失败|正在加载请求状态/)
    assert.equal(warnings(failed).length, 0)
    assert.equal(walk(failed).find(node => node.type === pages.TrafficTable).props.data, rankData)
    assert.equal(walk(failed).find(node => node.type === pages.TrafficChart).props.series, seriesData)
    const retry = buttons(failedCard)[0]
    assert.equal(retry.props.disabled, false)
    h.setMode('held')
    h.resetCalls()
    retry.props.onClick()
    await h.settle()
    assert.deepEqual(h.calls, [statusKey])
    const heldResult = h.observers[2].getCurrentResult()
    assert.equal(heldResult.data, undefined)
    assert.equal(heldResult.error, null)
    assert.equal(heldResult.isPending, true)
    assert.equal(heldResult.isFetching, true)
    const held = h.render()
    const heldCard = statusCard(held)
    assert.equal(texts(walk(heldCard).find(node => node.props && node.props.role === 'status')).join(''), '正在加载请求状态')
    assert.equal(buttons(heldCard).length, 0)
    assert.doesNotMatch(texts(heldCard).join(''), /刷新失败|暂时无法加载/)
    assert.equal(warnings(held).length, 0)
    assert.equal(walk(held).find(node => node.type === pages.TrafficTable).props.data, rankData)
    assert.equal(walk(held).find(node => node.type === pages.TrafficChart).props.series, seriesData)
    assert.deepEqual(h.calls, [statusKey])
    h.setMode('success')
    h.resolveWhere(isStatus)
    await h.settle()
    const recovered = h.render()
    assert.deepEqual(buttons(statusCard(recovered)).map(node => texts(node).join('')), ['20012 次', '4042 次'])
    assert.equal(warnings(recovered).length, 0)
    assert.equal(walk(recovered).some(node => node.props && node.props.role === 'alert'), false)
    assert.equal(walk(recovered).find(node => node.type === pages.TrafficTable).props.data, rankData)
    assert.equal(walk(recovered).find(node => node.type === pages.TrafficChart).props.series, seriesData)
    h.resetCalls()
    buttons(statusCard(recovered))[0].props.onClick()
    await h.settle()
    assert.deepEqual(h.calls, [])
    assert.equal(h.changes.length, 1)
    assert.equal(h.changes[0].status, '200')
    assert.equal(h.changes[0].page, 1)
  })
})

test('real QueryObserver: cached status rows and cached empty results keep their view across refresh failure', async () => {
  await withObservers('/sites/cached.example', async (h) => {
    h.render()
    await h.settle()
    const ready = h.render()
    assert.deepEqual(buttons(statusCard(ready)).map(node => texts(node)[0]), ['200', '404'])
    const statusKey = h.observers[2].options.queryKey[1]
    h.setFailure(isStatus)
    h.resetCalls()
    await h.observers[2].refetch()
    await h.settle()
    const failed = h.render()
    const failedCard = statusCard(failed)
    assert.equal(warnings(failed).length, 1)
    assert.match(warningText(warnings(failedCard)[0]), /刷新失败/)
    assert.match(warningText(warnings(failedCard)[0]), /状态请求失败/)
    assert.match(warningText(warnings(failedCard)[0]), /上次成功：\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}（Asia\/Shanghai）/)
    assert.doesNotMatch(texts(failedCard).join(''), /暂时无法加载/)
    assert.deepEqual(buttons(failedCard).map(node => texts(node).join('')), ['20012 次', '4042 次'])
    assert.equal(walk(failed).find(node => node.type === pages.TrafficTable).props.data, rankData)
    assert.equal(walk(failed).find(node => node.type === pages.TrafficChart).props.series, seriesData)
    h.setFailure(() => false)
    h.setMode('held')
    h.resetCalls()
    warningButton(warnings(failedCard)[0]).props.onClick()
    await h.settle()
    assert.deepEqual(h.calls, [statusKey])
    const fetching = h.render()
    assert.equal(warningButton(warnings(statusCard(fetching))[0]).props.disabled, true)
    warningButton(warnings(statusCard(fetching))[0]).props.onClick()
    await h.settle()
    assert.deepEqual(h.calls, [statusKey])
    assert.deepEqual(buttons(statusCard(fetching)).map(node => texts(node)[0]), ['200', '404'])
    h.setMode('success')
    h.resolveWhere(isStatus)
    await h.settle()
    const recovered = h.render()
    assert.equal(warnings(recovered).length, 0)
    assert.equal(walk(recovered).find(node => node.type === pages.TrafficChart).props.series, seriesData)
  })
  await withObservers('/sites/cached.example', async (h) => {
    h.setStatus(emptyStatus)
    h.render()
    await h.settle()
    const empty = h.render()
    assert.equal(walk(statusCard(empty)).find(node => node.type === pages.EmptyState).props.title, '暂无请求状态')
    assert.equal(buttons(statusCard(empty)).length, 0)
    h.setFailure(isStatus)
    await h.observers[2].refetch()
    await h.settle()
    const failed = h.render()
    const card = statusCard(failed)
    assert.equal(warnings(card).length, 1)
    assert.match(warningText(warnings(card)[0]), /刷新失败/)
    assert.equal(walk(card).find(node => node.type === pages.EmptyState).props.title, '暂无请求状态')
    assert.doesNotMatch(texts(card).join(''), /暂时无法加载/)
    assert.equal(walk(failed).find(node => node.type === pages.TrafficTable).props.data, rankData)
    h.setFailure(() => false)
    h.setStatus(statusData)
    h.resetCalls()
    warningButton(warnings(card)[0]).props.onClick()
    await h.settle()
    assert.deepEqual(h.calls, [h.observers[2].options.queryKey[1]])
    const recovered = h.render()
    assert.equal(warnings(recovered).length, 0)
    assert.deepEqual(buttons(statusCard(recovered)).map(node => texts(node)[0]), ['200', '404'])
  })
})

test('real QueryObserver: list queries stay disabled and IP detail never gains a site status card', async () => {
  await withObservers('/sites', async (h) => {
    const tree = h.render()
    await h.settle()
    assert.equal(h.observers[1].options.enabled, false)
    assert.equal(h.observers[2].options.enabled, false)
    assert.equal(h.calls.some(url => url.includes('/series?') || url.includes('group=status')), false)
    assert.equal(statusCards(tree).length, 0)
    assert.equal(walk(h.render()).find(node => node.type === pages.TrafficTable).props.data, rankData)
  })
  await withObservers('/ips', async (h) => {
    h.render()
    await h.settle()
    assert.equal(h.observers[1].options.enabled, false)
    assert.equal(h.observers[2].options.enabled, false)
    assert.equal(params(h.observers[2].options.queryKey[1]).get('group'), 'inbound')
    assert.equal(h.calls.some(url => url.includes('/series?') || url.includes('group=inbound') || url.includes('group=status')), false)
    assert.equal(statusCards(h.render()).length, 0)
  })
  await withObservers('/ips/203.0.113.8', async (h) => {
    h.render()
    await h.settle()
    const statusKey = h.observers[2].options.queryKey[1]
    assert.equal(params(statusKey).get('group'), 'inbound')
    assert.equal(params(statusKey).get('page'), '3')
    assert.equal(params(statusKey).get('status'), '204')
    await h.observers[2].refetch()
    h.setFailure(url => url.includes('group=inbound'))
    await h.observers[2].refetch()
    await h.settle()
    const tree = h.render()
    assert.equal(statusCards(tree).length, 0)
    assert.equal(texts(tree).includes('请求状态'), false)
    assert.equal(walk(tree).find(node => node.type === pages.TrafficTable).props.data, inboundData)
    assert.equal(walk(tree).find(node => node.type === pages.TrafficTable).props.group, 'inbound')
    assert.equal(warnings(tree).map(node => node.props.error.message).some(message => message.includes('group=inbound')), true)
    assert.equal(walk(tree).find(node => node.type === pages.TrafficChart).props.series, seriesData)
    h.setFailure(() => false)
    h.setMode('failed')
    const fresh = harness('/ips/203.0.113.8')
    try {
      fresh.setMode('failed')
      fresh.render()
      await fresh.settle()
      const initial = fresh.render()
      assert.equal(initial.type, pages.QueryState)
      assert.equal(statusCards(initial).length, 0)
    } finally { fresh.close() }
  })
})
