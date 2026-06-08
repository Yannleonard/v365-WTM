import { chromium } from 'playwright';
const BASE='http://host.docker.internal:8080';
const b=await chromium.launch({args:['--no-sandbox']});
const page=await (await b.newContext({viewport:{width:1600,height:950}})).newPage();
const goto=async p=>{await page.goto(BASE+p,{waitUntil:'domcontentloaded',timeout:25000});await page.waitForTimeout(2000);};
await goto('/login'); await page.waitForSelector('input#username');
await page.fill('input#username','admin'); await page.fill('input#password','Admin1234567');
await page.locator('button[type="submit"]').first().click(); await page.waitForTimeout(2000);
await goto('/vms'); await page.waitForTimeout(1500);
await page.screenshot({path:'/work/test/e2e/shots/lot2-vms-tree.png', fullPage:false});
// open a VM + Monitor tab
await page.locator('text=web-server-01').first().click().catch(()=>{}); await page.waitForTimeout(1500);
await page.locator('button:has-text("Monitor"),[role="tab"]:has-text("Monitor")').first().click().catch(()=>{}); await page.waitForTimeout(2500);
await page.screenshot({path:'/work/test/e2e/shots/lot2-monitor.png', fullPage:false});
await page.locator('button:has-text("Configure"),[role="tab"]:has-text("Configure")').first().click().catch(()=>{}); await page.waitForTimeout(1500);
await page.screenshot({path:'/work/test/e2e/shots/lot2-configure.png', fullPage:false});
const t=await page.locator('body').innerText();
console.log('tree present:', /Hypervisors|web-server-01/.test(t), '| tabs:', ['Summary','Monitor','Configure','Permissions','Snapshots','Console'].filter(x=>t.includes(x)).join(','));
await b.close(); process.exit(0);
