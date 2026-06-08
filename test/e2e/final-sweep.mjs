import { chromium } from 'playwright';
const BASE = 'http://host.docker.internal:8080';
const SHOTS = '/work/test/e2e/shots';

const IGNORE = (m) =>
  /translate\.google|gstatic|Content Security Policy|\/auth\/me.*401|favicon/i.test(m);

const ROUTES = [
  ['Dashboard', '/'],
  ['Cost (FinOps)', '/finops'],
  ['Insights', '/insights'],
  ['Alarms', '/alarms'],
  ['Hosts', '/hosts'],
  ['Workloads', '/workloads'],
  ['Marketplace', '/marketplace'],
  ['Stacks', '/stacks'],
  ['Images', '/images'],
  ['Networks', '/networks'],
  ['Volumes', '/volumes'],
  ['Virtual Machines', '/vms'],
  ['Hypervisors', '/vm/connections'],
  ['VM Clusters', '/vm-clusters'],
  ['Resource Pools', '/resource-pools'],
  ['VM Networks', '/vm-networks'],
  ['VM Storage', '/vm-storage'],
  ['Storage Backends', '/storage-backends'],
  ['Migration (V2V)', '/migration'],
  ['Replication (DR)', '/replication'],
  ['VM Backups', '/vm-backups'],
  ['Backups', '/backups'],
  ['Swarm', '/swarm'],
  ['Kubernetes', '/k8s'],
  ['K8s Storage', '/k8s-storage'],
  ['K8s Cluster', '/k8s-cluster'],
  ['Helm', '/helm'],
  ['Audit', '/audit'],
  ['Users', '/users'],
  ['Roles', '/roles'],
  ['Registries', '/registries'],
  ['Catalogs', '/catalogs'],
  ['Authentication', '/authentication'],
  ['Settings', '/settings'],
  ['API Tokens', '/api-tokens'],
];

const b = await chromium.launch({ args: ['--no-sandbox'] });
const ctx = await b.newContext({ viewport: { width: 1500, height: 950 } });
const page = await ctx.newPage();

let consoleErrs = [];
let badApi = [];
page.on('console', (msg) => {
  if (msg.type() === 'error') {
    const t = msg.text();
    if (!IGNORE(t)) consoleErrs.push(t);
  }
});
page.on('pageerror', (e) => {
  if (!IGNORE(String(e))) consoleErrs.push('PAGEERROR: ' + String(e).slice(0, 160));
});
page.on('response', (r) => {
  const u = r.url();
  if (u.includes('/api/') && r.status() >= 400) {
    if (u.includes('/auth/me') && r.status() === 401) return;
    badApi.push(`${r.status()} ${u.split('/api/v1')[1] || u}`);
  }
});

// login
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input#username', { timeout: 20000 });
await page.fill('input#username', 'admin');
await page.fill('input#password', 'Admin1234567');
await page.locator('button[type="submit"]').first().click();
await page.waitForTimeout(2500);

const rows = [];
for (const [label, path] of ROUTES) {
  consoleErrs = [];
  badApi = [];
  let loads = true;
  let bodyText = '';
  try {
    await page.goto(BASE + path, { waitUntil: 'domcontentloaded', timeout: 25000 });
    await page.waitForTimeout(1700);
    bodyText = await page.locator('body').innerText();
    const notFound = /not found|page not found|404/i.test(bodyText) && bodyText.length < 500;
    loads = !notFound;
    // crash / blank detection
    const blank = bodyText.replace(/\s/g, '').length < 30;
    if (blank) loads = false;
  } catch (e) {
    loads = false;
    consoleErrs.push('NAV-ERR: ' + String(e).slice(0, 120));
  }
  const safe = label.replace(/\W+/g, '_');
  await page.screenshot({ path: `${SHOTS}/final-${safe}.png`, fullPage: false }).catch(() => {});
  rows.push({
    route: label,
    path,
    loads,
    cerr: [...new Set(consoleErrs)].slice(0, 4),
    bad: [...new Set(badApi)].slice(0, 6),
  });
}

console.log('=== ROUTE SWEEP ===');
for (const r of rows) {
  const tag =
    r.loads && r.cerr.length === 0 && r.bad.filter((x) => x.startsWith('5')).length === 0
      ? 'CLEAN'
      : 'CHECK';
  console.log(
    `[${tag}] ${r.route} (${r.path}) loads=${r.loads} | cerr=${r.cerr.length ? r.cerr.join(' || ') : 'none'} | badApi=${r.bad.length ? r.bad.join(',') : 'none'}`
  );
}
console.log('=== END SWEEP ===');
await b.close();
process.exit(0);
