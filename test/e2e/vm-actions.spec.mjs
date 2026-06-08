// Deeper E2E: actually CLICK real action buttons and confirm effects + capture the
// network requests (esp. any 401/4xx/5xx) so we know which buttons truly work.
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const BASE = process.env.BASE_URL || 'http://host.docker.internal:8080';
const USER = process.env.UNIHV_USER || 'admin';
const PASS = process.env.UNIHV_PASS || 'Admin1234567';
const SHOTS = '/work/test/e2e/shots';
mkdirSync(SHOTS, { recursive: true });

const results = [];
const rec = (n, ok, d = '') => { results.push({ n, ok, d }); console.log(`${ok ? 'PASS' : 'FAIL'}  ${n}${d ? '  :: ' + d : ''}`); };

const browser = await chromium.launch({ args: ['--no-sandbox'] });
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 } });
const page = await ctx.newPage();

// Track ALL failed network responses with their URLs.
const failedReqs = [];
page.on('response', r => { if (r.status() >= 400) failedReqs.push(`${r.status()} ${r.request().method()} ${r.url().replace(BASE, '')}`); });

const shot = n => page.screenshot({ path: `${SHOTS}/${n}.png`, fullPage: true }).catch(() => {});
const goto = p => page.goto(BASE + p, { waitUntil: 'networkidle', timeout: 20000 });

try {
  await goto('/login');
  await page.locator('input[type="text"], input[name="username"]').first().fill(USER);
  await page.locator('input[type="password"]').first().fill(PASS);
  await page.locator('button[type="submit"]').first().click();
  await page.waitForTimeout(1500);
  rec('login', !page.url().includes('/login'));

  // Capture failures during a clean dashboard load (to find the 401 source).
  failedReqs.length = 0;
  await goto('/');
  await page.waitForTimeout(1500);
  rec('dashboard load no 4xx/5xx', failedReqs.length === 0, failedReqs.join(' | ') || 'clean');

  // --- POWER action on db-server-01 (it's stopped -> Start) ---
  await goto('/vms');
  await page.waitForTimeout(1500);
  failedReqs.length = 0;
  // Find the db-server-01 row and click its start (play) action button.
  const row = page.locator('tr', { hasText: 'db-server-01' }).first();
  let powerClicked = false;
  if (await row.count()) {
    // action buttons are icon buttons in the row; try title/aria first, else first button.
    const startBtn = row.locator('button[title*="Start" i], button[aria-label*="start" i], button:has-text("Start")').first();
    if (await startBtn.count()) { await startBtn.click().catch(() => {}); powerClicked = true; }
    else {
      const btns = row.locator('button');
      if (await btns.count()) { await btns.first().click().catch(() => {}); powerClicked = true; }
    }
  }
  await page.waitForTimeout(2500);
  await shot('act-01-power');
  rec('power button clickable', powerClicked, powerClicked ? `net: ${failedReqs.join(',') || 'ok'}` : 'no start button found');

  // --- CREATE NETWORK via the VM Networks form ---
  await goto('/vm-networks');
  await page.waitForTimeout(1500);
  failedReqs.length = 0;
  const addNet = page.locator('button:has-text("Create network"), button:has-text("Create"), button:has-text("Add")').first();
  let netCreated = false, netErr = '';
  if (await addNet.count()) {
    await addNet.click().catch(() => {});
    await page.waitForTimeout(800);
    // Fill the MODAL's Name field specifically (the dialog scopes the input, avoiding
    // the page search box). The modal title is "Create virtual network".
    const dialog = page.locator('div:has-text("Create virtual network")').last();
    const nameInput = page.locator('input').filter({ hasNot: page.locator('[placeholder*="Search" i]') }).nth(0);
    // More robust: the Name field is the input right under the "Name" label in the modal.
    const modalName = page.locator('text=Name').locator('xpath=following::input[1]');
    const target = (await modalName.count()) ? modalName.first() : nameInput;
    await target.fill('e2e-ui-net');
    await shot('act-02-net-form');
    const submit = page.locator('button:has-text("Create"), button:has-text("Save")').last();
    await submit.click().catch(() => {});
    await page.waitForTimeout(2500);
    const t = await page.locator('body').innerText();
    netCreated = /e2e-ui-net/.test(t) && !failedReqs.some(f => f.startsWith('5'));
    netErr = failedReqs.join(' | ');
  }
  await shot('act-03-net-result');
  rec('create network via UI', netCreated, netCreated ? 'e2e-ui-net visible' : ('fails: ' + netErr));

  // cleanup the test net through the UI if present (best-effort), else leave for script cleanup.

  console.log('\n--- ALL failed requests seen this run ---');
  [...new Set(failedReqs)].forEach(f => console.log('  ' + f));

} catch (e) {
  rec('FATAL', false, String(e).slice(0, 200));
} finally {
  console.log('\n===== SUMMARY =====');
  console.log(`${results.filter(r => r.ok).length}/${results.length} passed`);
  await browser.close();
  process.exit(0);
}
