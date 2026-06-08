import { chromium } from 'playwright';
const BASE = 'http://host.docker.internal:8080';
const VM = process.env.QAVM || 'Alpine';
const b = await chromium.launch({ args: ['--no-sandbox'] });
const page = await (await b.newContext({ viewport: { width: 1400, height: 900 } })).newPage();
page.on('websocket', ws => {
  if (/console/.test(ws.url())) {
    console.log('WS OPEN:', ws.url());
    ws.on('close', () => console.log('WS CLOSE (console)'));
  }
});
page.on('console', m => { const t = m.text(); if (/console|guac|error|fail/i.test(t)) console.log('PAGE:', t.slice(0, 160)); });
const goto = async p => { await page.goto(BASE + p, { waitUntil: 'domcontentloaded', timeout: 25000 }); await page.waitForTimeout(1600); };
await goto('/login'); await page.waitForSelector('input#username');
await page.fill('input#username', 'admin'); await page.fill('input#password', 'Admin1234567');
await page.locator('button[type="submit"]').first().click(); await page.waitForTimeout(2000);
await goto('/vms');
await page.locator('text=' + VM).first().click().catch(() => {}); await page.waitForTimeout(1500);
console.log('--- clicking Console tab for', VM, '---');
await page.locator('button:has-text("Console"),[role="tab"]:has-text("Console")').first().click().catch(() => {});
await page.waitForTimeout(10000);
const txt = await page.locator('body').innerText();
const m = txt.match(/Opening interactive console|Connected|Disconnected|error|failed|not supported/i);
console.log(VM, 'PANEL STATE:', m ? m[0] : 'unknown');
await page.screenshot({ path: `/work/test/e2e/shots/console-debug-${VM}.png` });
await b.close(); process.exit(0);
