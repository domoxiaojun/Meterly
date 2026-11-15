import { test, expect } from './fixtures.js';

test('live calendar ranges poll, history stops, and switching restores polling', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-11-11T04:30:00Z') });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const counts = new Map();
  const key = url => {
    const q = new URL(url).searchParams;
    return [q.get('period'), q.get('start'), q.get('end')].join('|');
  };
  await page.route('**/api/v2/analytics/rank?**', async route => {
    const response = await route.fetch();
    const data = await response.json();
    const k = key(route.request().url());
    await route.fulfill({ response, json: data });
    counts.set(k, (counts.get(k) || 0) + 1);
  });
  expect((await page.request.post('/api/login', { data: { token: 'local-browser-test-token' } })).ok()).toBeTruthy();
  const live = 'custom|2026-11-11|2026-11-11';
  const history = 'custom|2026-11-09|2026-11-10';
  await page.goto('/#/sites?period=custom&start=2026-11-11&end=2026-11-11');
  await expect(page.getByText('网站流量排行', { exact: true })).toBeVisible();
  await expect.poll(() => counts.get(live) || 0).toBeGreaterThan(0);
  const first = counts.get(live);
  await page.clock.runFor(31_000);
  await expect.poll(() => counts.get(live) || 0).toBeGreaterThan(first);

  await page.evaluate(() => { location.hash = '/sites?period=custom&start=2026-11-09&end=2026-11-10'; });
  await expect.poll(() => counts.get(history) || 0).toBeGreaterThan(0);
  await expect(page.getByText('网站流量排行', { exact: true })).toBeVisible();
  const historicCount = counts.get(history), liveCount = counts.get(live);
  await page.clock.runFor(61_000);
  expect(counts.get(history)).toBe(historicCount);
  expect(counts.get(live)).toBe(liveCount);

  await page.evaluate(() => { location.hash = '/sites?period=custom&start=2026-11-11&end=2026-11-11'; });
  await expect(page).toHaveURL(/start=2026-11-11/);
  await expect(page.getByText('网站流量排行', { exact: true })).toBeVisible();
  const resumed = counts.get(live);
  await page.clock.runFor(31_000);
  await expect.poll(() => counts.get(live) || 0).toBeGreaterThan(resumed);
  expect(counts.get(history)).toBe(historicCount);

  for (const [query, k] of [
    ['period=day&start=2026-11-11', 'day|2026-11-11|'],
    ['period=custom&start=2026-11-10&end=2026-11-11', 'custom|2026-11-10|2026-11-11'],
  ]) {
    await page.evaluate(q => { location.hash = '/sites?' + q; }, query);
    await expect.poll(() => counts.get(k) || 0).toBeGreaterThan(0);
    const before = counts.get(k);
    await page.clock.runFor(31_000);
    await expect.poll(() => counts.get(k) || 0).toBeGreaterThan(before);
  }
});
