// QA follow-up: wizard (11), correct-route views (13/16/21), reconfigure thin/thick (17), 401 origin.
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';
const BASE = process.env.BASE_URL || 'http://host.docker.internal:8080';
const SHOTS = '/work/test/e2e/shots';
mkdirSync(SHOTS, { recursive: true });
const log = (m) => console.log(m);
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const ctx = await browser.newContext({ viewport: { width: 1680, height: 1050 } });
const page = await ctx.newPage();
const shot = (n) => page.screenshot({ path: `${SHOTS}/qa-${n}.png`, fullPage: false }).catch(()=>{});
const goto = (p) => page.goto(BASE + p, { waitUntil: 'domcontentloaded', timeout: 30000 });
const sleep = (ms) => page.waitForTimeout(ms);
const body = async () => (await page.locator('body').innerText().catch(()=>'')) || '';

page.on('response', (r) => {
  if (r.status() === 401 && r.url().includes('/api/')) log('401 -> ' + r.url().replace(BASE,''));
});

try {
  await goto('/login');
  await page.locator('input#username, input[name="username"], input[type="text"]').first().fill('admin');
  await page.locator('input[type="password"]').first().fill('Admin1234567');
  await page.locator('button[type="submit"]').first().click();
  await sleep(2500);

  // ---- 11. Wizard ----
  await goto('/vms'); await sleep(2000);
  const createBtn = page.locator('button, a', { hasText: /Create VM/i }).first();
  log('11 createBtn count=' + await createBtn.count());
  if (await createBtn.count()) {
    await createBtn.click(); await sleep(2000);
    let wz = await body();
    for (let i=0;i<8;i++){
      const next = page.locator('button', { hasText: /^Next$|Continue|^Next/i }).first();
      if (await next.count() && await next.isVisible().catch(()=>false)) { await next.click().catch(()=>{}); await sleep(600); wz += '\n'+await body(); } else break;
    }
    await shot('11-create-wizard');
    log('11 WIZARD cpuTopo(Sockets/Cores/Threads)=' + (/Sockets/i.test(wz)&&/Cores/i.test(wz)&&/Threads/i.test(wz)) +
        ' markTemplate=' + /Mark as template/i.test(wz) + ' sysprep=' + /Sysprep/i.test(wz) +
        ' tpm=' + /TPM/i.test(wz) + ' secureboot=' + /Secure ?Boot/i.test(wz) + ' cloudinit=' + /cloud.?init/i.test(wz));
    await page.keyboard.press('Escape').catch(()=>{});
  }

  // ---- 13/16/21 correct routes ----
  const views = [
    ['13-apitokens', '/api-tokens', /Token/i],
    ['16-pools', '/resource-pools', /Pool/i],
    ['21-storagebackends', '/storage-backends', /Backend|Storage/i],
  ];
  for (const [n,p,re] of views) {
    await goto(p); await sleep(2200);
    const b = await body();
    log('VIEW ' + n + ' url=' + page.url() + ' notFound=' + /404|Not Found/i.test(b) + ' match=' + re.test(b));
    await shot('view-' + n);
  }

  // ---- 13b. ApiTokens create a token via UI (raw shown once) ----
  await goto('/api-tokens'); await sleep(2000);
  const newTokBtn = page.locator('button, a', { hasText: /New Token|Create Token|Generate|Create/i }).first();
  log('13 tokenCreateBtn=' + await newTokBtn.count());
  await shot('13-apitokens-view');

  // ---- 17. Reconfigure drawer thin/thick/TRIM ----
  await goto('/vms'); await sleep(2000);
  const vmRow = page.locator('a, tr, .invtree-item', { hasText: 'web-server-01' }).first();
  if (await vmRow.count()) { await vmRow.click().catch(()=>{}); await sleep(2500); }
  // Configure tab
  const cfg = page.locator('button, a, [role="tab"]', { hasText: /^Configure$/i }).first();
  if (await cfg.count()) { await cfg.click().catch(()=>{}); await sleep(1500); }
  const edit = page.locator('button', { hasText: /^Edit$|Edit Settings|Reconfigure/i }).first();
  if (await edit.count()) {
    await edit.click().catch(()=>{}); await sleep(1800);
    let dz = await body();
    // expand any disk rows / tabs inside drawer
    const disks = page.locator('button, summary', { hasText: /Disk|vda|Provision/i });
    const dn = await disks.count();
    for (let i=0;i<Math.min(dn,4);i++){ await disks.nth(i).click().catch(()=>{}); await sleep(400); dz += '\n'+await body(); }
    await shot('17-reconfigure-thin');
    log('17 DRAWER resources=' + /Reservation|Shares|Limit/i.test(dz) + ' qos=' + /IOPS|QoS|Throughput/i.test(dz) +
        ' thinThickTrim=' + /Thin|Thick|TRIM|discard|Provision/i.test(dz));
  } else { log('17 no Edit button'); }

  log('DONE');
} catch(e){ log('FATAL '+String(e).slice(0,400)); } finally { await browser.close(); process.exit(0); }
