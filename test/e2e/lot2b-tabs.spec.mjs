// Lot 2B — VM detail tabs: Monitor (perf charts + events), Configure (hardware +
// Edit drawer), Permissions (effective role bindings). Logs in, opens
// web-server-01, exercises each tab and saves shots/lot2b-*.png.
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const BASE = process.env.BASE_URL || 'http://host.docker.internal:8080';
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
const clickTab = async (name) => {
  const t = page.locator('button.tab', { hasText: name }).first();
  if (await t.count()) { await t.click(); await page.waitForTimeout(900); return true; }
  log('WARN: tab not found: ' + name);
  return false;
};

try {
  await goto('/login');
  await page.locator('input[type="text"], input[name="username"]').first().fill(USER);
  await page.locator('input[type="password"]').first().fill(PASS);
  await page.locator('button[type="submit"]').first().click();
  await page.waitForTimeout(2000);
  log('login -> ' + page.url());

  await goto('/vms');
  await page.waitForTimeout(2500);

  let row = page.locator('tr', { hasText: 'web-server-01' }).first();
  if (!(await row.count())) row = page.locator('tbody tr').first();
  await row.click();
  await page.waitForTimeout(2500);
  log('opened VM detail -> ' + page.url());

  // Tab bar with the canonical order.
  await shot('lot2b-00-tabbar');

  // Monitor: perf charts + events.
  if (await clickTab('Monitor')) {
    await page.waitForTimeout(1200);
    await shot('lot2b-01-monitor');
    // Try a time-range button (1h) to prove the selector wires up.
    const range = page.locator('button', { hasText: /^1h$/ }).first();
    if (await range.count()) { await range.click(); await page.waitForTimeout(800); await shot('lot2b-02-monitor-range'); }
  }

  // Configure: hardware view + Edit opens drawer.
  if (await clickTab('Configure')) {
    await shot('lot2b-03-configure');
    const editBtn = page.locator('button', { hasText: 'Edit Settings' }).first();
    if (await editBtn.count()) {
      await editBtn.click();
      await page.waitForTimeout(900);
      await shot('lot2b-04-configure-edit-drawer');
      await page.keyboard.press('Escape').catch(() => {});
      await page.waitForTimeout(400);
    } else {
      log('WARN: Edit Settings button not found on Configure');
    }
  }

  // Permissions: effective bindings table.
  if (await clickTab('Permissions')) {
    await shot('lot2b-05-permissions');
  }

  log('done');
} catch (e) {
  log('FATAL ' + String(e).slice(0, 400));
} finally {
  await browser.close();
  process.exit(0);
}
