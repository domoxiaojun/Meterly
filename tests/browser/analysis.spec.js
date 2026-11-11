import {test,expect} from '@playwright/test';
test('date → website → complete IP ranking → detail → back, URL and CSV',async({page})=>{
 const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto('/');await page.getByRole('link',{name:'网站',exact:true}).click();
 await page.locator('[name=period]').selectOption('day');await page.locator('[name=start]').fill('2026-11-10');await page.getByRole('button',{name:'应用筛选'}).click();
 await page.locator('tbody').getByRole('link',{name:'example.com',exact:true}).click();await expect(page.getByRole('heading',{name:'客户端 IP 排行'})).toBeVisible();
 await expect(page.locator('.pager')).toContainText('137 条');
 await page.getByRole('link',{name:'下一页',exact:true}).click();await expect(page.locator('.pager')).toContainText('第 2 / 3 页');
 const before=page.url();await page.locator('tbody .identity a').first().scrollIntoViewIfNeeded();const scrollBefore=await page.evaluate(()=>window.scrollY);const ip=await page.locator('tbody .identity a').first().textContent();await page.locator('tbody .identity a').first().click();await expect(page.getByRole('heading',{name:ip,exact:true})).toBeVisible();await expect(page.getByRole('heading',{name:'请求状态',exact:true})).toBeVisible();
 await page.goBack();await expect(page).toHaveURL(before);await expect(page.locator('.pager')).toContainText('第 2 / 3 页');await expect.poll(()=>page.evaluate(()=>window.scrollY)).toBe(scrollBefore);
 await page.reload();await expect(page.locator('[name=start]')).toHaveValue('2026-11-10');await expect(page.locator('.pager')).toContainText('第 2 / 3 页');
 const url=await page.getByRole('link',{name:'导出全部 CSV'}).getAttribute('href');const response=await page.request.get(url);expect(response.ok()).toBeTruthy();const csv=await response.text();expect(csv.trim().split('\n')).toHaveLength(138);
 expect(errors).toEqual([]);
});
test('abort obsolete requests and retry errors',async({page})=>{
 let failed=false;
 await page.route('**/api/v2/analytics/rank?**',async route=>{if(!failed){failed=true;return route.fulfill({status:503,contentType:'application/json',body:'{"error":"临时测试错误"}'});}return route.continue();});
 await page.goto('/#/sites?period=day&start=2026-11-10');await expect(page.getByText('临时测试错误')).toBeVisible();await page.getByRole('button',{name:'重试',exact:true}).click();await expect(page.getByRole('heading',{name:'网站排行'})).toBeVisible();
 await page.route('**/api/v2/analytics/rank?**',async route=>{if(route.request().url().includes('2026-11-09')){try{const response=await route.fetch();const data=await response.json();data.rows[0].key='stale.test';await new Promise(r=>setTimeout(r,300));await route.fulfill({response,json:data});}catch{}return;}try{await route.continue();}catch{}});
 await page.evaluate(()=>{location.hash='/sites?period=day&start=2026-11-09'});await page.waitForTimeout(50);await page.evaluate(()=>{location.hash='/sites?period=day&start=2026-11-10'});await expect(page.locator('[name=start]')).toHaveValue('2026-11-10');await expect(page.getByRole('heading',{name:'网站排行'})).toBeVisible();await page.waitForTimeout(400);await expect(page.locator('#page')).not.toContainText('stale.test');
});
test('mobile, theme, actual interfaces and system',async({page})=>{
 await page.setViewportSize({width:390,height:844});await page.goto('/#/sites?period=day&start=2026-11-10');await expect(page.getByRole('heading',{name:'网站排行'})).toBeVisible();
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBeTruthy();
 await page.locator('#theme').selectOption('light');await expect(page.locator('html')).toHaveAttribute('data-theme','light');
 await page.goto('/#/hosts/server-a?period=day&start=2026-11-10');await expect(page.locator('[data-iface]')).toHaveValue('ens3');await expect(page.locator('[data-iface] option')).toHaveText(['ens3','ens4']);
 await page.goto('/#/system');await expect(page.getByRole('heading',{name:'接入新主机'})).toBeVisible();await page.screenshot({path:'test-results/mobile-system.png',fullPage:true});
 await page.locator('#theme').selectOption('dark');await page.setViewportSize({width:1440,height:1000});await page.goto('/#/sites/example.com?period=day&start=2026-11-10');await expect(page.getByRole('heading',{name:'客户端 IP 排行'})).toBeVisible();await page.screenshot({path:'test-results/site-detail.png',fullPage:true});
});

test('zero, uncollected and expired remain distinct',async({page})=>{
 await page.goto('/#/ips?period=day&start=2026-11-10&ip=192.0.2.255');await expect(page.getByText('当前筛选暂无流量',{exact:true})).toBeVisible();
 await page.goto('/#/sites/unknown.test?period=day&start=2026-11-10');await expect(page.locator('.notice strong')).toHaveText('尚未采集');
 await page.goto('/#/ips?period=day&start=2025-01-01');await expect(page.getByText('明细已过期',{exact:true})).toBeVisible();
});
