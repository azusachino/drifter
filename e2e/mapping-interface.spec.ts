import { expect, test } from '@playwright/test';

test('mapping interface confirms, replaces and unlinks explicit mappings', async ({ page, request }) => {
  const providerRequests: string[] = [];
  page.on('request', (requestEvent) => {
    if (requestEvent.url().includes('/api/pending')) providerRequests.push(requestEvent.url());
  });
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Drifter mapping interface' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'View mappings as JSON' })).toBeVisible();
  await page.getByText('Link New Book').click();
  await expect(page.getByRole('form', { name: 'Confirm a mapping' })).toBeVisible();
  expect(providerRequests).toEqual([]);

  const confirm = page.getByRole('form', { name: 'Confirm a mapping' });
  await confirm.getByLabel('Source book ID').fill('123');
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

test('native mapping form submits with same-origin Origin and no page script', async ({ page }) => {
  const scriptRequests: string[] = [];
  page.on('request', (requestEvent) => {
    if (requestEvent.resourceType() === 'script') scriptRequests.push(requestEvent.url());
  });
  await page.goto('/');
  await page.getByText('Link New Book').click();
  const form = page.getByRole('form', { name: 'Confirm a mapping' });
  await form.getByLabel('Source book ID').fill('124');
  await form.getByLabel('Target work ID').fill('78');
  await form.getByRole('radio', { name: 'Work only' }).check();
  const [response] = await Promise.all([
    page.waitForResponse((candidate) => candidate.request().method() === 'POST' && candidate.url().includes('/mappings/confirm')),
    form.getByRole('button', { name: 'Confirm mapping' }).click(),
  ]);
  expect(response.status()).toBe(303);
  expect(response.request().headers().origin).toBe(new URL(process.env.E2E_BASE_URL!).origin);
  expect(scriptRequests).toEqual([]);
});

test('stale browser tabs cannot replace or unlink a changed mapping', async ({ page, context, request }) => {
  await page.goto('/');
  await page.getByText('Link New Book').click();
  const confirm = page.getByRole('form', { name: 'Confirm a mapping' });
  await confirm.getByLabel('Source book ID').fill('125');
  await confirm.getByLabel('Target work ID').fill('77');
  await confirm.getByRole('radio', { name: 'Work only' }).check();
  await confirm.getByRole('button', { name: 'Confirm mapping' }).click();
  await expect(page.getByRole('status')).toHaveText('Mapping confirmed.');

  const stale = await context.newPage();
  const staleUnlink = await context.newPage();
  await Promise.all([stale.goto('/'), staleUnlink.goto('/')]);
  await page.getByText('Replace mapping for book 125').click();
  const currentReplace = page.getByRole('form', { name: 'Replace mapping for book 125' });
  await currentReplace.getByLabel('New target work ID').fill('88');
  await currentReplace.getByRole('radio', { name: 'Work only' }).check();
  await currentReplace.getByRole('button', { name: 'Replace mapping' }).click();
  await expect(page.getByRole('status')).toHaveText('Mapping replaced.');

  await stale.getByText('Replace mapping for book 125').click();
  const staleReplace = stale.getByRole('form', { name: 'Replace mapping for book 125' });
  await staleReplace.getByLabel('New target work ID').fill('99');
  await staleReplace.getByRole('radio', { name: 'Work only' }).check();
  await staleReplace.getByRole('button', { name: 'Replace mapping' }).click();
  await expect(stale.getByText(/mapping changed since this page was loaded/i)).toBeVisible();
  let response = await request.get(`${process.env.E2E_BASE_URL}/api/mappings`);
  expect((await response.json()).find((mapping: { book_id: string }) => mapping.book_id === '125')).toMatchObject({ book_id: '125', work_id: 88 });

  await staleUnlink.getByText('Unlink mapping for book 125').click();
  await staleUnlink.getByRole('button', { name: 'Unlink this mapping' }).click();
  await expect(staleUnlink.getByText(/mapping changed since this page was loaded/i)).toBeVisible();
  response = await request.get(`${process.env.E2E_BASE_URL}/api/mappings`);
  expect((await response.json()).find((mapping: { book_id: string }) => mapping.book_id === '125')).toMatchObject({ book_id: '125', work_id: 88 });
});

test('mapping interface remains usable at a narrow viewport', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/');
  await page.getByText('Link New Book').click();
  await expect(page.getByRole('button', { name: 'Confirm mapping' })).toBeVisible();
  const width = await page.locator('html').evaluate((element) => element.scrollWidth);
  expect(width).toBe(390);
});

test('sync jobs panel submits sync recent and lifetime forms and cancels active jobs', async ({ page, request }) => {
  const scriptRequests: string[] = [];
  page.on('request', (requestEvent) => {
    if (requestEvent.resourceType() === 'script') scriptRequests.push(requestEvent.url());
  });

  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Drifter mapping interface' })).toBeVisible();

  // Navigate to Sync & Health tab
  await page.getByText('Sync & Health').click();
  await expect(page.getByRole('heading', { name: 'Trigger background sync' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Sync jobs' })).toBeVisible();

  // Form 1: Trigger Sync Recent (7 days)
  const recentForm = page.getByRole('form', { name: 'Sync Recent' });
  await expect(recentForm).toBeVisible();
  const [recentResponse] = await Promise.all([
    page.waitForResponse((res) => res.request().method() === 'POST' && res.url().includes('/jobs/sync')),
    recentForm.getByRole('button', { name: 'Sync Recent (7 days)' }).click(),
  ]);
  expect(recentResponse.status()).toBe(303);
  expect(recentResponse.request().headers().origin).toBe(new URL(process.env.E2E_BASE_URL!).origin);
  await expect(page.getByRole('status')).toHaveText(/Background sync job started|Matching sync job already queued/);

  // Switch back to Sync & Health view to inspect the job table
  await page.getByText('Sync & Health').click();
  const jobsTable = page.getByRole('table');
  await expect(jobsTable).toBeVisible();
  const firstRow = page.getByRole('listitem', { name: 'Sync job 1' });
  await expect(firstRow).toBeVisible();
  await expect(firstRow).toContainText('recent');
  await expect(firstRow).toContainText('match_and_reconcile');

  // Verify zero scripts executed
  expect(scriptRequests).toEqual([]);

  // Form 2: Trigger Sync Lifetime with finished checkbox
  const lifetimeForm = page.getByRole('form', { name: 'Sync Lifetime' });
  await expect(lifetimeForm).toBeVisible();
  await lifetimeForm.getByLabel('Finished only').check();
  const [lifetimeResponse] = await Promise.all([
    page.waitForResponse((res) => res.request().method() === 'POST' && res.url().includes('/jobs/sync')),
    lifetimeForm.getByRole('button', { name: 'Sync Lifetime' }).click(),
  ]);
  expect(lifetimeResponse.status()).toBe(303);
  await expect(page.getByRole('status')).toHaveText(/Background sync job started|Matching sync job already queued/);

  // Switch back to Sync & Health view
  await page.getByText('Sync & Health').click();
  const secondRow = page.getByRole('listitem', { name: 'Sync job 2' });
  await expect(secondRow).toBeVisible();
  await expect(secondRow).toContainText('lifetime');

  // Cancel form: Cancel an active or queued job if cancel button is present
  const cancelBtn = page.getByRole('button', { name: 'Cancel' }).first();
  if (await cancelBtn.isVisible()) {
    const [cancelResponse] = await Promise.all([
      page.waitForResponse((res) => res.request().method() === 'POST' && res.url().includes('/jobs/cancel')),
      cancelBtn.click(),
    ]);
    expect(cancelResponse.status()).toBe(303);
    await expect(page.getByRole('status')).toHaveText('Sync job cancelled.');
  }

  // Double check status JSON reflects the latest sync job
  const statusRes = await request.get(`${process.env.E2E_BASE_URL}/api/status`);
  expect(statusRes.ok()).toBeTruthy();
  const statusData = await statusRes.json();
  expect(statusData.latest_job).not.toBeNull();
  expect(statusData.latest_job.scope).toBe('lifetime');
});
