import { test, expect } from './fixtures.js';
import { createHash } from 'node:crypto';

for (const second of [0, 37]) {
  for (const query of ['period=today', 'period=day&start=2026-11-12', 'period=custom&start=2026-11-12&end=2026-11-12']) {
    test(`empty Shanghai current minute at second ${second} keeps polling ${query}`, async ({ page }, info) => {
      const midnight = Date.parse('2026-11-11T16:00:00Z') / 1000;
      await page.clock.install({ time: new Date((midnight + second) * 1000) });
      await page.emulateMedia({ reducedMotion: 'reduce' });
      // API shape and live flag are exercised against real storage in the Go regression.
      const meta = { from: midnight, to: midnight, tz: 'Asia/Shanghai', gran: 'hour', resolution: 'minute', state: 'not_collected', complete: false, reasons: ['尚未采集'], ips_available: true, live: true };
      let requests = 0;
      await page.route('**/api/v2/analytics/rank?**', async route => {
        if (new URL(route.request().url()).searchParams.get('group') === 'site') requests++;
        await route.fulfill({ json: { rows: [], totals: null, meta, count: 0, page: 1, page_size: 10 } });
      });
      await page.route('**/api/v2/analytics/series?**', route => route.fulfill({ json: { points: [], meta } }));
      const id = createHash('sha256').update(info.testId).digest('hex');
      const login = await page.request.post('/api/login', { headers: { 'X-Forwarded-For': `2001:db8:${id.slice(0, 4)}:${id.slice(4, 8)}::1` }, data: { token: 'local-browser-test-token' } });
      expect(login.status()).toBe(200);
      await page.goto('/#/sites?' + query);
      await expect(page.getByText('网站流量排行', { exact: true })).toBeVisible();
      await expect(page.getByRole('heading', { name: '暂时无法加载' })).toHaveCount(0);
      await expect.poll(() => requests).toBeGreaterThan(0);
      const initial = requests;
      await page.clock.runFor(31_000);
      await expect.poll(() => requests).toBeGreaterThan(initial);
      await expect(page.getByRole('heading', { name: '暂时无法加载' })).toHaveCount(0);
    });
  }
}
