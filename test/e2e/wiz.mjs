import { chromium } from 'playwright';
const BASE='http://host.docker.internal:8080';
const b=await chromium.launch({args:['--no-sandbox']});
const page=await (await b.newContext({viewport:{width:1600,height:1000}})).newPage();
const goto=async p=>{await page.goto(BASE+p,{waitUntil:'domcontentloaded',timeout:25000});await page.waitForTimeout(1500);};
await goto('/login'); await page.waitForSelector('input#username');
await page.fill('input#username','admin'); await page.fill('input#password','Admin1234567');
await page.locator('button[type="submit"]').first().click(); await page.waitForTimeout(2000);
await goto('/vms/new');
await page.locator('input[placeholder="my-vm"]').first().fill('e2e-wizvm');
for (let step=1; step<=6; step++){
  const btns = await page.$$eval('button', els=>els.map(b=>(b.textContent||'').trim()).filter(Boolean));
  const sel = await page.$$eval('select', els=>els.map(s=>({opts:[...s.options].map(o=>o.text).slice(0,4)})));
  const inputs = await page.$$eval('input', els=>els.map(e=>({type:e.type,ph:e.placeholder})));
  console.log(`STEP ${step}: BTNS=${JSON.stringify(btns)} INPUTS=${JSON.stringify(inputs)} SELECTS=${JSON.stringify(sel)}`);
  const next = page.locator('button:has-text("Next")').first();
  if (await next.count() && await next.isEnabled().catch(()=>false)) { await next.click().catch(()=>{}); await page.waitForTimeout(700); }
  else { console.log('  (no enabled Next — last step)'); break; }
}
await b.close(); process.exit(0);
