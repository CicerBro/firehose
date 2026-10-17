import { expect, test, type Request } from '@playwright/test';

// §13.5: load page → IP strip populated → Start → phases progress → tiles non-zero →
// Copy JSON produces a valid §7.5 result → no OPTIONS (preflight) requests at all.

declare global {
  interface Window {
    __copied?: string;
    __image?: string;
  }
}

test('full test run, valid JSON, no preflight requests', async ({ page, context }) => {
  const requests: Request[] = [];
  context.on('request', (r) => requests.push(r));

  // Capture "Copy results" without needing clipboard permissions (works in all three engines).
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: {
        writeText: async (t: string) => {
          window.__copied = t;
        },
        // Share → Image: record the PNG's type and size.
        write: async (items: ClipboardItem[]) => {
          const b = await items[0]!.getType('image/png');
          window.__image = `${b.type}:${b.size}`;
        },
      },
    });
  });

  await page.goto('/?duration=3&grace=0.5');

  // IP strip
  await expect(page.locator('#ip-addr')).toHaveText(/^[0-9a-f:.]+$/i);
  await expect(page.locator('#ip-family')).toHaveText(/^IPv[46]$/);
  await expect(page.locator('#server-info')).not.toBeEmpty();

  // Multi with 4 streams, no packet-loss test (keeps the latency phase at 2 s).
  await page.locator('details.advanced summary').click();
  await page.locator('#streams').selectOption('4');
  const loss = page.locator('#loss');
  if (await loss.isVisible()) await loss.uncheck();

  await page.locator('#start-btn').click();
  await expect(page.locator('#start-text')).toHaveText('Stop');
  const label = page.locator('#gauge-label');
  await expect(label).toHaveText('Latency');
  await expect(label).toHaveText('↓ Download', { timeout: 15_000 });
  await expect(label).toHaveText('↑ Upload', { timeout: 15_000 });
  await expect(label).toHaveText('Done', { timeout: 20_000 });
  await expect(page.locator('#start-text')).toHaveText('Run again');

  // Tiles show non-zero values
  for (const id of ['#tile-down', '#tile-up', '#tile-ping']) {
    const v = page.locator(`${id} .v`);
    await expect(v).not.toHaveText('—');
    expect(Number.parseFloat((await v.textContent()) ?? '')).toBeGreaterThan(0);
  }

  // Copy JSON
  await page.locator('details.copy summary').click();
  await page.locator('button[data-copy="json"]').click();
  await expect.poll(() => page.evaluate(() => window.__copied ?? '')).not.toBe('');
  const json = await page.evaluate(() => window.__copied!);
  const r = JSON.parse(json);
  expect(Object.keys(r)).toEqual(['version', 'timestamp', 'server', 'client', 'config', 'latency', 'download', 'upload', 'loss', 'flags']);
  expect(r.version).toBe(1);
  expect(r.timestamp).toMatch(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/);
  expect(typeof r.server.name).toBe('string');
  expect(r.server.url).toBe(new URL(page.url()).origin);
  expect(['ipv4', 'ipv6']).toContain(r.client.family);
  expect(r.config).toEqual({ connections: 4, durationSec: 3, graceSec: 0.5, loss: false });
  expect(r.latency.idle.samples).toBeGreaterThan(0);
  expect(r.latency.idle.medianMs).toBeGreaterThan(0);
  for (const k of ['medianMs', 'p90Ms', 'samples']) {
    expect(typeof r.latency.download[k]).toBe('number');
    expect(typeof r.latency.upload[k]).toBe('number');
  }
  for (const d of [r.download, r.upload]) {
    expect(d.bps).toBeGreaterThan(0);
    expect(d.p90bps).toBeGreaterThan(0);
    expect(d.bytes).toBeGreaterThan(0);
    expect(d.steadyMs).toBeGreaterThan(0);
    expect(typeof d.short).toBe('boolean');
  }
  expect(typeof r.upload.approx).toBe('boolean');
  expect(Object.keys(r.loss)).toEqual(['available', 'downPct', 'upPct', 'jitterDownMs', 'jitterUpMs', 'sentDown', 'sentUp', 'reorderedDown']);
  expect(Object.keys(r.flags)).toEqual(['tabHidden', 'singleOrigin', 'aborted']);
  expect(r.flags.aborted).toBe(false);

  // Copy Text
  await page.locator('details.copy summary').click();
  await page.locator('button[data-copy="text"]').click();
  await expect.poll(() => page.evaluate(() => window.__copied ?? '')).toContain('Download');

  // Share → Image produces a PNG
  await page.locator('details.copy summary').click();
  await page.locator('button[data-copy="image"]').click();
  await expect.poll(() => page.evaluate(() => window.__image ?? '')).toMatch(/^image\/png:\d{4,}$/);

  // No preflight requests, ever (§5.5). Transfer requests must have gone cross-origin to 127.0.0.1.
  expect(requests.filter((q) => q.method() === 'OPTIONS').map((q) => q.url())).toEqual([]);
  expect(requests.some((q) => q.url().includes('/api/session'))).toBe(true);
  console.log(`[${test.info().project.name}] requests seen: down=${requests.filter((q) => q.url().includes('/api/down')).length} up=${requests.filter((q) => q.url().includes('/api/up')).length}`);
});

test('Stop aborts and resets to idle', async ({ page }) => {
  await page.goto('/?duration=3&grace=0.5');
  await expect(page.locator('#server-info')).not.toBeEmpty();
  await page.locator('#start-btn').click();
  await expect(page.locator('#gauge-label')).toHaveText('Latency');
  await page.locator('#start-btn').click();
  await expect(page.locator('#start-text')).toHaveText('Start');
  await expect(page.locator('#gauge-label')).toHaveText('Ready');
  await expect(page.locator('#tile-down .v')).toHaveText('—');
  await expect(page.locator('#copy-row')).toBeHidden();
});
