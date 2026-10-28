const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL} = H.fleet;
const bad=(d)=>d.querySelectorAll('#certificates img,#certificates script,#certificates [onerror],#certificates [onclick],.modal-overlay img,.modal-overlay script,#blacksmith-live-region img').length;
t('fleet tab', async () => {
  // ---- normal ----
  let {w,d,server,log,errors}=await boot();
  const badge=d.getElementById('certificates-badge');
  ok(!badge.hidden && badge.textContent==='3' && /3 certificates need attention/.test(badge.getAttribute('aria-label')),'badge on load: '+badge.textContent+' / '+badge.getAttribute('aria-label'));
  ok(!!w.blacksmithCerts && typeof w.blacksmithCerts.toQuery==='function' && w.blacksmithCerts.selection instanceof w.Map,'window.blacksmithCerts shape');
  ok(w.blacksmithCerts.toQuery(w.blacksmithCerts.state)==='' ,'toQuery empty');
  await w.blacksmithNav.switchTab('certificates'); await sleep(100);
  ok(d.getElementById('certificates').classList.contains('active'),'tab active');
  ok(w.blacksmithCerts.state.attention===true,'default attention filter');
  let rows=d.querySelectorAll('#cert-tbody tr.cert-row');
  ok(rows.length===3,'attention rows='+rows.length);
  ok(bad(d)===0,'no injected elements after render');
  ok(/CredHub is not configured, so 1 certificate shows an unknown expiry/.test(d.getElementById('cert-fleet-notices').textContent),'capability notice: '+d.getElementById('cert-fleet-notices').textContent.slice(0,120));
  ok(rows[0].textContent.includes(EVIL.slice(0,10))===true,'hostile text literal');
  // pressed
  const btn=(label)=>Array.from(d.querySelectorAll('.cert-strip-btn')).find(b=>b.textContent.startsWith(label));
  btn('Critical').click(); await sleep(80);
  ok(w.blacksmithCerts.state.expiry.join()==='critical' && btn('Critical').getAttribute('aria-pressed')==='true','critical pressed');
  btn('Warning').click(); await sleep(80);
  ok(w.blacksmithCerts.state.expiry.join()==='critical,warning','critical+warning');
  ok(/expiry=critical&expiry=warning/.test(w.blacksmithCerts.toQuery(w.blacksmithCerts.state)),'query '+w.blacksmithCerts.toQuery(w.blacksmithCerts.state));
  // chips
  let chips=d.querySelectorAll('.cert-chip'); ok(chips.length===3,'chips='+chips.length);
  chips[0].click(); await sleep(80);
  ok(w.blacksmithCerts.state.expiry.join()==='warning','chip removes own filter');
  d.querySelector('.cert-clear-btn').click(); await sleep(80);
  ok(w.blacksmithCerts.toQuery(w.blacksmithCerts.state)==='' && d.querySelectorAll('.cert-chip').length===0,'clear filters');
  ok(d.querySelectorAll('#cert-tbody tr.cert-row').length===10,'all rows='+d.querySelectorAll('#cert-tbody tr.cert-row').length);
  // service narrow
  const sel=d.getElementById('cert-filter-service'); ok(sel.options.length>=2,'service options '+sel.options.length);
  sel.value='valkey'; sel.dispatchEvent(new w.Event('change')); await sleep(80);
  ok(/service=valkey/.test(w.blacksmithCerts.toQuery(w.blacksmithCerts.state)),'service query');
  // expand
  const ex=d.querySelector('.cert-expand-btn'); ex.click(); await sleep(10);
  const det=d.querySelector('.cert-row-detail');
  ok(!!det && det.querySelectorAll('.cert-probe-row').length===1 && !!det.querySelector('.cert-drawer-credhub'),'expanded CredHub details and listener observation');
  ok(d.querySelector('.cert-expand-btn[aria-expanded="true"]')!==null,'aria-expanded');
  ok(bad(d)===0,'no injected after expand');
  // search + / key
  d.body.focus(); d.dispatchEvent(new w.KeyboardEvent('keydown',{key:'/',bubbles:true,cancelable:true}));
  ok(d.activeElement===d.getElementById('cert-search'),'/ focuses search');
  const s=d.getElementById('cert-search'); s.value='var3'; s.dispatchEvent(new w.Event('input')); await sleep(450);
  ok(w.blacksmithCerts.state.q==='var3','search state');
  // scan
  w.blacksmithCerts.state.q=''; s.value='';
  d.getElementById('cert-scan-btn').click(); await sleep(100);
  ok(log.some(l=>l==='POST /b/certificates/scan'),'scan posted');
  ok(/Scanning 1 of 5/.test(d.getElementById('cert-fleet-summary').textContent),'scanning summary: '+d.getElementById('cert-fleet-summary').textContent);
  await sleep(100);
  ok(/Scanning 1 of 5 instances/.test(d.getElementById('blacksmith-live-region').textContent),'live region progress: '+d.getElementById('blacksmith-live-region').textContent);
  // empty state
  w.blacksmithCerts.state.q='zzzz-nothing'; await w.blacksmithCerts.refresh({announce:true}); await sleep(80);
  ok(/No certificates match these filters/.test(d.getElementById('cert-empty').textContent),'empty state');
  // export links
  ok(/format=csv/.test(d.getElementById('cert-export-csv').getAttribute('href')),'export href');
  // second visit does not break
  await w.blacksmithNav.switchTab('blacksmith'); await w.blacksmithNav.switchTab('certificates'); await sleep(50);
  ok(d.querySelectorAll('#cert-fleet').length===1,'single shell after revisit');

  // ---- first scan failed ----
  ({w,d,server}=await boot()); server.mode='first-failed';
  await w.blacksmithNav.switchTab('certificates'); await sleep(100);
  ok(!d.getElementById('cert-fleet-state').hidden && /first certificate scan failed/.test(d.getElementById('cert-fleet-state').textContent) && d.getElementById('cert-fleet-main').hidden,'first scan error panel');
  ok(d.getElementById('cert-fleet-summary').textContent==='' && !!d.querySelector('.cert-retry-button'),'no counts, retry button');
  ok(d.querySelector('.cert-state-error').getAttribute('data-variant')==='danger' && d.querySelector('.cert-state-error').getAttribute('role')==='alert','first scan panel is a danger alert');
  ok(bad(d)===0,'no injected (first-error)');
  // ---- disabled ----
  ({w,d,server}=await boot()); server.mode='disabled';
  await w.blacksmithNav.switchTab('certificates'); await sleep(100);
  ok(/certificates\.inventory\.enabled/.test(d.getElementById('cert-fleet-state').textContent) && d.getElementById('certificates-badge').hidden,'disabled panel + no badge');
  ok(d.querySelector('.cert-state-disabled').getAttribute('role')==='status' && !d.querySelector('.cert-state-disabled').hasAttribute('data-variant'),'disabled panel is a plain status');
  // ---- request failure ----
  ({w,d,server}=await boot()); server.mode='fail';
  await w.blacksmithNav.switchTab('certificates'); await sleep(100);
  ok(/boom/.test(d.getElementById('cert-fleet-state').textContent) && !!d.querySelector('#cert-fleet-state .cert-retry-button') && bad(d)===0,'load error panel with retry');
  // ---- skeleton ----
  ({w,d,server}=await boot()); server.mode='never';
  await w.blacksmithNav.switchTab('certificates'); await sleep(100);
  ok(d.querySelectorAll('.cert-skeleton-row').length>0 && /Scanning 12 of 58/.test(d.getElementById('cert-fleet-summary').textContent),'skeleton rows + header');
  // ---- stale banner + degraded ----
  ({w,d,server}=await boot());
  server.scan=Object.assign({},server.scan,{errors:[{instance_id:'inst1',error:'scan timed out'+EVIL,last_success_at:new Date(Date.now()-7200e3).toISOString()}]});
  server.caps=Object.assign({},server.caps,{rotation:{enabled:false,reason:'BOSH director unavailable'},director:{version:''},credhub:{state:'not_configured'}});
  await w.blacksmithNav.switchTab('certificates'); await sleep(100);
  const notices=d.getElementById('cert-fleet-notices').textContent;
  ok(/1 instance could not be scanned\. Showing data from/.test(notices) && /Show them/.test(notices),'stale banner');
  ok(/BOSH director is unavailable/.test(notices),'degraded notice');
  const infos=Array.from(d.querySelectorAll('#cert-fleet-notices .cert-notice--info'));
  ok(infos.length===2 && infos.every(n=>n.getAttribute('role')==='status' && !n.hasAttribute('data-variant')),'credhub and director info notices are status regions: '+infos.map(n=>n.getAttribute('role')).join());
  Array.from(d.querySelectorAll('.cert-link-btn')).find(b=>b.textContent==='Show them').click(); await sleep(80);
  ok(w.blacksmithCerts.state.instance.join()==='inst1','Show them filters instance');
  ok(bad(d)===0,'no injected (stale)');
  expect(failures).toEqual([]);
}, 120000);
