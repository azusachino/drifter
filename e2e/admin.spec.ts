import { expect, test } from '@playwright/test';

test('admin confirms, replaces and unlinks mappings only after explicit form actions', async ({ page, request }) => {
  const providerRequests: string[] = [];
  page.on('request', (requestEvent) => {
    if (requestEvent.url().includes('/api/pending')) providerRequests.push(requestEvent.url());
  });
  await page.goto('/admin');
  await expect(page.getByRole('heading', { name: 'Drifter administration' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'View mappings as JSON' })).toBeVisible();
  await expect(page.getByRole('form', { name: 'Confirm a mapping' })).toBeVisible();
  expect(providerRequests).toEqual([]);

  const confirm = page.getByRole('form', { name: 'Confirm a mapping' });
  await confirm.getByLabel('Source account').fill('synthetic-source');
  await confirm.getByLabel('Source book ID').fill('123');
  await confirm.getByLabel('Target account').fill('synthetic-target');
  await confirm.getByLabel('Target work ID').fill('77');
  await confirm.getByRole('radio', { name: 'Work only' }).check();
  await confirm.getByRole('button', { name: 'Confirm mapping' }).click();
  await expect(page.getByRole('status')).toHaveText('Mapping confirmed.');
  await expect(page.getByRole('listitem', { name: 'Mapping for book 123' })).toContainText('work 77, work only');

  let response = await request.get(`${process.env.E2E_BASE_URL}/api/mappings`);
  expect(response.ok()).toBeTruthy();
  let mappings = await response.json();
  expect(mappings).toHaveLength(1);
  expect(mappings[0]).toMatchObject({ book_id: '123', work_id: 77 });
  expect(mappings[0]).not.toHaveProperty('edition_id');

  await page.getByText('Replace mapping for book 123').click();
  const replace = page.getByRole('form', { name: 'Replace mapping for book 123' });
  await replace.getByLabel('New target work ID').fill('88');
  await replace.getByRole('radio', { name: 'Specific edition' }).check();
  await replace.getByLabel('Edition ID (required for a specific edition)').fill('89');
  await replace.getByRole('button', { name: 'Replace mapping' }).click();
  await expect(page.getByRole('status')).toHaveText('Mapping replaced.');
  await expect(page.getByRole('listitem', { name: 'Mapping for book 123' })).toContainText('work 88, edition 89');

  response = await request.get(`${process.env.E2E_BASE_URL}/api/mappings`);
  mappings = await response.json();
  expect(mappings).toHaveLength(1);
  expect(mappings[0]).toMatchObject({ book_id: '123', work_id: 88, edition_id: 89 });

  await page.getByText('Unlink mapping for book 123').click();
  await page.getByRole('button', { name: 'Unlink this mapping' }).click();
  await expect(page.getByRole('status')).toHaveText('Mapping unlinked.');
  await expect(page.getByText('No mappings are saved.')).toBeVisible();
  response = await request.get(`${process.env.E2E_BASE_URL}/api/mappings`);
  expect(await response.json()).toEqual([]);
  expect(providerRequests).toEqual([]);
});

test('admin remains usable at a narrow viewport', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/admin');
  await expect(page.getByRole('button', { name: 'Confirm mapping' })).toBeVisible();
  const width = await page.locator('html').evaluate((element) => element.scrollWidth);
  expect(width).toBe(390);
});
