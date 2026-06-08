// QA full end-to-end UI validation (Lots 1-5). Headless Chromium.
// Captures console errors + 4xx/5xx responses on main views.
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const BASE = process.env.BASE_URL || 'http://host.docker.internal:8080';
const USER = process.env.UNIHV_USER || 'admin';
const PASS = process.env.UNIHV_PASS || 'Admin1234567';
const SHOTS = '/work/test/e2e/shots';
mkdirSync(SHOTS, { recursive: true });

const log = (m) => console.log(m);
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const ctx = await browser.newContext({ viewport: { width: 1680, height: 1050 } });
const page = await ctx.newPage();
const shot = (n) => page.screenshot({ path: `${SHOTS}/qa-${n}.png`, fullPage: false }).catch(() => {});
const goto = (p) => page.goto(BASE + p, { waitUntil: 'domcontentloaded', timeout: 30000 });
const sleep = (ms) => page.waitForTimeout(ms);

const jsErrors = [];
const badResponses = [];
page.on('console', (msg) => {
  if (msg.type() === 'error') {
    const t = msg.text();
    if (/translate|gstatic|CSP|Content Security|favicon/i.test(t)) return;
    jsErrors.push(t.slice(0, 200));
  }
});
page.on('pageerror', (e) => jsErrors.push('PAGEERROR ' + String(e).slice(0, 200)));
page.on('response', (r) => {
  const s = r.status();
  const u = r.url();
  if (s >= 400 && u.includes('/api/')) {
    // ignore pre-login auth/me 401
    if (s === 401 && /auth\/me/.test(u)) return;
    badResponses.push(`${s} ${r.request().method()} ${u.replace(BASE, '').slice(0, 120)}`);
  }
});

async function bodyText() { return (await page.locator('body').innerText().catch(() => '')) || ''; }
async function has(re) { return re.test(await bodyText()); }

try {
  // ---- 1. LOGIN ----
  await goto('/login');
  await page.locator('input#username, input[name="username"], input[type="text"]').first().fill(USER);
  await page.locator('input[type="password"]').first().fill(PASS);
  await page.locator('button[type="submit"]').first().click();
  await sleep(2500);
  log('1 LOGIN -> url=' + page.url() + '  loggedIn=' + (!/\/login/.test(page.url())));
  await shot('01-login-dashboard');

  // ---- 2/3/4/5/6. VM list + tree + detail + tabs + actionbar + tasks bar ----
  await goto('/vms');
  await sleep(2500);
  const treeCount = await page.locator('.invtree').count();
  log('2 TREE present=' + treeCount);
  // expand tree
  for (let pass = 0; pass < 5; pass++) {
    const chevs = page.locator('.invtree-item[aria-expanded="false"] .invtree-chev:not(.ghost)');
    const n = await chevs.count();
    if (!n) break;
    for (let i = 0; i < n; i++) { await chevs.nth(i).click().catch(() => {}); await sleep(120); }
    await sleep(300);
  }
  await shot('02-tree-expanded');
  const treeBody = await bodyText();
  log('2 tree contains Hypervisors=' + /Hypervisor/i.test(treeBody) + ' web-server-01=' + /web-server-01/.test(treeBody) + ' Containers=' + /Container/i.test(treeBody));

  // click web-server-01
  const vmNode = page.locator('.invtree-item', { hasText: 'web-server-01' }).first();
  if (await vmNode.count()) { await vmNode.scrollIntoViewIfNeeded().catch(()=>{}); await vmNode.click(); }
  await sleep(3000);
  log('2 VM detail route=' + page.url() + ' isDetail=' + /\/vms\/[^/]+\/[^/]+/.test(page.url()));
  await shot('03-vm-detail-summary');

  // ---- 3. TABS ----
  const tabs = ['Summary','Monitor','Configure','Permissions','Snapshots','Console','Inspect'];
  const detailText = await bodyText();
  const tabsFound = tabs.filter(t => new RegExp('\\b'+t+'\\b','i').test(detailText));
  log('3 TABS found=' + JSON.stringify(tabsFound));

  // ---- 4. Summary cards/gauges ----
  log('4 SUMMARY cpu=' + /CPU/i.test(detailText) + ' mem=' + /Mem|Memory/i.test(detailText) +
      ' storage=' + /Storage/i.test(detailText) + ' general=' + /General/i.test(detailText) +
      ' hardware=' + /Hardware/i.test(detailText) + ' related=' + /Related/i.test(detailText));
  await shot('04-summary-gauges');

  // ---- 5. Action bar text + Actions menu ----
  log('5 ACTIONBAR console=' + /Console/i.test(detailText) + ' shutdown=' + /Shut ?Down|Power/i.test(detailText) +
      ' editsettings=' + /Edit Settings|Edit/i.test(detailText) + ' snapshot=' + /Snapshot/i.test(detailText));
  // open Actions menu
  const actBtn = page.locator('button', { hasText: /Actions/i }).first();
  if (await actBtn.count()) {
    await actBtn.click().catch(()=>{});
    await sleep(800);
    const menuText = await bodyText();
    log('5 ACTIONS-MENU power=' + /Power/i.test(menuText) + ' snapshots=' + /Snapshot/i.test(menuText) +
        ' storage=' + /Storage/i.test(menuText) + ' networking=' + /Network/i.test(menuText) +
        ' clone=' + /Clone/i.test(menuText) + ' migrate=' + /Migrate/i.test(menuText) + ' delete=' + /Delete/i.test(menuText));
    await shot('05-actions-menu');
    await page.keyboard.press('Escape').catch(()=>{});
  } else {
    log('5 ACTIONS-MENU button NOT FOUND');
  }

  // ---- 6. Recent Tasks / Alarms bottom bar ----
  await sleep(500);
  const txt6 = await bodyText();
  log('6 BOTTOMBAR recentTasks=' + /Recent Tasks|Tasks/i.test(txt6) + ' alarms=' + /Alarm/i.test(txt6));
  await shot('06-bottom-tasks-alarms');

  // ---- 7. Monitor tab ----
  const monTab = page.locator('button, a, [role="tab"]', { hasText: /^Monitor$/i }).first();
  if (await monTab.count()) { await monTab.click().catch(()=>{}); await sleep(2500); }
  const monText = await bodyText();
  const svgCount = await page.locator('svg').count();
  log('7 MONITOR svgCharts=' + svgCount + ' cpu=' + /CPU/i.test(monText) + ' mem=' + /Mem/i.test(monText) +
      ' net=' + /Net/i.test(monText) + ' disk=' + /Disk/i.test(monText) + ' events=' + /Event/i.test(monText));
  await shot('07-monitor');

  // ---- 8. Guest info in Summary ----
  const sumTab = page.locator('button, a, [role="tab"]', { hasText: /^Summary$/i }).first();
  if (await sumTab.count()) { await sumTab.click().catch(()=>{}); await sleep(1500); }
  const sumText = await bodyText();
  log('8 GUESTINFO guestPresent=' + /Guest|Agent|Hostname|IP Address|VMware Tools|guest agent/i.test(sumText));
  await shot('08-summary-guest');

  // ---- 9. Snapshots tab UI ----
  const snapTab = page.locator('button, a, [role="tab"]', { hasText: /^Snapshots$/i }).first();
  if (await snapTab.count()) { await snapTab.click().catch(()=>{}); await sleep(2000); }
  const snapText = await bodyText();
  log('9 SNAPSHOTS-UI present=' + (await snapTab.count()>0) + ' takeBtn=' + /Take Snapshot|Create Snapshot|New Snapshot|Take/i.test(snapText));
  await shot('09-snapshots-tab');

  // ---- 10. Configure tab: resize + hardware + options + Edit drawer ----
  const cfgTab = page.locator('button, a, [role="tab"]', { hasText: /^Configure$/i }).first();
  if (await cfgTab.count()) { await cfgTab.click().catch(()=>{}); await sleep(2000); }
  const cfgText = await bodyText();
  log('10 CONFIGURE resize=' + /Resize/i.test(cfgText) + ' hardware=' + /Hardware/i.test(cfgText) +
      ' options=' + /Options/i.test(cfgText) + ' editBtn=' + /Edit/i.test(cfgText));
  await shot('10-configure');
  // open Edit drawer (reconfigure)
  const editBtn = page.locator('button', { hasText: /^Edit$|Edit Settings|Reconfigure/i }).first();
  if (await editBtn.count()) {
    await editBtn.click().catch(()=>{});
    await sleep(1800);
    const drawerText = await bodyText();
    log('10/17 RECONFIGURE-DRAWER opened; resources=' + /Reservation|Shares|Limit|Resources/i.test(drawerText) +
        ' qos=' + /QoS|IOPS|Throughput|read.*iops/i.test(drawerText) +
        ' thin=' + /Thin|Thick|TRIM|Provision/i.test(drawerText));
    await shot('10-17-reconfigure-drawer');
    await page.keyboard.press('Escape').catch(()=>{});
    await sleep(500);
  } else {
    log('10 EDIT button NOT FOUND on Configure');
  }

  // ---- 11. Create VM wizard ----
  await goto('/vms');
  await sleep(1500);
  const createBtn = page.locator('button, a', { hasText: /Create VM|New VM|Create Virtual/i }).first();
  if (await createBtn.count()) {
    await createBtn.click().catch(()=>{});
    await sleep(2000);
    let wz = await bodyText();
    // try to advance through steps to reveal all sections
    for (let i=0;i<6;i++){
      const next = page.locator('button', { hasText: /^Next$|Continue/i }).first();
      if (await next.count()) { await next.click().catch(()=>{}); await sleep(700); wz += '\n' + await bodyText(); } else break;
    }
    log('11 WIZARD topology=' + /Topology|Cores|Sockets|Threads/i.test(wz) +
        ' template=' + /Template/i.test(wz) + ' sysprep=' + /Sysprep/i.test(wz) +
        ' tpm=' + /TPM/i.test(wz) + ' secureboot=' + /Secure ?Boot/i.test(wz) + ' cloudinit=' + /cloud.?init/i.test(wz));
    await shot('11-create-wizard');
    await page.keyboard.press('Escape').catch(()=>{});
  } else {
    log('11 CREATE VM button NOT FOUND');
  }

  // ---- 13/16/19/20/21. Other views render ----
  const views = [
    ['13-apitokens', ['/settings/api-tokens','/api-tokens','/settings/tokens','/tokens'], /Token|API/i],
    ['16-pools', ['/vm/pools','/pools','/resource-pools'], /Pool/i],
    ['19-vmbackups', ['/vm-backups','/backups/vm','/backups'], /Backup/i],
    ['20-alarms', ['/alarms'], /Alarm/i],
    ['21-finops', ['/finops'], /Cost|FinOps|Spend/i],
    ['21-insights', ['/insights'], /Insight/i],
    ['21-replication', ['/replication'], /Replicat/i],
    ['21-storagebackends', ['/storage/backends','/storage-backends','/settings/storage'], /Storage|Backend/i],
  ];
  for (const [name, paths, re] of views) {
    let ok = false, used = '';
    for (const p of paths) {
      await goto(p).catch(()=>{});
      await sleep(1800);
      if (!/\/login/.test(page.url()) && !/404|Not Found|Page not found/i.test(await bodyText()) && re.test(await bodyText())) { ok = true; used = p; break; }
      if (re.test(await bodyText())) { ok = true; used = p; break; }
    }
    log('VIEW ' + name + ' render=' + ok + ' path=' + used + ' url=' + page.url());
    await shot('view-' + name);
  }

  // ---- bulk multi-select UI on VM list ----
  await goto('/vms');
  await sleep(2000);
  const cbs = await page.locator('input[type="checkbox"]').count();
  log('14 VMLIST checkboxes=' + cbs);
  if (cbs > 1) {
    await page.locator('input[type="checkbox"]').nth(1).check().catch(()=>{});
    await sleep(800);
    const bt = await bodyText();
    log('14 BULK-BAR after select: ' + /selected|Bulk|Power|Start|Stop/i.test(bt));
    await shot('14-bulk-bar');
  }

  log('CONSOLE-ERRORS count=' + jsErrors.length);
  jsErrors.slice(0, 25).forEach(e => log('  JSERR: ' + e));
  log('BAD-API-RESPONSES count=' + badResponses.length);
  [...new Set(badResponses)].slice(0, 40).forEach(r => log('  API: ' + r));

  log('DONE');
} catch (e) {
  log('FATAL ' + String(e).slice(0, 500));
} finally {
  await browser.close();
  process.exit(0);
}
