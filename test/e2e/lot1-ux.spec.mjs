// Lot 1 — UX clarity screenshots: labeled action bar, Actions ▾ menu open,
// rich Summary (gauges), and the Recent Tasks bottom bar.
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const BASE = process.env.BASE_URL || 'http://host.docker.internal:5173';
const USER = process.env.UNIHV_USER || 'admin';
const PASS = process.env.UNIHV_PASS || 'Admin1234567';
const SHOTS = '/work/test/e2e/shots';
mkdirSync(SHOTS, { recursive: true });

const log = (m) => console.log(m);
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 } });
const page = await ctx.newPage();
const shot = (n) => page.screenshot({ path: `${SHOTS}/${n}.png`, fullPage: false }).catch(() => {});
const goto = (p) => page.goto(BASE + p, { waitUntil: 'domcontentloaded', timeout: 25000 });

try {
  await goto('/login');
  await page.locator('input[type="text"], input[name="username"]').first().fill(USER);
  await page.locator('input[type="password"]').first().fill(PASS);
  await page.locator('button[type="submit"]').first().click();
  await page.waitForTimeout(2000);
  log('login -> ' + page.url());

  // Open the VM list (compact Actions ▾ per row) and screenshot the Recent Tasks bar.
  await goto('/vms');
  await page.waitForTimeout(2500);
  await shot('lot1-00-vmlist-tasksbar');

  // Open the running web-server-01 (falls back to first row).
  let row = page.locator('tr', { hasText: 'web-server-01' }).first();
  if (!(await row.count())) row = page.locator('tbody tr').first();
  await row.click();
  await page.waitForTimeout(2500);
  await shot('lot1-01-detail-actionbar-summary');

  // Open the Actions ▾ menu, then hover a submenu group (Power).
  const actionsBtn = page.locator('button[aria-label="Actions"]').first();
  if (await actionsBtn.count()) {
    await actionsBtn.click();
    await page.waitForTimeout(500);
    await shot('lot1-02-actions-menu-open');
    const powerGroup = page.locator('.menu-item.has-sub', { hasText: 'Power' }).first();
    if (await powerGroup.count()) {
      await powerGroup.hover();
      await page.waitForTimeout(400);
      await shot('lot1-03-actions-power-submenu');
    }
    await page.keyboard.press('Escape').catch(() => {});
    await page.mouse.click(10, 300);
  } else {
    log('WARN: Actions button not found');
  }
  await page.waitForTimeout(300);

  // Summary tab full view (gauges + cards).
  const summaryTab = page.locator('button.tab', { hasText: 'Summary' }).first();
  if (await summaryTab.count()) { await summaryTab.click(); await page.waitForTimeout(800); }
  await shot('lot1-04-summary-gauges');

  // Expand and screenshot the Recent Tasks bar specifically (click its tab).
  const tasksTab = page.locator('.tasksbar-tab', { hasText: 'Recent Tasks' }).first();
  if (await tasksTab.count()) {
    await tasksTab.click();
    await page.waitForTimeout(800);
    await shot('lot1-05-recent-tasks-bar');
  }
  // Alarms tab.
  const alarmsTab = page.locator('.tasksbar-tab', { hasText: 'Alarms' }).first();
  if (await alarmsTab.count()) {
    await alarmsTab.click();
    await page.waitForTimeout(600);
    await shot('lot1-06-alarms-tab');
  }

  log('done');
} catch (e) {
  log('FATAL ' + String(e).slice(0, 300));
} finally {
  await browser.close();
  process.exit(0);
}
