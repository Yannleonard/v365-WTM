import { chromium } from 'playwright';
const BASE='http://host.docker.internal:8080';
const R=[]; const rec=(n,ok,d='')=>{R.push({n,ok});console.log(`${ok?'PASS':'FAIL'}  ${n}${d?'  :: '+d:''}`);};
const b=await chromium.launch({args:['--no-sandbox']});
const page=await (await b.newContext({viewport:{width:1500,height:950}})).newPage();
const fails=[]; page.on('response',r=>{if(r.status()>=400&&r.url().includes('/api/'))fails.push(`${r.status()} ${r.url().split('/api/v1')[1]}`);});
const goto=async p=>{await page.goto(BASE+p,{waitUntil:'domcontentloaded',timeout:25000});await page.waitForTimeout(1800);};
await goto('/login'); await page.waitForSelector('input#username');
await page.fill('input#username','admin'); await page.fill('input#password','Admin1234567');
await page.locator('button[type="submit"]').first().click(); await page.waitForTimeout(2000);
// new feature views
for (const [label,path,marker] of [
  ['FinOps cost', '/finops', /cost|spend|rate|finops|\$/i],
  ['Insights', '/insights', /insight|severity|recommend|health/i],
  ['Replication', '/replication', /replicat|RPO|failover|policy/i],
  ['Storage Backends', '/storage-backends', /storage|backend|nfs|s3|azure|smb|iscsi/i],
]){
  try{
    fails.length=0;
    await goto(path);
    const t=await page.locator('body').innerText();
    const notFound=/not found|404/i.test(t)&&t.length<400;
    rec(`${label} view loads`, !notFound && marker.test(t) && !fails.some(f=>f.startsWith('5')), notFound?'404':(fails.length?fails.join(','):(marker.test(t)?'ok':'marker missing')));
    await page.screenshot({path:`/work/test/e2e/shots/feat-${label.replace(/\W+/g,'_')}.png`,fullPage:true});
  }catch(e){ rec(`${label} view loads`,false,String(e).slice(0,80)); }
}
// hot-add buttons present on a VM detail
await goto('/vms'); await page.locator('text=web-server-01').first().click().catch(()=>{}); await page.waitForTimeout(1500);
const bodyBtns = await page.$$eval('button',els=>els.map(b=>(b.title||b.getAttribute('aria-label')||b.textContent||'').trim()));
const hasHotAdd = bodyBtns.some(x=>/add disk|add network|mount iso|hotplug/i.test(x)) || bodyBtns.some(x=>/Add disk/i.test(x));
rec('hot-add buttons on VM detail', hasHotAdd, hasHotAdd?'present':('buttons: '+bodyBtns.filter(Boolean).slice(0,20).join('|')));
console.log(`\n===== ${R.filter(r=>r.ok).length}/${R.length} =====`);
await b.close(); process.exit(0);
