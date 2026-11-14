import {test,expect} from './fixtures.js';
test.beforeEach(async ({page}) => { const response = await page.request.post('/api/login', {data:{token:'local-browser-test-token'}}); expect(response.ok()).toBeTruthy(); });
test('date → website → complete IP ranking → detail → back, URL and CSV',async({page})=>{
 const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto('/legacy/');await page.getByRole('link',{name:'网站',exact:true}).click();
 await page.locator('[name=period]').selectOption('day');await page.locator('[name=start]').fill('2026-11-10');await expect(page).toHaveURL(/start=2026-11-10/);
 await page.locator('tbody').getByRole('link',{name:'example.com',exact:true}).click();await expect(page.getByRole('heading',{name:'客户端 IP 排行'})).toBeVisible();
 await expect(page.locator('.pager')).toContainText('137 条');
 await page.getByRole('link',{name:'下一页',exact:true}).click();await expect(page.locator('.pager')).toContainText('第 2 / 3 页');
 const before=page.url();await page.locator('tbody .identity a').first().scrollIntoViewIfNeeded();const scrollBefore=await page.evaluate(()=>window.scrollY);const ip=await page.locator('tbody .identity a').first().textContent();await page.locator('tbody .identity a').first().click();await expect(page.getByRole('heading',{name:ip,exact:true})).toBeVisible();await expect(page.getByRole('heading',{name:'请求状态',exact:true})).toBeVisible();
 await page.goBack();await expect(page).toHaveURL(before);await expect(page.locator('.pager')).toContainText('第 2 / 3 页');await expect.poll(()=>page.evaluate(()=>window.scrollY)).toBe(scrollBefore);
 await page.reload();await expect(page.locator('[name=start]')).toHaveValue('2026-11-10');await expect(page.locator('.pager')).toContainText('第 2 / 3 页');
 const url=await page.getByRole('link',{name:'导出全部 CSV'}).getAttribute('href');const response=await page.request.get(url);expect(response.ok()).toBeTruthy();const csv=await response.text();expect(csv.trim().split('\n')).toHaveLength(138);
 expect(errors).toEqual([]);
});

test('IP geography, traffic details and website drill-down preserve the selected IP',async({page})=>{
 await page.goto('/legacy/#/ips?period=day&start=2026-11-10');
 await expect(page.getByRole('heading',{name:'IP 流量排行'})).toBeVisible();
 await expect(page.getByRole('columnheader',{name:'归属地 / 运营商',exact:true})).toBeVisible();
 await page.getByRole('link',{name:'198.51.100.137',exact:true}).click();
 await expect(page.locator('.ip-profile')).toContainText('中国 · Guangdong · Shenzhen');
 const websites=page.getByRole('region',{name:'网站流量表',exact:true});
 await expect(websites.getByRole('link',{name:'example.com',exact:true})).toBeVisible();
 await expect(websites.getByRole('link',{name:'media.example.com',exact:true})).toBeVisible();
 await expect(page.getByRole('heading',{name:'主机流量分布'})).toBeVisible();
 await expect(page.getByRole('region',{name:'按时段流量明细',exact:true})).toBeVisible();
 await page.locator('[name=status]').fill('200');
 await expect(page).toHaveURL(/status=200/);await expect(page.locator('#page')).not.toHaveAttribute('aria-busy','true');
 await expect(websites.getByRole('link',{name:'media.example.com',exact:true})).toHaveCount(0);
 const detailURL=page.url();
 await websites.getByRole('link',{name:'example.com',exact:true}).click();
 await expect(page).toHaveURL(/ip=198.51.100.137/);await expect(page).toHaveURL(/status=200/);
 await expect(page.getByRole('heading',{name:'客户端 IP 排行'})).toBeVisible();
 await expect(page.locator('.pager')).toContainText('1 条');
 await page.goBack();await expect(page).toHaveURL(detailURL);await expect(page.locator('[name=status]')).toHaveValue('200');
 await page.goto('/legacy/#/ips/2001%3Adb8%3A%3A1?period=day&start=2026-11-10');
 await expect(page.locator('.ip-profile')).toContainText('Frankfurt am Main');
 await expect(page.getByRole('link',{name:'ipv6.example.com',exact:true})).toBeVisible();
});

test('filters apply automatically, validate input and retain focus',async({page})=>{
 const requests=[];page.on('request',r=>{if(r.url().includes('/analytics/rank?'))requests.push(r.url());});
 await page.goto('/legacy/#/sites?period=day&start=2026-11-10');await expect(page.getByRole('heading',{name:'网站排行'})).toBeVisible();
 await expect(page.getByRole('button',{name:'应用筛选',exact:true})).toHaveCount(0);
 await page.locator('[name=gran]').selectOption('hour');await expect(page).toHaveURL(/gran=hour/);
 await expect(page.locator('[name=gran]')).toHaveValue('hour');
 await page.locator('[name=host]').selectOption('server-a');await expect(page).toHaveURL(/host=server-a/);
 await expect(page.locator('#page')).not.toHaveAttribute('aria-busy','true');
 const before=requests.length,search=page.locator('[name=search]');
 await search.pressSequentially('media',{delay:50});
 await expect(page).toHaveURL(/search=media/);await expect(page.locator('.pager')).toContainText('1 条');
 expect(requests.length-before).toBe(1);await expect(search).toBeFocused();
 const validRequests=requests.length;
 await page.locator('[name=ip]').fill('198.51.');
 await expect(page.locator('[data-advanced] [data-filter-error]')).toContainText('完整的 IP');
 expect(requests.length).toBe(validRequests);await expect(page.locator('[name=ip]')).toHaveValue('198.51.');
 await page.getByRole('button',{name:'清除条件'}).click();
 await expect(page.locator('[name=ip]')).toHaveValue('');await expect(page.locator('[data-advanced] [data-filter-error]')).toBeHidden();
 await page.locator('[name=period]').selectOption('custom');
 await page.locator('[name=end]').fill('2026-11-09');
 await expect(page.locator('#filters [data-filter-error]')).toContainText('结束日期不能早于开始日期');
 await page.locator('[name=end]').fill('2026-11-11');await expect(page).toHaveURL(/end=2026-11-11/);
 await page.goto('/legacy/#/ips?period=day&start=2026-11-10&status=200');
 await page.locator('[name=source]').selectOption('singbox');await expect(page).toHaveURL(/source=singbox/);
 await expect(page.locator('[name=status]')).toHaveCount(0);
 await page.getByRole('link',{name:'198.51.100.7',exact:true}).click();
 await expect(page.getByRole('heading',{name:'入站流量详情'})).toBeVisible();
 await expect(page.getByRole('region',{name:'入站流量表',exact:true})).toContainText('inbound-a');
});

test('desktop and mobile controls align and fit in both themes',async({page})=>{
 for(const width of [1973,1440,375,844]){
  await page.setViewportSize({width,height:width===844?390:950});
  await page.goto('/legacy/#/sites?period=day&start=2026-11-10');await expect(page.getByRole('heading',{name:'网站排行'})).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBeTruthy();
  for(const theme of ['light','dark']){
   await page.locator('#theme').selectOption(theme);await expect(page.locator('#page')).not.toHaveAttribute('aria-busy','true');
   const css=await page.locator('#filters select').evaluateAll(elements=>elements.map(el=>({height:el.getBoundingClientRect().height,image:getComputedStyle(el).backgroundImage})));
   expect(new Set(css.map(el=>el.height)).size).toBe(1);expect(css.every(el=>el.image.includes('chevron-down.svg'))).toBeTruthy();
   const alignment=await page.locator('.traffic-table thead .num').evaluateAll(elements=>elements.map(el=>getComputedStyle(el).textAlign));
   expect(alignment.every(v=>v==='right')).toBeTruthy();
   if(width===1440||width===375)await page.screenshot({path:`test-results/sites-${width}-${theme}.png`,fullPage:true});
  }
 }
 await page.emulateMedia({reducedMotion:'reduce'});
 await page.setViewportSize({width:1440,height:1000});await page.goto('/legacy/#/ips/198.51.100.137?period=day&start=2026-11-10');
 await expect(page.getByRole('heading',{name:'网站流量详情'})).toBeVisible();
 const ticks=await page.locator('#trend').evaluate(canvas=>window.Chart.getChart(canvas).scales.x.ticks.map(t=>t.value));
 expect(ticks.length).toBeGreaterThan(1);expect(ticks.every(t=>t%3600000===0)).toBeTruthy();
 await page.screenshot({path:'test-results/ip-detail.png',fullPage:true});
});
test('abort obsolete requests and retry errors',async({page})=>{
 let failed=false;
 await page.route('**/api/v2/analytics/rank?**',async route=>{if(!failed){failed=true;return route.fulfill({status:503,contentType:'application/json',body:'{"error":"临时测试错误"}'});}return route.continue();});
 await page.goto('/legacy/#/sites?period=day&start=2026-11-10');await expect(page.getByText('临时测试错误')).toBeVisible();await page.getByRole('button',{name:'重试',exact:true}).click();await expect(page.getByRole('heading',{name:'网站排行'})).toBeVisible();
 await page.route('**/api/v2/analytics/rank?**',async route=>{if(route.request().url().includes('2026-11-09')){try{const response=await route.fetch();const data=await response.json();data.rows[0].key='stale.test';await new Promise(r=>setTimeout(r,300));await route.fulfill({response,json:data});}catch{}return;}try{await route.continue();}catch{}});
 await page.evaluate(()=>{location.hash='/sites?period=day&start=2026-11-09'});await page.waitForTimeout(50);await page.evaluate(()=>{location.hash='/sites?period=day&start=2026-11-10'});await expect(page.locator('[name=start]')).toHaveValue('2026-11-10');await expect(page.getByRole('heading',{name:'网站排行'})).toBeVisible();await page.waitForTimeout(400);await expect(page.locator('#page')).not.toContainText('stale.test');
});
test('mobile, theme, actual interfaces and system',async({page})=>{
 await page.setViewportSize({width:390,height:844});await page.goto('/legacy/#/sites?period=day&start=2026-11-10');await expect(page.getByRole('heading',{name:'网站排行'})).toBeVisible();
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBeTruthy();
 await page.locator('#theme').selectOption('light');await expect(page.locator('html')).toHaveAttribute('data-theme','light');
 await page.goto('/legacy/#/hosts/server-a?period=day&start=2026-11-10');await expect(page.locator('[data-iface]')).toHaveValue('ens3');await expect(page.locator('[data-iface] option')).toHaveText(['ens3','ens4']);
 await page.goto('/legacy/#/system');await expect(page.getByRole('heading',{name:'接入新主机'})).toBeVisible();await page.screenshot({path:'test-results/mobile-system.png',fullPage:true});
 await page.locator('#theme').selectOption('dark');await page.setViewportSize({width:1440,height:1000});await page.goto('/legacy/#/sites/example.com?period=day&start=2026-11-10');await expect(page.getByRole('heading',{name:'客户端 IP 排行'})).toBeVisible();await page.screenshot({path:'test-results/site-detail.png',fullPage:true});
});

test('zero, uncollected and expired remain distinct',async({page})=>{
 await page.goto('/legacy/#/ips?period=day&start=2026-11-10&ip=192.0.2.255');await expect(page.getByText('当前筛选暂无流量',{exact:true})).toBeVisible();
 await page.goto('/legacy/#/sites/unknown.test?period=day&start=2026-11-10');await expect(page.locator('.notice strong')).toHaveText('尚未采集');
 await page.goto('/legacy/#/ips?period=day&start=2025-01-01');await expect(page.getByText('明细已过期',{exact:true})).toBeVisible();
});

test('host filter controls list and server online state; detail route takes precedence',async({page})=>{
 await page.goto('/legacy/#/hosts?host=all');await expect(page.locator('.host-panel')).toHaveCount(3);
 await expect(page.locator('.host-panel .section-title .badge')).toHaveText(['离线','在线','在线']);
 await page.locator('[name=host]').selectOption('server-a');await expect(page.locator('.host-panel')).toHaveCount(1);
 await expect(page.locator('.host-panel > .section-title h2')).toHaveText('server-a');await page.reload();await expect(page.locator('.host-panel')).toHaveCount(1);
 await page.goto('/legacy/#/hosts/server-b?host=server-a');await expect(page.locator('.host-panel > .section-title h2')).toHaveText('server-b');
 await page.goto('/legacy/#/hosts?host=missing');await expect(page.getByRole('heading',{name:'当前筛选没有主机'})).toBeVisible();
});
