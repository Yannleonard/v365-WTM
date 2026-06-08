import { chromium } from 'playwright';
const BASE='http://host.docker.internal:8080';
const b=await chromium.launch({args:['--no-sandbox']});
const page=await (await b.newContext({viewport:{width:1600,height:1000}})).newPage();
const goto=async p=>{await page.goto(BASE+p,{waitUntil:'domcontentloaded',timeout:25000});await page.waitForTimeout(1500);};
await goto('/login'); await page.waitForSelector('input#username');
await page.fill('input#username','admin'); await page.fill('input#password','Admin1234567');
await page.locator('button[type="submit"]').first().click(); await page.waitForTimeout(2000);
async function dialogDump(label){
  await page.waitForTimeout(800);
  const inputs=await page.$$eval('input,select,textarea',els=>els.map(e=>({tag:e.tagName,type:e.type||'',ph:e.placeholder||'',name:e.name||'',label:''})));
  const btns=await page.$$eval('button',els=>els.map(b=>(b.textContent||'').trim()).filter(Boolean));
  const labels=await page.$$eval('label',els=>els.map(l=>(l.textContent||'').trim()).filter(Boolean));
  console.log(`\n== ${label} ==`); console.log('LABELS:',labels.join(' | ')); console.log('INPUTS:',JSON.stringify(inputs)); console.log('BUTTONS:',JSON.stringify(btns));
}
// snapshot dialog
await goto('/vms'); await page.locator('text=web-server-01').first().click(); await page.waitForTimeout(1500);
await page.locator('button[title="Snapshot"]').first().click().catch(()=>{});
await dialogDump('SNAPSHOT dialog');
await page.keyboard.press('Escape');
// reconfigure dialog
await page.locator('button[title="Reconfigure"]').first().click().catch(()=>{});
await dialogDump('RECONFIGURE dialog');
await b.close(); process.exit(0);
