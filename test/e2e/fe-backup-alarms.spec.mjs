// frontend-engineer verification (travaux.md §0ter / §4.6):
//   1. The VM "Back up now" action against KVM web-server-01 (real qcow2) SUCCEEDS
//      with an honest success toast (size stored) — never a fake/silent success;
//      and a clear error toast is what would surface if export were unsupported.
//   2. The Alarms SMTP channel create form exposes host/port/user/password/from/to/TLS,
//      and the Test button surfaces a result toast.
//
// Screenshots land in test/e2e/shots/fe-*.png.
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const BASE = process.env.BASE || process.env.BASE_URL || 'http://host.docker.internal:8080';
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
const toastText = async () => (await page.locator('.toast, [class*="toast"]').allInnerTexts().catch(() => [])).join(' | ');

try {
  // ---- login ----
  await goto('/login');
  await page.locator('input[type="text"], input[name="username"]').first().fill(USER);
  await page.locator('input[type="password"]').first().fill(PASS);
  await page.locator('button[type="submit"]').first().click();
  await page.waitForTimeout(2000);
  log('login -> ' + page.url());

  // ---- VM detail: open web-server-01 (KVM, real qcow2) ----
  await goto('/vms');
  await page.waitForTimeout(2500);
  let row = page.locator('tr', { hasText: 'web-server-01' }).first();
  if (!(await row.count())) row = page.locator('tbody tr').first();
  await row.click();
  await page.waitForTimeout(2500);
  await shot('fe-01-vm-detail');

  // Open Actions ▾ and confirm "Back up now…" item is present + enabled.
  const actionsBtn = page.locator('button[aria-label="Actions"]').first();
  await actionsBtn.click();
  await page.waitForTimeout(500);
  const backupItem = page.locator('.menu-item', { hasText: 'Back up now' }).first();
  const present = await backupItem.count();
  const disabled = present ? await backupItem.isDisabled() : true;
  log(`Back up now item present=${!!present} disabled=${disabled}`);
  await shot('fe-02-actions-menu-backup');

  if (present && !disabled) {
    await backupItem.click();
    // Wait for the drawer's backend <select> to populate (useStorageBackends()).
    const drawer = page.locator('.drawer').last();
    await drawer.waitFor({ state: 'visible', timeout: 8000 }).catch(() => {});
    const sel = drawer.locator('select').first();
    // Wait for the real (non-placeholder) options to render.
    await drawer.locator('select option', { hasText: /local|s3|azure|minio/i })
      .first().waitFor({ state: 'attached', timeout: 12000 }).catch(() => {});
    const opts = await sel.locator('option').count();
    log('backend options=' + opts);
    await shot('fe-03-backup-drawer');
    if (opts > 1) {
      await sel.selectOption({ index: 1 });
      await page.waitForTimeout(300);
      const backBtn = drawer.locator('button', { hasText: /^Back up$/ }).first();
      if (await backBtn.count()) {
        await backBtn.click();
        // KVM file-backed disk: real qemu-img export. Poll for the result toast.
        let tt = '';
        for (let i = 0; i < 30; i++) {
          await page.waitForTimeout(1000);
          tt = await toastText();
          if (tt && /complete|failed|stored|error/i.test(tt)) break;
        }
        await shot('fe-04-backup-result-toast');
        log('backup toast: ' + tt);
      }
    } else {
      log('WARN: no storage backend available to pick');
    }
  }

  // ---- Alarms: SMTP channel form + Test ----
  await goto('/alarms');
  await page.waitForTimeout(2000);
  await shot('fe-05-alarms');

  // Open "New channel" and switch the Type select to smtp.
  const newCh = page.locator('button', { hasText: 'New channel' }).first();
  if (await newCh.count()) {
    await newCh.click();
    await page.waitForTimeout(600);
    const typeSel = page.locator('.modal select, [role="dialog"] select').first();
    await typeSel.selectOption('smtp').catch(() => {});
    await page.waitForTimeout(500);
    // Verify the SMTP fields are present by their labels.
    const labels = (await page.locator('.modal, [role="dialog"]').first().innerText()).toLowerCase();
    for (const f of ['smtp host', 'port', 'username', 'password', 'from', 'to', 'encryption']) {
      log(`smtp field "${f}" present=${labels.includes(f)}`);
    }
    // Fill the SMTP form against a local relay (no TLS) and create the channel.
    const dlg = page.locator('.modal, [role="dialog"]').first();
    const fill = async (label, val) => {
      const f = dlg.locator('.field', { hasText: label }).locator('input').first();
      if (await f.count()) await f.fill(val);
    };
    await dlg.locator('input').first().fill('fe-smtp-test'); // Name
    await fill('SMTP host', 'localhost');
    await fill('From', 'alarms@example.com');
    await fill('To', 'ops@example.com');
    await shot('fe-06-alarms-smtp-form');
    const createBtn = dlg.locator('button', { hasText: /Create channel/ }).first();
    if (await createBtn.count() && !(await createBtn.isDisabled())) {
      await createBtn.click();
      await page.waitForTimeout(1500);
      log('create channel toast: ' + (await toastText()));
    } else {
      await page.locator('button', { hasText: 'Cancel' }).first().click().catch(() => {});
    }
    await page.waitForTimeout(800);
  } else {
    log('WARN: New channel button not found');
  }

  // Test the channel -> expect a result toast (success or clear error vs localhost:25).
  const testBtn = page.locator('button', { hasText: /^Test$/ }).first();
  if (await testBtn.count()) {
    await testBtn.click();
    let tt = '';
    for (let i = 0; i < 10; i++) { await page.waitForTimeout(800); tt = await toastText(); if (tt) break; }
    await shot('fe-07-alarms-test-toast');
    log('channel test toast: ' + tt);
  } else {
    log('no existing channel to Test');
  }

  log('done');
} catch (e) {
  log('FATAL ' + String(e).slice(0, 400));
} finally {
  await browser.close();
  process.exit(0);
}
