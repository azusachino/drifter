import { expect, test } from '@playwright/test';

test('probe one admin route', async ({ page }, testInfo) => {
  const errors: string[] = [];
  page.on('console', (message) => {
    if (message.type() === 'error') errors.push(message.text());
  });
  page.on('pageerror', (error) => errors.push(error.message));

  await page.goto(process.env.E2E_PROBE_ROUTE || '/admin');
  await expect(page.locator('body')).toHaveText(/\S/, { useInnerText: true, timeout: 15_000 });
  await page.screenshot({ path: testInfo.outputPath('probe.png'), fullPage: true });
  console.log(JSON.stringify({ title: await page.title(), url: page.url(), errors }, null, 2));
  console.log(await page.locator('body').ariaSnapshot());
});
