import { chromium } from 'playwright';
const BASE='http://host.docker.internal:8080';
const b=await chromium.launch({args:['--no-sandbox']});
const page=await (await b.newContext({viewport:{width:1400,height:900}})).newPage();
const goto=async p=>{await page.goto(BASE+p,{waitUntil:'domcontentloaded',timeout:25000});await page.waitForTimeout(1800);};
await goto('/login'); await page.waitForSelector('input#username');
await page.fill('input#username','admin'); await page.fill('input#password','Admin1234567');
await page.locator('button[type="submit"]').first().click(); await page.waitForTimeout(2000);
await goto('/vms');
await page.locator('text=alpine-boot-test').first().click().catch(()=>{}); await page.waitForTimeout(1500);
await page.locator('button:has-text("Console"),[role="tab"]:has-text("Console")').first().click().catch(()=>{});
await page.waitForTimeout(6000); // let guacamole connect + Alpine draw
await page.screenshot({path:'/work/test/e2e/shots/iso-boot.png', fullPage:true});
const canvas = await page.locator('canvas').count();
console.log('console canvas:', canvas);
await b.close(); process.exit(0);
