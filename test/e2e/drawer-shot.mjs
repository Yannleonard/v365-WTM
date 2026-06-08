import { chromium } from 'playwright';
const BASE='http://host.docker.internal:8080';
const b=await chromium.launch({args:['--no-sandbox']});
const page=await (await b.newContext({viewport:{width:1500,height:950}})).newPage();
const goto=async p=>{await page.goto(BASE+p,{waitUntil:'domcontentloaded',timeout:25000});await page.waitForTimeout(1800);};
await goto('/login'); await page.waitForSelector('input#username');
await page.fill('input#username','admin'); await page.fill('input#password','Admin1234567');
await page.locator('button[type="submit"]').first().click(); await page.waitForTimeout(2000);
await goto('/vms');
await page.locator('text=web-server-01').first().click().catch(()=>{}); await page.waitForTimeout(1500);
// click Reconfigure (title/aria)
await page.locator('button[title="Reconfigure"],button[aria-label="Reconfigure"]').first().click().catch(()=>{});
await page.waitForTimeout(1500);
await page.screenshot({path:'/work/test/e2e/shots/reconfigure-drawer.png', fullPage:true});
// check it's a drawer (right side) + has disks/nics sections
const t=await page.locator('body').innerText();
console.log('has Disks section:', /disk/i.test(t), '| has Network section:', /network|adapter/i.test(t), '| has vCPU:', /vcpu|cpu/i.test(t));
await b.close(); process.exit(0);
