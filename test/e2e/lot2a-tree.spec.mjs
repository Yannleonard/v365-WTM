// Lot 2A — Inventory Tree screenshots + navigation smoke.
// Logs in, opens /vms (where the tree shows as a second pane), expands a
// hypervisor provider → host → VM, screenshots it, then clicks a VM node and
// asserts it navigates to that VM's detail route (/vms/:pid/:id).
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
const sleep = (ms) => page.waitForTimeout(ms);

try {
  await goto('/login');
  await page.locator('input#username, input[name="username"], input[type="text"]').first().fill(USER);
  await page.locator('input[type="password"]').first().fill(PASS);
  await page.locator('button[type="submit"]').first().click();
  await sleep(2000);
  log('login -> ' + page.url());

  // VM domain → the Inventory Tree pane appears beside the content.
  await goto('/vms');
  await sleep(2500);

  const tree = page.locator('.invtree').first();
  log('tree present: ' + (await tree.count()));
  await shot('lot2a-00-tree-collapsed-roots');

  // Expand the Hypervisors root (default open) then drill providers/hosts. Expand
  // every collapsed expandable row a couple of passes to reveal provider→host→VM.
  for (let pass = 0; pass < 4; pass++) {
    const chevs = page.locator('.invtree-item[aria-expanded="false"] .invtree-chev:not(.ghost)');
    const n = await chevs.count();
    if (!n) break;
    for (let i = 0; i < n; i++) {
      const c = chevs.nth(i);
      if (await c.count()) { await c.click().catch(() => {}); await sleep(150); }
    }
    await sleep(400);
  }
  await sleep(800);
  await shot('lot2a-01-tree-expanded');

  // Find a VM node by a known VM name and click it → should route to detail.
  const vmNames = ['web-server-01', 'linux-server', 'db-server-01', 'windows-11'];
  let clicked = null;
  for (const name of vmNames) {
    const node = page.locator('.invtree-item', { hasText: name }).first();
    if (await node.count()) {
      await node.scrollIntoViewIfNeeded().catch(() => {});
      await node.click();
      clicked = name;
      break;
    }
  }
  await sleep(2500);
  log('clicked VM node: ' + clicked + ' -> ' + page.url());
  const onDetail = /\/vms\/[^/]+\/[^/]+/.test(page.url());
  log(onDetail ? 'PASS navigation to VM detail' : 'FAIL not on a VM detail route');
  await shot('lot2a-02-vm-selected-detail');

  // The clicked node should now be the active row in the tree.
  const active = page.locator('.invtree-item.active').first();
  log('active node count: ' + (await active.count()));
  await shot('lot2a-03-active-node-highlight');

  log('done');
} catch (e) {
  log('FATAL ' + String(e).slice(0, 400));
} finally {
  await browser.close();
  process.exit(0);
}
