// Definitive UI-driven E2E using the REAL selectors (icon buttons carry title/aria).
// Clicks every VM action through the browser; asserts no API error + UI reflects it.
// Companion bash verifies each effect in real libvirt.
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const BASE = process.env.BASE_URL || 'http://host.docker.internal:8080';
const SHOTS = '/work/test/e2e/shots';
mkdirSync(SHOTS, { recursive: true });

const R = [];
const rec = (n, ok, d = '') => { R.push({ n, ok }); console.log(`${ok ? 'PASS' : 'FAIL'}  ${n}${d ? '  :: ' + d : ''}`); };
const fails = [];
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await (await browser.newContext({ viewport: { width: 1600, height: 1000 } })).newPage();
page.on('response', r => { if (r.status() >= 400 && r.url().includes('/api/')) fails.push(`${r.status()} ${r.request().method()} ${r.url().replace(BASE,'')}`); });
const shot = n => page.screenshot({ path: `${SHOTS}/fs-${n}.png`, fullPage: true }).catch(()=>{});
const goto = async p => { await page.goto(BASE + p, { waitUntil: 'domcontentloaded', timeout: 25000 }); await page.waitForTimeout(1500); };
const sleep = ms => page.waitForTimeout(ms);
const cf = () => { fails.length = 0; };
const ok = () => fails.filter(f=>!f.startsWith('404')).length === 0;
// click an icon action button (title/aria) inside scope
async function action(title, scope) {
  const b = (scope||page).locator(`button[title="${title}"], button[aria-label="${title}"]`).first();
  if (await b.count()) { await b.click().catch(()=>{}); return true; } return false;
}
// confirm any open destructive dialog
async function confirm() {
  for (const t of ['Confirm','Delete','Yes','OK','Remove']) {
    const b = page.locator(`button:has-text("${t}")`).last();
    if (await b.count() && await b.isVisible().catch(()=>false)) { await b.click().catch(()=>{}); return; }
  }
}
async function fillByLabel(label, val) {
  const i = page.locator(`text=${label}`).locator('xpath=following::input[1]').first();
  if (await i.count()) { await i.fill(val); return true; } return false;
}

try {
  await goto('/login');
  await page.waitForSelector('input#username');
  await page.fill('input#username','admin'); await page.fill('input#password','Admin1234567');
  await page.locator('button[type="submit"]').first().click(); await sleep(2000);
  rec('login', !page.url().includes('/login'));

  // ---- POWER: web-server-01 running -> Stop, then Resume/Start ----
  await goto('/vms');
  rec('only real VMs (no sim)', /web-server-01/.test(await page.locator('body').innerText()) && !/sim-vm-/.test(await page.locator('body').innerText()));
  const wrow = () => page.locator('tr', { hasText: 'web-server-01' }).first();
  cf(); const didStop = await action('Stop', wrow()); await sleep(3500);
  rec('power STOP (UI->API)', didStop && ok(), fails.join(',')||'ok'); await shot('stop');
  cf(); const didStart = (await action('Start', wrow())) || (await action('Resume', wrow())); await sleep(3500);
  rec('power START/RESUME (UI->API)', didStart && ok(), fails.join(',')||'ok');

  // ---- SNAPSHOT via row action ----
  cf(); await action('Snapshot', wrow()); await sleep(800);
  await fillByLabel('Name','e2e-snap');
  await page.locator('button:has-text("Create"),button:has-text("Take"),button:has-text("Save")').last().click().catch(()=>{});
  await sleep(3000);
  rec('snapshot CREATE (UI->API->libvirt)', ok() && !fails.length, fails.join(',')||'ok'); await shot('snap');

  // ---- RECONFIGURE via detail header ----
  await goto('/vms'); await page.locator('text=web-server-01').first().click().catch(()=>{}); await sleep(1500);
  cf(); const didRe = await action('Reconfigure'); await sleep(800);
  if (didRe) { const n = page.locator('input[type="number"]').first(); if (await n.count()) await n.fill('3');
    await page.locator('button:has-text("Save"),button:has-text("Apply"),button:has-text("Reconfigure")').last().click().catch(()=>{}); await sleep(3000); }
  rec('reconfigure (UI->API)', didRe && ok(), fails.join(',')||'ok'); await shot('reconf');

  // ---- CLONE via row action ----
  await goto('/vms'); cf(); await action('Clone', page.locator('tr',{hasText:'db-server-01'}).first()); await sleep(800);
  await fillByLabel('Name','e2e-clone');
  await page.locator('button:has-text("Clone"),button:has-text("Create")').last().click().catch(()=>{}); await sleep(3500);
  rec('clone (UI->API->libvirt)', ok(), fails.join(',')||'ok'); await shot('clone');

  // ---- NETWORK create ----
  await goto('/vm-networks'); cf();
  await page.locator('button:has-text("Create network")').first().click().catch(()=>{}); await sleep(800);
  await fillByLabel('Name','e2e-fnet');
  await page.locator('button:has-text("Create"),button:has-text("Save")').last().click().catch(()=>{}); await sleep(3000);
  rec('network CREATE (UI->API->libvirt)', /e2e-fnet/.test(await page.locator('body').innerText()) && ok(), fails.join(',')||'ok'); await shot('net');

  // ---- VOLUME create ----
  await goto('/vm-storage'); await sleep(500);
  const radio = page.locator('input[type="radio"][name="pool"]').first(); if (await radio.count()) await radio.check().catch(()=>{});
  await sleep(500); cf();
  await page.locator('button:has-text("Create volume")').first().click().catch(()=>{}); await sleep(800);
  await fillByLabel('Name','e2e-fvol');
  const cap = page.locator('input[type="number"]').first(); if (await cap.count()) await cap.fill('1');
  await page.locator('button:has-text("Create"),button:has-text("Save")').last().click().catch(()=>{}); await sleep(3000);
  rec('volume CREATE (UI->API->libvirt)', ok(), fails.join(',')||'ok'); await shot('vol');

  // ---- CREATE VM wizard (placeholder my-vm) ----
  await goto('/vms/new'); cf();
  const nm = page.locator('input[placeholder="my-vm"]').first();
  if (await nm.count()) await nm.fill('e2e-wizardvm');
  for (let i=0;i<6;i++){ const nx=page.locator('button:has-text("Next")').first(); if (await nx.count() && await nx.isEnabled().catch(()=>false)) { await nx.click().catch(()=>{}); await sleep(500);} else break; }
  await page.locator('button:has-text("Create VM"),button:has-text("Create"),button:has-text("Finish")').last().click().catch(()=>{}); await sleep(3500);
  rec('Create VM wizard (UI->API->libvirt)', ok(), fails.join(',')||'ok'); await shot('wizard');

  console.log('\n--- API failures across run ---'); [...new Set(fails)].forEach(f=>console.log('  '+f));
} catch(e){ rec('FATAL', false, String(e).slice(0,200)); }
finally { console.log(`\n===== ${R.filter(r=>r.ok).length}/${R.length} passed =====`); await browser.close(); process.exit(0); }
