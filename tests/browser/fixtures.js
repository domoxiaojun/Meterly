import { test as base, expect } from '@playwright/test';
import { createHash } from 'node:crypto';

// Each test represents a distinct client, so the real login limiter remains enabled.
// Only the loopback test server trusts this forwarding header.
export const test = base.extend({
  page: async ({ page }, use, info) => {
    const id = createHash('sha256').update(info.testId).digest('hex');
    await page.setExtraHTTPHeaders({ 'X-Forwarded-For': `2001:db8:${id.slice(0, 4)}:${id.slice(4, 8)}::1` });
    await use(page);
  },
});
export { expect };
