import { test, expect } from './fixtures.js';

test.use({ timezoneId: 'America/Los_Angeles' });

test('calendar today follows Shanghai server midnight, not the browser date', async ({ page }) => {
  const now = new Date('2026-11-11T15:59:37Z');
  await page.clock.install({ time: now });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.route('**/api/v2/me', async route => {
    const response = await route.fetch();
    const data = await response.json();
    await route.fulfill({ response, json: { ...data, now: now.getTime() / 1000 } });
  });
  expect((await page.request.post('/api/login', { data: { token: 'local-browser-test-token' } })).ok()).toBeTruthy();
  await page.goto('/#/?period=today');
  await expect(page.getByText('Asia/Shanghai', { exact: true }).first()).toBeVisible();
  await page.locator('.filter-select').filter({ hasText: '今天' }).click();
  const today = page.locator('.date-popover button[data-day="2026/11/11"]');
  const tomorrow = page.locator('.date-popover button[data-day="2026/11/12"]');
  await expect(today).toBeEnabled();
  await expect(tomorrow).toBeDisabled();
  await page.clock.runFor(31_000);
  await expect(tomorrow).toBeEnabled();
  expect(await page.evaluate(() => new Date().getDate())).toBe(11);
});
