// Node vm simulation of the React element tree. This does not open a browser,
// render DOM, or bypass localhost browser restrictions.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import { createRequire } from 'node:module'

const req = createRequire(import.meta.url)
const { transformSync } = await import(req.resolve('rolldown/experimental'))

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
const midnight = Date.parse('2026-11-15T16:00:00Z')
let session = { tz: 'Asia/Shanghai' }
let responses = []
const refetches = []
pages.useSession = () => session
pages.useAPI = (path, schema, options) => {
  pages.useAPI.calls.push({ path, schema, options })
  const matched = responses.find(item => item.match(path))
  const result = { data: undefined, error: null, isPending: false, isFetching: false, dataUpdatedAt: 0, ...(matched ? matched.result : {}) }
  return { ...result, refetch: () => { refetches.push(path) } }
}
pages.useAPI.calls = []

const filter = { period: 'today', host: 'edge-a', source: 'singbox', start: '', end: '', search: '', ip: '', country: '', asn: '', status: '204', site: '', page: 3, sort: 'total', order: 'desc', gran: 'auto' }
const overviewData = { site: { total: 10 }, meta: { tz: 'Asia/Shanghai', state: 'available', complete: true, reasons: [] }, growth_pct: null, top_sites: [{ key: 'cached.example' }], alerts: [], online: 1, host_count: 1 }
const seriesData = { meta: { from: 1, to: 2, tz: 'Asia/Shanghai', gran: 'hour' }, points: [{ ts: 1, tx: 5 }] }
const rankData = { rows: [{ key: '203.0.113.8', org: 'Cached Org', asn: 64500 }], totals: { total: 9 }, meta: overviewData.meta, count: 1, page: 1, page_size: 10 }
const statusData = { rows: [{ key: '200', requests: 12 }, { key: '404', requests: 2 }], totals: null, meta: overviewData.meta, count: 2, page: 1, page_size: 10 }
function queryResult(data, error, extra = {}) {
  return { data, error, isPending: !data && !error, isFetching: false, dataUpdatedAt: data ? updatedAt : 0, ...extra }
}
function render(page, path) {
  refetches.length = 0
  pages.useAPI.calls = []
  const props = { filter, onChange() {} }
  return path ? pages.AnalysisPage({ ...props, path }) : pages.OverviewPage(props)
}
function warnings(tree) { return walk(tree).filter(node => node.type === pages.RefreshFailure) }
function warningText(node) { return texts(pages.RefreshFailure(node.props)).join('') }
function warningButton(node) { return walk(pages.RefreshFailure(node.props)).find(item => item.type === pages.Button) }

test('timestamp uses the session timezone, defaults to Asia/Shanghai, and keeps explicit zones', () => {
  assert.equal(format.dateTimeLabel(updatedAt), '2026-11-15 23:59:59')
  assert.equal(format.dateTimeLabel(updatedAt, ''), '2026-11-15 23:59:59')
  assert.equal(format.dateTimeLabel(updatedAt, 'America/New_York'), '2026-11-15 10:59:59')
  assert.equal(format.dateTimeLabel(midnight, 'Asia/Shanghai'), '2026-11-16 00:00:00')
  assert.equal(format.dateTimeLabel(midnight, 'UTC'), '2026-11-15 16:00:00')
  assert.equal(format.dateTimeLabel(midnight, 'America/New_York'), '2026-11-15 11:00:00')
})

test('unsafe last-success times render 未知 and invalid zones fall back to Asia/Shanghai', () => {
  const unknown = '未知'
  const unsafe = [Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY, undefined, null, 0, 8_640_000_000_000_001, -8_640_000_000_000_001]
  for (const value of unsafe) {
    assert.equal(format.dateTimeLabel(value, 'Asia/Shanghai'), unknown)
    assert.equal(format.dateTimeLabel(value, 'America/New_York'), unknown)
    assert.equal(format.dateTimeLabel(value, 'Not/AZone'), unknown)
    assert.equal(format.dateTimeLabel(value), unknown)
  }
  assert.equal(format.dateTimeLabel(0, 'Asia/Shanghai'), unknown)
  assert.notEqual(format.dateTimeLabel(0, 'Asia/Shanghai'), '1970-01-01 08:00:00')
  assert.equal(format.dateTimeLabel(undefined, 'Asia/Shanghai'), unknown)
  assert.notEqual(format.dateTimeLabel(undefined, 'Asia/Shanghai'), format.dateTimeLabel(Date.now(), 'Asia/Shanghai'))
  assert.equal(format.dateTimeLabel(-1, 'Asia/Shanghai'), '1970-01-01 07:59:59')
  assert.equal(format.dateTimeLabel(1, 'Asia/Shanghai'), '1970-01-01 08:00:00')
  assert.equal(format.displayTimeZone('Not/AZone'), 'Asia/Shanghai')
  assert.equal(format.displayTimeZone(''), 'Asia/Shanghai')
  assert.equal(format.displayTimeZone(null), 'Asia/Shanghai')
  assert.equal(format.displayTimeZone('Asia/Shanghai'), 'Asia/Shanghai')
  assert.equal(format.displayTimeZone('America/New_York'), 'America/New_York')
  assert.equal(format.displayTimeZone('UTC'), 'UTC')
  assert.equal(format.dateTimeLabel(updatedAt, 'Not/AZone'), format.dateTimeLabel(updatedAt, 'Asia/Shanghai'))
  assert.equal(format.dateTimeLabel(updatedAt, 'Asia/Shanghai'), '2026-11-15 23:59:59')
  assert.equal(format.dateTimeLabel(updatedAt, 'America/New_York'), '2026-11-15 10:59:59')
  assert.equal(format.dateTimeLabel(midnight, 'UTC'), '2026-11-15 16:00:00')

  session = { tz: 'Asia/Shanghai' }
  const shanghai = texts(pages.RefreshFailure({ error: new Error('failed'), updatedAt, fetching: false, onRetry() {} })).join('')
  session = { tz: 'Not/AZone' }
  const fallback = texts(pages.RefreshFailure({ error: new Error('failed'), updatedAt, fetching: false, onRetry() {} })).join('')
  assert.equal(fallback, shanghai)
  assert.match(fallback, /上次成功：2026-11-15 23:59:59（Asia\/Shanghai）/)
  assert.doesNotMatch(fallback, /Not\/AZone/)
  session = { tz: 'America/New_York' }
  const explicit = texts(pages.RefreshFailure({ error: new Error('failed'), updatedAt, fetching: false, onRetry() {} })).join('')
  assert.match(explicit, /上次成功：2026-11-15 10:59:59（America\/New_York）/)
  assert.doesNotMatch(explicit, /Asia\/Shanghai/)

  for (const value of unsafe) {
    for (const tz of ['Not/AZone', 'Asia/Shanghai', 'America/New_York']) {
      session = { tz }
      const expectedZone = format.displayTimeZone(tz)
      const warning = texts(pages.RefreshFailure({ error: new Error('boundary'), updatedAt: value, fetching: false, onRetry() {} })).join('')
      assert.match(warning, new RegExp(`上次成功：未知（${expectedZone}）`))
      assert.doesNotMatch(warning, /1970/)
      assert.doesNotMatch(warning, /Not\/AZone/)
      assert.doesNotMatch(warning, /\d{4}-\d{2}-\d{2}/)
    }
  }
})

test('api query key, polling, and enabled semantics stay unchanged', () => {
  const source = fs.readFileSync(new URL('../frontend/src/lib/api.ts', import.meta.url), 'utf8')
  assert.match(source, /queryKey: \['meterly', path\]/)
  assert.match(source, /staleTime: 30_000/)
  assert.match(source, /refetchInterval:/)
  assert.match(source, /retry: false/)
  assert.match(source, /refetchOnWindowFocus: false/)
  assert.match(source, /enabled: options\.enabled \?\? true/)
})

test('overview keeps cached overview and series, warns with reason, time, and same-query retry', () => {
  session = { tz: 'America/New_York' }
  responses = [
    { match: path => path.startsWith('/api/v2/overview?'), result: queryResult(overviewData, new Error('总览刷新失败')) },
    { match: path => path.includes('/analytics/series?'), result: queryResult(seriesData, new Error('曲线刷新失败'), { isFetching: true }) },
  ]
  const tree = render('overview')
  assert.equal(pages.useAPI.calls.length, 2)
  assert.equal(pages.useAPI.calls[0].options, undefined)
  assert.equal(pages.useAPI.calls[1].options, undefined)
  assert.match(pages.useAPI.calls[0].path, /host=edge-a/)
  assert.match(pages.useAPI.calls[0].path, /source=site/)
  assert.match(pages.useAPI.calls[0].path, /status=204/)
  const found = warnings(tree)
  assert.equal(found.length, 2)
  assert.equal(warningText(found[0]), '刷新失败总览刷新失败上次成功：2026-11-15 10:59:59（America/New_York）重试')
  assert.equal(found[0].props.updatedAt, updatedAt)
  const seriesWarning = found[1]
  assert.match(warningText(seriesWarning), /曲线刷新失败/)
  assert.equal(warningButton(seriesWarning).props.disabled, true)
  warningButton(seriesWarning).props.onClick()
  assert.deepEqual(refetches, [])
  const chart = walk(tree).find(node => node.type === pages.TrafficChart)
  assert.equal(chart.props.series, seriesData)
  assert.equal(walk(tree).some(node => node.type === pages.QueryState), false)
  assert.equal(walk(tree).find(node => node.type === pages.TrafficTable).props.data.rows[0].key, 'cached.example')
  warningButton(found[0]).props.onClick()
  assert.deepEqual(refetches, [pages.useAPI.calls[0].path])
  responses[1].result = queryResult(seriesData, null)
  const recovered = render('overview')
  assert.equal(warnings(recovered).length, 1)
  assert.match(warningText(warnings(recovered)[0]), /总览刷新失败/)
  assert.equal(walk(recovered).find(node => node.type === pages.TrafficChart).props.series, seriesData)
  responses[0].result = queryResult(overviewData, null)
  const clear = render('overview')
  assert.equal(warnings(clear).length, 0)
  assert.equal(walk(clear).find(node => node.type === pages.MetricGrid).props.totals, overviewData.site)
})

test('initial overview and series errors without cached data stay on QueryState', () => {
  session = { tz: 'Asia/Shanghai' }
  responses = [
    { match: path => path.startsWith('/api/v2/overview?'), result: queryResult(undefined, new Error('总览暂不可用')) },
    { match: path => path.includes('/analytics/series?'), result: queryResult(undefined, new Error('曲线暂不可用')) },
  ]
  const tree = render('overview')
  assert.equal(tree.type, pages.QueryState)
  assert.equal(tree.props.error.message, '总览暂不可用')
  assert.equal(warnings(tree).length, 0)
  tree.props.onRetry()
  assert.deepEqual(refetches, [pages.useAPI.calls[0].path])
  const initial = pages.QueryState({ error: new Error('曲线测试服务暂不可用'), onRetry() {} })
  const text = texts(initial).join('')
  assert.match(text, /暂时无法加载/)
  assert.match(text, /曲线测试服务暂不可用/)
  assert.match(text, /重新加载/)
  assert.doesNotMatch(text, /刷新失败/)
  responses[0].result = queryResult(overviewData, null)
  const seriesOnly = render('overview')
  assert.equal(walk(seriesOnly).filter(node => node.type === pages.QueryState).length, 1)
  assert.equal(walk(seriesOnly).some(node => node.type === pages.TrafficChart), false)
  assert.equal(warnings(seriesOnly).length, 0)
  assert.equal(walk(seriesOnly).find(node => node.type === pages.QueryState).props.error.message, '曲线暂不可用')
})

test('rank and series refresh failures keep cached analysis content and retry the same filter', () => {
  session = { tz: '' }
  responses = [
    { match: path => path.includes('/analytics/rank?') && path.includes('group=ip'), result: queryResult(rankData, new Error('排行刷新失败')) },
    { match: path => path.includes('/analytics/series?'), result: queryResult(seriesData, new Error('详情曲线刷新失败')) },
    { match: path => path.includes('group=status'), result: queryResult(statusData, new Error('状态刷新失败')) },
  ]
  const tree = render('analysis', '/sites/cached.example')
  const calls = pages.useAPI.calls
  assert.equal(calls[1].options.enabled, true)
  assert.equal(calls[2].options.enabled, true)
  assert.match(calls[0].path, /site=cached\.example/)
  assert.match(calls[0].path, /group=ip/)
  assert.match(calls[0].path, /status=204/)
  assert.match(calls[2].path, /group=status/)
  assert.match(calls[2].path, /site=cached\.example/)
  assert.doesNotMatch(calls[2].path, /status=204/)
  assert.equal(calls[2].path.includes('page=1'), true)
  const found = warnings(tree)
  assert.equal(found.map(node => node.props.error.message).join('|'), '排行刷新失败|详情曲线刷新失败|状态刷新失败')
  assert.match(warningText(found[0]), /2026-11-15 23:59:59（Asia\/Shanghai）/)
  assert.equal(walk(tree).find(node => node.type === pages.TrafficChart).props.series, seriesData)
  assert.equal(walk(tree).find(node => node.type === pages.TrafficTable).props.data, rankData)
  const statusCard = walk(tree).find(node => node.type === pages.Card && walk(node).some(child => child.type === pages.CardTitle && texts(child).join('') === '请求状态'))
  assert.ok(statusCard)
  assert.equal(walk(statusCard).filter(node => node.type === pages.RefreshFailure).length, 1)
  assert.match(warningText(walk(statusCard).find(node => node.type === pages.RefreshFailure)), /状态刷新失败/)
  const labels = walk(statusCard).filter(node => node.type === pages.Button).map(node => texts(node).join(''))
  assert.deepEqual(labels, ['20012 次', '4042 次'])
  warningButton(found[2]).props.onClick()
  assert.deepEqual(refetches, [calls[2].path])
  responses[2].result = queryResult(statusData, null)
  const recovered = render('analysis', '/sites/cached.example')
  const statusAfter = walk(recovered).find(node => node.type === pages.Card && walk(node).some(child => child.type === pages.CardTitle && texts(child).join('') === '请求状态'))
  assert.equal(walk(statusAfter).some(node => node.type === pages.RefreshFailure), false)
  assert.deepEqual(walk(statusAfter).filter(node => node.type === pages.Button).map(node => texts(node)[0]), ['200', '404'])
})

test('series and associated content without cached data do not use the refresh warning', () => {
  session = { tz: 'Asia/Shanghai' }
  responses = [
    { match: path => path.includes('/analytics/rank?') && path.includes('group=ip'), result: queryResult(undefined, new Error('排行暂不可用')) },
    { match: path => path.includes('/analytics/series?'), result: queryResult(undefined, new Error('曲线暂不可用')) },
    { match: path => path.includes('/analytics/rank?') && path.includes('group=status'), result: queryResult(undefined, new Error('状态暂不可用'), { isPending: false }) },
  ]
  const initial = render('analysis', '/sites/cached.example')
  assert.equal(initial.type, pages.QueryState)
  assert.equal(initial.props.error.message, '排行暂不可用')
  assert.equal(warnings(initial).length, 0)
  initial.props.onRetry()
  assert.deepEqual(refetches, [pages.useAPI.calls[0].path])
  responses[0].result = queryResult(rankData, null)
  const detail = render('analysis', '/sites/cached.example')
  assert.equal(walk(detail).filter(node => node.type === pages.QueryState).length, 1)
  assert.equal(walk(detail).find(node => node.type === pages.QueryState).props.error.message, '曲线暂不可用')
  assert.equal(walk(detail).some(node => node.type === pages.TrafficChart), false)
  assert.equal(texts(detail).includes('请求状态'), false)
  assert.equal(warnings(detail).length, 0)
  responses[1].result = queryResult(seriesData, null)
  responses[2].result = { data: undefined, error: null, isPending: true, isFetching: true, dataUpdatedAt: 0 }
  const pending = render('analysis', '/sites/cached.example')
  assert.equal(texts(pending).includes('请求状态'), false)
  assert.equal(walk(pending).some(node => node.type === pages.QueryState), false)
  responses[2].result = queryResult({ ...statusData, rows: [] }, new Error('空状态刷新失败'))
  const emptyCached = render('analysis', '/sites/cached.example')
  assert.equal(texts(emptyCached).includes('请求状态'), true)
  assert.equal(warnings(emptyCached).length, 1)
  assert.match(warningText(warnings(emptyCached)[0]), /空状态刷新失败/)
})

test('ip detail keeps cached associated rows and does not add a site request-status region', () => {
  session = { tz: 'UTC' }
  const associated = { ...statusData, rows: [{ key: 'inbound-a', requests: 4 }] }
  responses = [
    { match: path => path.includes('/analytics/rank?') && path.includes('group=ip'), result: queryResult(rankData, new Error('IP 排行刷新失败')) },
    { match: path => path.includes('/analytics/series?'), result: queryResult(seriesData, null) },
    { match: path => path.includes('group=inbound'), result: queryResult(associated, new Error('入站刷新失败')) },
  ]
  const tree = render('analysis', '/ips/203.0.113.8')
  const calls = pages.useAPI.calls
  assert.match(calls[0].path, /ip=203\.0\.113\.8/)
  assert.match(calls[0].path, /page=1/)
  assert.match(calls[2].path, /group=inbound/)
  assert.match(calls[2].path, /page=3/)
  assert.match(calls[2].path, /status=204/)
  assert.equal(calls[1].options.enabled, true)
  assert.equal(calls[2].options.enabled, true)
  assert.equal(texts(tree).includes('请求状态'), false)
  const tables = walk(tree).filter(node => node.type === pages.TrafficTable)
  assert.equal(tables.at(-1).props.data, associated)
  assert.equal(tables.at(-1).props.group, 'inbound')
  const found = warnings(tree)
  assert.deepEqual(found.map(node => node.props.error.message), ['IP 排行刷新失败', '入站刷新失败'])
  assert.match(warningText(found[1]), /2026-11-15 15:59:59（UTC）/)
  warningButton(found[1]).props.onClick()
  assert.deepEqual(refetches, [calls[2].path])
  responses[2].result = queryResult(undefined, new Error('入站暂不可用'), { isPending: false })
  const missing = render('analysis', '/ips/203.0.113.8')
  assert.equal(walk(missing).find(node => node.type === pages.QueryState).props.error.message, '入站暂不可用')
  assert.equal(warnings(missing).map(node => node.props.error.message).join('|'), 'IP 排行刷新失败')
  assert.equal(texts(missing).includes('请求状态'), false)
})

test('list pages keep rank cache on refresh failure and leave detail queries disabled', () => {
  session = { tz: 'Asia/Shanghai' }
  responses = [
    { match: path => path.includes('/analytics/rank?') && path.includes('group=site'), result: queryResult(rankData, new Error('列表刷新失败')) },
    { match: path => path.includes('/analytics/series?'), result: queryResult(seriesData, new Error('不应显示')) },
    { match: path => path.includes('/analytics/rank?') && (path.includes('group=status') || path.includes('group=inbound')), result: queryResult(statusData, new Error('不应显示')) },
  ]
  const tree = render('analysis', '/sites')
  assert.equal(pages.useAPI.calls[1].options.enabled, false)
  assert.equal(pages.useAPI.calls[2].options.enabled, false)
  assert.equal(warnings(tree).map(node => node.props.error.message).join('|'), '列表刷新失败')
  assert.equal(walk(tree).some(node => node.type === pages.TrafficChart), false)
  assert.equal(texts(tree).includes('请求状态'), false)
  assert.equal(walk(tree).find(node => node.type === pages.TrafficTable).props.data, rankData)
  warningButton(warnings(tree)[0]).props.onClick()
  assert.deepEqual(refetches, [pages.useAPI.calls[0].path])
})
