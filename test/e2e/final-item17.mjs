import { chromium } from 'playwright';
const BASE = 'http://host.docker.internal:8080';
const SHOTS = '/work/test/e2e/shots';
const b = await chromium.launch({ args: ['--no-sandbox'] });
const page = await (await b.newContext({ viewport: { width: 1500, height: 950 } })).newPage();
const apiCalls = [];
page.on('response', (r) => {
  if (r.url().includes('/disks') && r.request().method() === 'POST')
    apiCalls.push(`${r.status()} ${r.url().split('/api/v1')[1]}`);
});

await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input#username');
await page.fill('input#username', 'admin');
await page.fill('input#password', 'Admin1234567');
await page.locator('button[type="submit"]').first().click();
await page.waitForTimeout(2500);

// open web-server-01
await page.goto(BASE + '/vms', { waitUntil: 'domcontentloaded' });
await page.waitForTimeout(1500);
await page.locator('text=web-server-01').first().click();
await page.waitForTimeout(2000);

// find Add disk trigger: an Actions menu or a button/tab. Try Actions menu first.
async function clickByText(re) {
  const els = await page.$$('button, a, [role="menuitem"], [role="tab"]');
  for (const el of els) {
    const t = ((await el.innerText().catch(() => '')) || '').trim();
    if (re.test(t)) { await el.click().catch(() => {}); return t; }
  }
  return null;
}
// open Actions dropdown
await page.locator('button[aria-label="Actions"], button:has-text("Actions")').first().click();
await page.waitForTimeout(700);
// hover Storage submenu (opens on mouseenter)
const storage = page.locator('button.menu-item.has-sub:has-text("Storage")').first();
await storage.hover();
await page.waitForTimeout(500);
let clicked = 'Add Disk';
await page.locator('button:has-text("Add Disk")').first().click();
await page.waitForTimeout(1300);

// Inspect the drawer for Provisioning select + TRIM checkbox
const hasProvisioning = await page.locator('text=Provisioning').count();
const hasThin = await page.locator('option', { hasText: /Thin/i }).count();
const hasThick = await page.locator('option', { hasText: /Thick/i }).count();
const hasTrim = await page.locator('text=/TRIM|discard/i').count();
console.log(`DRAWER: provisioningLabel=${hasProvisioning} thinOpt=${hasThin} thickOpt=${hasThick} trimText=${hasTrim}`);
await page.screenshot({ path: `${SHOTS}/final-item17-add-disk-drawer.png`, fullPage: false });

// Submit thick + discard via the UI
try {
  // set capacity to 1
  const cap = page.locator('input[type="number"]').first();
  await cap.fill('1').catch(() => {});
  // provisioning -> thick
  const sel = page.locator('select').filter({ has: page.locator('option', { hasText: /Thick/i }) }).first();
  await sel.selectOption('thick').catch(async () => {
    // fallback: pick the select that has thin/thick
    const selects = await page.$$('select');
    for (const s of selects) {
      const opts = await s.$$eval('option', (o) => o.map((x) => x.value));
      if (opts.includes('thick')) { await s.selectOption('thick'); break; }
    }
  });
  // discard checkbox
  await page.locator('input[type="checkbox"]').first().check().catch(() => {});
  await page.waitForTimeout(400);
  await page.screenshot({ path: `${SHOTS}/final-item17-filled.png`, fullPage: false });
  // submit
  await clickByText(/Attach disk|Add disk|Attach|Confirm|Submit/i);
  await page.waitForTimeout(2500);
} catch (e) {
  console.log('SUBMIT-ERR ' + String(e).slice(0, 120));
}
await page.screenshot({ path: `${SHOTS}/final-item17-after-submit.png`, fullPage: false });
console.log('DISK POST CALLS: ' + (apiCalls.join(' | ') || 'none captured via UI'));
await b.close();
process.exit(0);
