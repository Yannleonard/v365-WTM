// Dump the real interactive elements (buttons w/ title/aria/text, inputs, tabs) on
// each view so we target them precisely instead of guessing selectors.
import { chromium } from 'playwright';
const BASE = process.env.BASE_URL || 'http://host.docker.internal:8080';
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await (await browser.newContext({ viewport:{width:1600,height:1000} })).newPage();
const goto = p => page.goto(BASE + p, { waitUntil: 'domcontentloaded', timeout:25000 });
const sleep = ms => page.waitForTimeout(ms);

await goto('/login');
await page.waitForSelector('input#username', { timeout: 20000 });
await page.fill('input#username', 'admin');
await page.fill('input#password', 'Admin1234567');
await page.locator('button[type="submit"]').first().click();
await sleep(2000);

async function dump(label, path, extra) {
  await goto(path); await sleep(1500);
  if (extra) await extra();
  const btns = await page.$$eval('button', els => els.map(b => ({ t:(b.textContent||'').trim().slice(0,24), title:b.title||'', aria:b.getAttribute('aria-label')||'' })).filter(b=>b.t||b.title||b.aria));
  const inputs = await page.$$eval('input,select', els => els.map(e => ({ tag:e.tagName, type:e.type||'', name:e.name||'', ph:e.placeholder||'' })));
  const tabs = await page.$$eval('[role="tab"], .tab', els => els.map(e=>(e.textContent||'').trim()).filter(Boolean));
  console.log(`\n===== ${label} (${path}) =====`);
  console.log('TABS:', tabs.join(' | ') || '(none)');
  console.log('BUTTONS:', JSON.stringify(btns.slice(0,40)));
  console.log('INPUTS:', JSON.stringify(inputs.slice(0,20)));
}

await dump('VMs list', '/vms');
// open detail
await goto('/vms'); await sleep(1000);
await page.locator('text=web-server-01').first().click().catch(()=>{});
await sleep(1500);
{
  const btns = await page.$$eval('button', els => els.map(b => ({ t:(b.textContent||'').trim().slice(0,24), title:b.title||'', aria:b.getAttribute('aria-label')||'' })).filter(b=>b.t||b.title||b.aria));
  const tabs = await page.$$eval('[role="tab"], .tab', els => els.map(e=>(e.textContent||'').trim()).filter(Boolean));
  console.log('\n===== VM DETAIL (web-server-01) =====');
  console.log('TABS:', tabs.join(' | '));
  console.log('HEADER/ACTION BUTTONS:', JSON.stringify(btns.slice(0,40)));
}
await dump('VM Storage', '/vm-storage');
await dump('Create VM wizard', '/vms/new');
await browser.close();
process.exit(0);
