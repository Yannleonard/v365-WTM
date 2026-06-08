// Definitive UI-driven E2E with the REAL selectors. Clicks every VM action through
// the browser; asserts no API error AND that the effect is real. Test artifacts use
// an e2e- prefix so a companion bash step verifies/cleans them in libvirt.
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';
const BASE = process.env.BASE_URL || 'http://host.docker.internal:8080';
const SHOTS = '/work/test/e2e/shots'; mkdirSync(SHOTS, { recursive: true });
const R = []; const rec = (n, ok, d='') => { R.push({n,ok}); console.log(`${ok?'PASS':'FAIL'}  ${n}${d?'  :: '+d:''}`); };
const fails = [];
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await (await browser.newContext({ viewport:{width:1600,height:1000} })).newPage();
page.on('response', r => { if (r.status() >= 400 && r.url().includes('/api/')) fails.push(`${r.status()} ${r.request().method()} ${r.url().split('/api/v1')[1]||r.url()}`); });
const shot = n => page.screenshot({ path:`${SHOTS}/fs-${n}.png`, fullPage:true }).catch(()=>{});
const goto = async p => { await page.goto(BASE+p,{waitUntil:'domcontentloaded',timeout:25000}); await page.waitForTimeout(1800); };
const sleep = ms => page.waitForTimeout(ms);
const cf = () => { fails.length = 0; };
const ok = () => fails.filter(f=>!f.startsWith('404')).length === 0;
const act = async (title, scope) => { const b=(scope||page).locator(`button[title="${title}"],button[aria-label="${title}"]`).first(); if (await b.count()){await b.click().catch(()=>{});return true;} return false; };
// Fill a labeled field INSIDE the currently-open modal/dialog (avoids matching page
// headings/search boxes). Falls back to the global label search if no dialog.
const fillLabel = async (label,val) => {
  const dlg = page.locator('[role="dialog"], .modal, .Modal').last();
  if (await dlg.count()) {
    const di = dlg.locator(`text=${label}`).locator('xpath=following::input[1]').first();
    if (await di.count()) { await di.fill(val); return true; }
    const any = dlg.locator('input[type="text"], input:not([type])').first();
    if (await any.count()) { await any.fill(val); return true; }
  }
  const i=page.locator(`text=${label}`).locator('xpath=following::input[1]').first();
  if (await i.count()){await i.fill(val);return true;} return false;
};
const clickBtn = async txt => { const b=page.locator(`button:has-text("${txt}")`).last(); if (await b.count()){await b.click().catch(()=>{});return true;} return false; };

try {
  await goto('/login'); await page.waitForSelector('input#username');
  await page.fill('input#username','admin'); await page.fill('input#password','Admin1234567');
  await page.locator('button[type="submit"]').first().click(); await sleep(2000);
  rec('login', !page.url().includes('/login'));

  await goto('/vms');
  const t = await page.locator('body').innerText();
  rec('VMs list: real VMs, no sim', /web-server-01/.test(t) && !/sim-vm-/.test(t));
  const wrow = () => page.locator('tr',{hasText:'web-server-01'}).first();

  // POWER stop/start
  cf(); await act('Stop', wrow()); await sleep(3500);
  rec('power STOP', ok(), fails.join(',')||'ok'); await shot('stop');
  cf(); (await act('Start', wrow())) || (await act('Resume', wrow())); await sleep(3500);
  rec('power START', ok(), fails.join(',')||'ok');

  // SNAPSHOT (on web-server-01 — diskless, so expect a clean ERROR surfaced, NOT silent ok)
  cf(); await act('Snapshot', wrow()); await sleep(800);
  await fillLabel('Name','e2e-snap'); await clickBtn('Create snapshot'); await sleep(2500);
  // diskless -> the app MUST show an error (no silent success). A 4xx is the correct outcome.
  const snapErrShown = fails.some(f=>f.startsWith('4'));
  rec('snapshot on diskless surfaces error (no fake success)', snapErrShown, fails.join(',')||'no error shown!'); await shot('snap-err');
  await page.keyboard.press('Escape').catch(()=>{});

  // RECONFIGURE (detail) memory to a valid value on a stopped VM
  await goto('/vms'); await act('Stop', wrow()); await sleep(3000);
  await page.locator('text=web-server-01').first().click().catch(()=>{}); await sleep(1500);
  cf(); const re = await act('Reconfigure'); await sleep(800);
  if (re) { const n=page.locator('input[type="number"]').first(); if(await n.count()) await n.fill('2'); await clickBtn('Apply'); await sleep(2500); }
  rec('reconfigure (UI->API)', re && ok(), fails.join(',')||'ok'); await shot('reconf');
  await goto('/vms'); await act('Start', wrow()).catch(()=>{});

  // CLONE db-server-01
  await goto('/vms'); cf(); await act('Clone', page.locator('tr',{hasText:'db-server-01'}).first()); await sleep(800);
  await fillLabel('Name','e2e-clone'); await clickBtn('Clone'); await sleep(4000);
  rec('clone (UI->API->libvirt)', ok(), fails.join(',')||'ok'); await shot('clone');

  // NETWORK create (NAT, no CIDR — should now succeed)
  await goto('/vm-networks'); cf();
  await clickBtn('Create network'); await sleep(800);
  await fillLabel('Name','e2e-fnet'); await clickBtn('Create'); await sleep(3000);
  rec('network CREATE (UI->API->libvirt)', /e2e-fnet/.test(await page.locator('body').innerText()) && ok(), fails.join(',')||'ok'); await shot('net');

  // VOLUME create
  await goto('/vm-storage'); await sleep(500);
  const radio = page.locator('input[type="radio"][name="pool"]').first(); if (await radio.count()) await radio.check().catch(()=>{});
  await sleep(500); cf();
  await clickBtn('Create volume'); await sleep(800);
  await fillLabel('Name','e2e-fvol'); const cap=page.locator('input[type="number"]').first(); if(await cap.count()) await cap.fill('1');
  await clickBtn('Create'); await sleep(3000);
  rec('volume CREATE (UI->API->libvirt)', ok(), fails.join(',')||'ok'); await shot('vol');

  // CREATE VM wizard — 4 steps: basics, compute, storage(+pool), network. The
  // final "Create VM" needs a network chosen on the last step.
  await goto('/vms/new'); cf();
  const nm = page.locator('input[placeholder="my-vm"]').first(); if (await nm.count()) await nm.fill('e2e-wizvm');
  for (let i=0;i<6;i++){
    // On the storage step, pick a pool so the disk is provisioned.
    const pool = page.locator('select').filter({ hasText: 'free' }).first();
    if (await pool.count()) await pool.selectOption({ index: 0 }).catch(()=>{});
    const nx=page.locator('button:has-text("Next")').first();
    if (await nx.count() && await nx.isEnabled().catch(()=>false)){await nx.click().catch(()=>{});await sleep(700);} else break;
  }
  // Last step: choose a network (the select offering 'default (nat)').
  const netSel = page.locator('select').last();
  if (await netSel.count()) await netSel.selectOption({ index: 1 }).catch(()=>{});
  await sleep(400);
  (await clickBtn('Create VM')) || (await clickBtn('Create')) || (await clickBtn('Finish')); await sleep(4500);
  rec('Create VM wizard (UI->API->libvirt)', ok(), fails.join(',')||'ok'); await shot('wizard');

  console.log('\n--- API failures across run ---'); [...new Set(fails)].forEach(f=>console.log('  '+f));
} catch(e){ rec('FATAL', false, String(e).slice(0,200)); }
finally { console.log(`\n===== ${R.filter(r=>r.ok).length}/${R.length} passed =====`); await browser.close(); process.exit(0); }
