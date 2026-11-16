import { test, expect } from './fixtures.js';

const stale = 'period=day&start=2026-11-10&host=server-a&source=singbox&ip=203.0.113.200&country=ZZ&asn=AS64599&status=599&site=missing.example&search=missing&page=4';
const unsupported = ['ip', 'country', 'asn', 'status', 'site', 'search'];
test.beforeEach(async ({ page }) => {
  expect((await page.request.post('/api/login', { data: { token: 'local-browser-test-token' } })).ok()).toBeTruthy();
});

async function checkOverview(page, navigate) {
  const paths = [];
  page.on('request', req => {
    const u = new URL(req.url());
    if ((u.pathname === '/api/v2/overview' || u.pathname === '/api/v2/analytics/series') && u.searchParams.get('period') === 'day') paths.push(u);
  });
  const overviewResponse = page.waitForResponse(res => {
    const u = new URL(res.url());
    return u.pathname === '/api/v2/overview' && u.searchParams.get('period') === 'day';
  });
  const seriesResponse = page.waitForResponse(res => {
    const u = new URL(res.url());
    return u.pathname === '/api/v2/analytics/series' && u.searchParams.get('period') === 'day';
  });
  await navigate();
  await expect(page.getByRole('heading', { name: '流量总览', exact: true })).toBeVisible();
  const [overview, series] = await Promise.all([(await overviewResponse).json(), (await seriesResponse).json()]);
  expect(overview.site.total).toBeGreaterThan(0);
  expect(series.points.reduce((n, p) => n + p.total, 0)).toBe(overview.site.total);
  expect(paths.length).toBeGreaterThanOrEqual(2);
  for (const u of paths) {
    for (const k of unsupported) expect(u.searchParams.get(k) || '', u.href + ' retains ' + k).toBe('');
    expect(u.searchParams.get('source')).toBe('site');
    expect(u.searchParams.get('host')).toBe('server-a');
    expect(u.searchParams.get('start')).toBe('2026-11-10');
  }
  await expect(page.locator('.active-filters')).toHaveCount(0);
}

for (const path of ['/', '/overview']) test('direct overview clears unsupported analysis filters: ' + path, async ({ page }) => {
  await checkOverview(page, () => page.goto('/#' + path + '?' + stale));
});

test('brand return clears analysis filters but preserves host and date', async ({ page }) => {
  await page.goto('/#/ips?' + stale);
  await expect(page.getByRole('heading', { name: 'IP 分析', exact: true })).toBeVisible();
  await checkOverview(page, () => page.locator('a.brand').click());
  const params = new URLSearchParams(new URL(page.url()).hash.split('?')[1]);
  for (const k of unsupported) expect(params.get(k) || '').toBe('');
});

test('sidebar return clears filters without changing analytical pages', async ({ page }) => {
  await page.goto('/#/ips?period=day&start=2026-11-10&host=server-a&ip=198.51.100.137&country=CN');
  await expect(page.locator('.active-filters')).toContainText('198.51.100.137');
  await expect(page.locator('.active-filters')).toContainText('CN');
  await checkOverview(page, () => page.getByRole('link', { name: '流量总览', exact: true }).click());
});
