// Headless Playwright E2E that drives the REAL UniHV UI in a browser and reports,
// per view/action, what works and what breaks. Run inside the official Playwright
// container against the live app (host.docker.internal:8080).
//
// It is intentionally a single self-contained script (not the full Playwright test
// runner) so it can run with `node` + the Playwright lib in the mcr image and emit
// a clear PASS/FAIL table + screenshots to /work/test/e2e/shots.

import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const BASE = process.env.BASE_URL || 'http://host.docker.internal:8080';
const USER = process.env.UNIHV_USER || 'admin';
const PASS = process.env.UNIHV_PASS || 'Admin1234567';
const SHOTS = '/work/test/e2e/shots';
mkdirSync(SHOTS, { recursive: true });

const results = [];
function rec(name, ok, detail = '') { results.push({ name, ok, detail }); console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  :: ' + detail : ''}`); }

const browser = await chromium.launch({ args: ['--no-sandbox'] });
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, ignoreHTTPSErrors: true });
const page = await ctx.newPage();

const consoleErrors = [];
page.on('console', m => { if (m.type() === 'error') consoleErrors.push(m.text()); });
page.on('pageerror', e => consoleErrors.push('pageerror: ' + e.message));

async function shot(name) { try { await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: true }); } catch {} }
async function goto(path) { await page.goto(BASE + path, { waitUntil: 'networkidle', timeout: 20000 }); }

try {
  // --- login ---
  await goto('/login');
  // Fill username/password — try common selectors.
  const u = page.locator('input[type="text"], input[name="username"], input[autocomplete="username"]').first();
  const p = page.locator('input[type="password"]').first();
  await u.fill(USER); await p.fill(PASS);
  await Promise.all([
    page.waitForLoadState('networkidle'),
    page.locator('button[type="submit"], button:has-text("Sign in"), button:has-text("Log in"), button:has-text("Login")').first().click(),
  ]);
  await page.waitForTimeout(1500);
  const loggedIn = !page.url().includes('/login');
  rec('login', loggedIn, page.url());
  await shot('01-after-login');

  // --- Virtual Machines list ---
  await goto('/vms');
  await page.waitForTimeout(1500);
  const bodyText = await page.locator('body').innerText();
  const hasReal = /web-server-01|db-server-01/.test(bodyText);
  const hasSim = /sim-vm-/.test(bodyText);
  rec('vms list shows real VMs', hasReal, hasReal ? 'web/db-server present' : 'no real VM rows');
  rec('vms list has NO sim/mock', !hasSim, hasSim ? 'sim-vm-* STILL PRESENT' : 'clean');
  await shot('02-vms');

  // Count VM rows
  const rowCount = await page.locator('table tbody tr, [role="row"]').count().catch(() => 0);
  rec('vms list rows', rowCount > 0, `${rowCount} rows`);

  // --- VM detail + Console tab ---
  // Click the web-server-01 row/link.
  const vmLink = page.locator('text=web-server-01').first();
  if (await vmLink.count()) {
    await vmLink.click().catch(() => {});
    await page.waitForTimeout(1500);
    rec('open VM detail', /web-server-01/.test(await page.locator('body').innerText()), page.url());
    await shot('03-vm-detail');

    // Console tab
    const consoleTab = page.locator('button:has-text("Console"), .tab:has-text("Console"), [role="tab"]:has-text("Console")').first();
    if (await consoleTab.count()) {
      await consoleTab.click().catch(() => {});
      await page.waitForTimeout(4000); // let guacamole connect
      await shot('04-console-tab');
      // Look for a canvas (guacamole renders into a canvas) or a connected status.
      const hasCanvas = await page.locator('canvas').count();
      const panelText = await page.locator('body').innerText();
      const connected = hasCanvas > 0 || /connected/i.test(panelText);
      rec('console tab renders canvas/connected', connected, hasCanvas ? `${hasCanvas} canvas` : panelText.slice(0, 120));
    } else {
      rec('console tab present', false, 'no Console tab found');
    }
  } else {
    rec('open VM detail', false, 'web-server-01 link not found');
  }

  // --- other VM views reachable ---
  for (const [label, path, marker] of [
    ['Connections', '/vm/connections', /WSL KVM|connection/i],
    ['VM Networks', '/vm-networks', /network/i],
    ['VM Storage', '/vm-storage', /storage|pool|volume/i],
    ['Clusters', '/vm-clusters', /cluster/i],
    ['Migration', '/migration', /migrat/i],
  ]) {
    try {
      await goto(path);
      await page.waitForTimeout(1200);
      const t = await page.locator('body').innerText();
      const notFound = /not found|404/i.test(t) && t.length < 400;
      rec(`view ${label} loads`, !notFound && marker.test(t), notFound ? '404' : (marker.test(t) ? 'ok' : 'marker missing'));
      await shot('view-' + label.replace(/\W+/g, '_'));
    } catch (e) {
      rec(`view ${label} loads`, false, String(e).slice(0, 100));
    }
  }

  // --- Create VM wizard opens ---
  await goto('/vms');
  await page.waitForTimeout(1000);
  const createBtn = page.locator('button:has-text("Create VM"), a:has-text("Create VM")').first();
  if (await createBtn.count()) {
    await createBtn.click().catch(() => {});
    await page.waitForTimeout(1200);
    const t = await page.locator('body').innerText();
    rec('Create VM wizard opens', /create|name|vcpu|memory|step/i.test(t), page.url());
    await shot('05-create-wizard');
  } else {
    rec('Create VM button present', false, 'no Create VM button');
  }

} catch (e) {
  rec('FATAL', false, String(e).slice(0, 200));
} finally {
  // Summary
  console.log('\n===== SUMMARY =====');
  const pass = results.filter(r => r.ok).length;
  console.log(`${pass}/${results.length} checks passed`);
  if (consoleErrors.length) {
    console.log('\n--- browser console errors (first 15) ---');
    consoleErrors.slice(0, 15).forEach(e => console.log('  ' + e.slice(0, 200)));
  }
  await browser.close();
  process.exit(results.some(r => !r.ok) ? 1 : 0);
}
