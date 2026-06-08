import { chromium } from 'playwright';
const BASE = 'http://host.docker.internal:8080';
const SHOTS = '/work/test/e2e/shots';
const b = await chromium.launch({ args: ['--no-sandbox'] });
const page = await (await b.newContext({ viewport: { width: 820, height: 1100 } })).newPage();
const cerr = [];
page.on('console', (m) => { if (m.type() === 'error' && !/translate|gstatic|auth\/me.*401/i.test(m.text())) cerr.push(m.text()); });

await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input#username');
await page.fill('input#username', 'admin');
await page.fill('input#password', 'Admin1234567');
await page.locator('button[type="submit"]').first().click();
await page.waitForTimeout(2500);

// inventory tree / VM list at 820
await page.goto(BASE + '/vms', { waitUntil: 'domcontentloaded' });
await page.waitForTimeout(1800);
await page.screenshot({ path: `${SHOTS}/final-mobile-inventory.png`, fullPage: false });

// VM detail at 820
await page.locator('text=web-server-01').first().click();
await page.waitForTimeout(2000);
await page.screenshot({ path: `${SHOTS}/final-mobile-vmdetail.png`, fullPage: false });

// horizontal overflow check (broken overlap symptom)
const overflow = await page.evaluate(() => ({
  scrollW: document.documentElement.scrollWidth,
  clientW: document.documentElement.clientWidth,
}));
console.log(`viewport=820 scrollW=${overflow.scrollW} clientW=${overflow.clientW} hOverflow=${overflow.scrollW - overflow.clientW}`);
console.log('console errors: ' + (cerr.length ? cerr.join(' | ') : 'none'));
await b.close();
process.exit(0);
