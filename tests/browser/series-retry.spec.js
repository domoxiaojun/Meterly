import { test, expect } from './fixtures.js';

for (const detail of [
  { path: '/sites/example.com', heading: '客户端 IP 排行' },
  { path: '/ips/198.51.100.7', heading: '关联网站' },
]) {
  test(`historical ${detail.path} shows a series error and recovers on retry`, async ({ page }) => {
    await page.clock.install({ time: new Date('2026-11-11T04:30:00Z') });
    await page.emulateMedia({ reducedMotion: 'reduce' });
    let fail = true, requests = 0;
    await page.route('**/api/v2/analytics/series?**', async route => {
      requests++;
      if (fail) await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: '曲线测试服务暂不可用' }) });
      else await route.continue();
    });
    expect((await page.request.post('/api/login', { data: { token: 'local-browser-test-token' } })).ok()).toBeTruthy();
    await page.goto(`/#${detail.path}?period=day&start=2026-11-10`);
    await expect(page.getByText(detail.heading, { exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: '暂时无法加载' })).toBeVisible();
    await expect(page.getByText('曲线测试服务暂不可用', { exact: true })).toBeVisible();
    await expect(page.getByText('正在载入时段数据', { exact: true })).toHaveCount(0);
    expect(requests).toBe(1);
    await page.clock.runFor(61_000);
    expect(requests).toBe(1);
    fail = false;
    await page.getByRole('button', { name: '重新加载', exact: true }).click();
    await expect(page.getByText('查看按时段明细', { exact: true })).toBeVisible();
    await expect(page.getByRole('img', { name: '当前筛选范围的流量趋势' })).toBeVisible();
    await expect(page.getByRole('heading', { name: '暂时无法加载' })).toHaveCount(0);
    await expect(page.getByText('正在载入时段数据', { exact: true })).toHaveCount(0);
    await expect(page.getByText(detail.heading, { exact: true })).toBeVisible();
    expect(requests).toBe(2);
  });
}
