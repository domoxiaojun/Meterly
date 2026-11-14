import { test, expect } from './fixtures.js';

const token = 'local-browser-test-token';
async function login(page, path = '/') {
  await page.goto(path);
  await page.getByLabel('面板口令', { exact: true }).fill(token);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.locator('.workspace-main')).toBeVisible();
}

test('embedded production app uses real auth, retains the route, and logs out', async ({ page }) => {
  const paths = [], errors = [];
  page.on('request', req => paths.push(new URL(req.url()).pathname));
  page.on('pageerror', e => errors.push(e.message));
  const response = await page.goto('/#/sites?period=day&start=2026-11-10');
  expect(response.headers()['content-security-policy']).toContain("script-src 'self'");
  await expect(page.getByRole('heading', { name: '登录 Meterly' })).toBeVisible();
  expect(paths.some(p => p.includes('/analytics/'))).toBeFalsy();
  await page.getByLabel('面板口令', { exact: true }).fill('incorrect-test-token');
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('面板口令不正确');
  await page.getByLabel('面板口令', { exact: true }).fill(token);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByText('网站流量排行', { exact: true })).toBeVisible();
  await expect(page).toHaveURL(/start=2026-11-10/);
  await expect(page.locator('.page-date')).toContainText(/中心时间.*11.*11/);
  await expect(page.getByText('演示数据', { exact: true })).toHaveCount(0);
  expect(paths).not.toContain('/api/preview');
  expect(paths.some(p => /^\/assets\/.+\.js$/.test(p))).toBeTruthy();
  await page.reload();
  await expect(page.getByText('网站流量排行', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: '退出登录' }).click();
  await expect(page.getByRole('heading', { name: '登录 Meterly' })).toBeVisible();
  expect((await page.request.get('/api/v2/me')).status()).toBe(401);
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }));
  expect(stored).not.toContain(token);
  expect(errors).toEqual([]);
});

test('six production pages, pending host and classic management remain usable', async ({ page }) => {
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  await login(page);
  for (const [path, title] of [['/overview', '流量总览'], ['/sites', '网站分析'], ['/ips', 'IP 分析'], ['/hosts', '主机管理'], ['/billing', '计费与对账'], ['/system', '系统设置']]) {
    await page.goto(`/#${path}?period=day&start=2026-11-10`);
    await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
    await expect(page.locator('.page-sections')).toBeVisible();
    await expect(page.getByText('响应数据不完整，暂时无法显示')).toHaveCount(0);
  }
  await page.getByRole('link', { name: '管理注册码与维护任务' }).click();
  await expect(page).toHaveURL(/\/legacy\/#\/system/);
  await expect(page.getByRole('heading', { name: '接入新主机', exact: true })).toBeVisible();
  await page.getByRole('link', { name: '返回新版工作台' }).click();
  await expect(page.getByRole('heading', { name: '流量总览', exact: true })).toBeVisible();
  await page.goto('/#/hosts/pending-host');
  await expect(page.locator('.host-card')).toContainText('尚未上报');
  await expect(page.locator('.host-platform')).toContainText('系统未上报');
  expect(errors).toEqual([]);
});

test('production rankings support drill-down, pagination, search and CSV', async ({ page }) => {
  await login(page, '/#/sites/example.com?period=day&start=2026-11-10');
  await expect(page.locator('.table-footer')).toContainText('共 137 条结果');
  await page.getByRole('button', { name: '下一页', exact: true }).click();
  await expect(page.locator('.table-footer')).toContainText('2 / 14');
  const before = page.url();
  await page.locator('tbody .table-link').first().click();
  await expect(page.getByText('关联网站', { exact: true })).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(before);
  await expect(page.locator('.table-footer')).toContainText('2 / 14');
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: '导出 CSV' }).click()]);
  const stream = await download.createReadStream();
  const chunks = []; for await (const chunk of stream) chunks.push(chunk);
  expect(Buffer.concat(chunks).toString().trim().split('\n')).toHaveLength(138);
  await page.goto('/#/sites?period=day&start=2026-11-10');
  const input = page.getByRole('textbox', { name: '搜索网站', exact: true });
  await input.fill('media');
  await expect(page).toHaveURL(/search=media/);
  await expect(input).toBeFocused();
  await expect(page.locator('.table-footer')).toContainText('共 1 条结果');
});

test('production IP list and details display province and city', async ({ page }) => {
  const location = '中国 · Guangdong · Shenzhen';
  await login(page, '/#/ips?period=day&start=2026-11-10');
  await expect(page.locator('tbody .table-identity').first()).toContainText(location);
  await page.goto('/#/ips/198.51.100.7?period=day&start=2026-11-10');
  await expect(page.locator('.ip-profile')).toContainText(location);
  await page.getByRole('button', { name: '代理载荷', exact: true }).click();
  await expect(page.locator('.ip-profile')).toContainText(location);
  await page.setViewportSize({ width: 375, height: 812 });
  await expect(page.locator('.ip-profile').getByText(location, { exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
  await page.screenshot({ path: 'test-results/ip-province-city-mobile.png', fullPage: true });
});

test('expired sessions clear private data and restore the requested page after login', async ({ page, context }) => {
  await login(page, '/#/hosts');
  await expect(page.locator('.host-card')).toHaveCount(3);
  await context.clearCookies();
  await page.getByRole('button', { name: '刷新数据' }).click();
  await expect(page.getByRole('heading', { name: '登录 Meterly' })).toBeVisible();
  await expect(page.locator('.host-card')).toHaveCount(0);
  await page.getByLabel('面板口令', { exact: true }).fill(token);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.locator('.host-card')).toHaveCount(3);
  await expect(page).toHaveURL(/#\/hosts/);
});

test('mobile controls, dialogs and charts work under production CSP', async ({ page }) => {
  await page.addInitScript(() => { window.cspErrors = []; document.addEventListener('securitypolicyviolation', e => window.cspErrors.push(`${e.violatedDirective}: ${e.blockedURI}`)); });
  await page.setViewportSize({ width: 375, height: 812 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await login(page);
  await expect(page.locator('.traffic-chart svg').first()).toBeVisible();
  await page.getByRole('button', { name: '最近 7 天' }).click();
  await expect(page.locator('[data-slot=calendar]')).toBeVisible();
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: '切换主题' }).click();
  await page.getByRole('menuitem', { name: '深色主题' }).click();
  await expect(page.locator('html')).toHaveClass(/dark/);
  await page.getByRole('button', { name: '展开或收起导航' }).click();
  await page.getByRole('link', { name: '系统设置', exact: true }).click();
  await expect(page.getByRole('heading', { name: '系统设置', exact: true })).toBeVisible();
  await page.getByRole('button', { name: '查看接入流程' }).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await page.keyboard.press('Escape');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
  await page.screenshot({ path: 'test-results/production-mobile-dark.png', fullPage: true });
  await page.setViewportSize({ width: 1440, height: 1000 });
  expect(await page.evaluate(() => window.cspErrors)).toEqual([]);
  await page.goto('/#/overview');
  await expect(page.locator('.traffic-chart svg').first()).toBeVisible();
  await page.screenshot({ path: 'test-results/production-desktop.png', fullPage: true });
  expect(await page.evaluate(() => window.cspErrors)).toEqual([]);
});
