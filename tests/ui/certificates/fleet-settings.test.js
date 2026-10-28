const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL} = H.fleet;
const bad=(d)=>d.querySelectorAll('#certificates img,#certificates script,#certificates [onerror],.modal-overlay img,.modal-overlay script,dialog img,dialog script,#blacksmith-live-region img,#toast-container img').length;
const fire=(w,el,type)=>el.dispatchEvent(new w.Event(type,{bubbles:true,cancelable:true}));
const submit=async(w,d)=>{ const f=d.querySelector('#cert-settings-modal form'); f.dispatchEvent(new w.Event('submit',{bubbles:true,cancelable:true})); await sleep(80); };
const setv=(d,k,v)=>{ d.getElementById('cert-setting-'+k).value=String(v); };
const text=(d,id)=>d.getElementById(id)?d.getElementById(id).textContent:'';
t('fleet settings and coverage line', async () => {
  let {w,d,server,log,errors}=await boot();
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  // ---- reassurance with problems present ----
  ok(!!d.querySelector('#cert-horizon table') && !/Nothing expires/.test(d.getElementById('cert-fleet-notices').textContent),'the horizon table renders beside expired, critical, and warning rows');
  // ---- settings modal ----
  const sb=d.getElementById('cert-settings-btn'); ok(!!sb && !sb.hidden,'Settings button');
  sb.focus(); sb.click(); await sleep(100);
  const modal=d.getElementById('cert-settings-modal'); ok(!!modal && modal.open && modal.tagName==='DIALOG' && !modal.hasAttribute('aria-hidden'),'modal open');
  const inputs=modal.querySelectorAll('input'); ok(inputs.length===8,'8 fields: '+inputs.length);
  ok(Array.from(inputs).every(i=>modal.querySelector('label[for="'+i.id+'"]')),'every field has a label');
  ok(Array.from(inputs).every(i=>(i.getAttribute('aria-describedby')||'').split(' ').every(id=>d.getElementById(id))),'describedby ids resolve');
  const helpText=modal.textContent;
  ok(/at least 5/.test(helpText) && /from 1 to 16/.test(helpText) && /from 5 to 60/.test(helpText) && /below the warning threshold/.test(helpText),'range help text visible');
  ok(d.getElementById('cert-setting-warning_days').value==='30' && d.getElementById('cert-setting-probe_enabled').checked===true,'values prefilled');
  ok(d.activeElement===d.getElementById('cert-setting-warning_days'),'focus in first field');
  // ---- seven rejections ----
  const before=JSON.stringify(server.settings);
  const cases=[['warning_days',0],['critical_days',0],['critical_days',30],['scan_interval_minutes',4],['scan_concurrency',17],['instance_scan_timeout_seconds',0],['preview_ttl_minutes',4]];
  let rejected=0;
  for(const [k,v] of cases){
    // reset form values
    const cur=JSON.parse(before); Object.keys(cur).forEach(key=>{ const i=d.getElementById('cert-setting-'+key); if(i.type==='checkbox') i.checked=cur[key]; else i.value=String(cur[key]); });
    setv(d,k,v); await submit(w,d);
    const err=d.getElementById('cert-setting-'+k+'-error'); const inp=d.getElementById('cert-setting-'+k);
    const good=!err.hidden && err.getAttribute('role')==='alert' && err.textContent.startsWith(k+' ') && inp.getAttribute('aria-invalid')==='true' && (inp.getAttribute('aria-describedby')||'').includes(err.id) && JSON.stringify(server.settings)===before && !!d.getElementById('cert-settings-modal') && d.activeElement===inp;
    ok(good,'rejected '+k+'='+v+' with a field error'); if(good) rejected++;
  }
  ok(rejected===7,'7/7 rejected beside their fields, nothing saved ('+rejected+')');
    // errors clear on edit
  setv(d,'preview_ttl_minutes',15); fire(w,d.getElementById('cert-setting-preview_ttl_minutes'),'input');
  ok(d.getElementById('cert-setting-preview_ttl_minutes-error').hidden && !d.getElementById('cert-setting-preview_ttl_minutes').hasAttribute('aria-invalid'),'error clears on edit');
  // local non-numeric
  setv(d,'scan_concurrency',''); const putsBefore=log.filter(l=>l.startsWith('PUT')).length; await submit(w,d);
  ok(/scan_concurrency must be a whole number/.test(text(d,'cert-setting-scan_concurrency-error')),'blank number rejected locally');
  setv(d,'scan_concurrency',4);
  // unknown-field hostile server error -> general alert, textContent
  server.putStatus={status:500,body:{success:false,error:'boom '+EVIL,code:'internal'}};
  await submit(w,d);
  const gen=modal.querySelector('form > .cert-settings-error');
  ok(!gen.hidden && gen.textContent.includes('boom') && bad(d)===0,'general error shown as text, no injection');
  server.putStatus={status:403,body:{success:false,error:'forbidden',code:'forbidden'}}; await submit(w,d);
  ok(modal.querySelector('form > .cert-settings-error').textContent==='forbidden' && !!d.getElementById('cert-settings-modal'),'403 on save keeps modal open with message');
  server.putStatus=null;
  // ---- successful save moves leaves to Warning ----
  server.recompute=true;
  // c3 is 24 days, c4 33: raise warning to 45 => c4 becomes warning
  const inv0=log.filter(l=>l.startsWith('GET /b/certificates/inventory')).length;
  setv(d,'warning_days',45); setv(d,'critical_days',20); await submit(w,d); await sleep(150);
  ok(!d.getElementById('cert-settings-modal') || !d.getElementById('cert-settings-modal').open,'modal closed after save');
  ok(server.settings.warning_days===45 && server.settings.critical_days===20,'server holds new settings');
  ok(d.body.textContent.includes('Certificate settings saved.'),'success toast text in the page');
  ok(log.filter(l=>l.startsWith('GET /b/certificates/inventory')).length>inv0,'inventory re-fetched');
  ok(d.activeElement===sb,'focus returns to Settings button');
  w.blacksmithCerts.state.attention=false; await w.blacksmithCerts.refresh({announce:true}); await sleep(80);
  const strip=Array.from(d.querySelectorAll('.cert-strip-btn')).find(b=>b.textContent.startsWith('Warning'));
  ok(strip.querySelector('.cert-strip-count').textContent==='3','Warning count moved from 2 to 3 (days 24,33,42 between 20 and 45): '+strip.querySelector('.cert-strip-count').textContent);
  ok(w.CertView.displayExpiry(new Date(Date.now()+40*86400000).toISOString()).status==='warning','pill thresholds updated in CertView');
  // ---- 403 on GET settings ----
  server.settingsGet={status:403,body:{success:false,error:'settings forbidden'+EVIL,code:'forbidden'}};
  sb.click(); await sleep(100);
  const m2=d.getElementById('cert-settings-modal');
  ok(!!m2 && /settings forbidden/.test(m2.textContent) && !!m2.querySelector('.cert-retry-button') && bad(d)===0,'GET 403 shows error with Retry in the modal');
  server.settingsGet=null; m2.querySelector('.cert-retry-button').click(); await sleep(100);
  ok(m2.querySelectorAll('input').length===8,'Retry loads the form');
  d.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true})); await sleep(50);
  ok(!m2.open && w.blacksmithSurfaces.size()===0,'Escape closes the modal');
  ok(d.activeElement===sb,'focus back on Settings');
  // cancel
  sb.click(); await sleep(80); Array.from(d.querySelectorAll('#cert-settings-modal .cert-settings-actions button')).find(b=>b.textContent==='Cancel').click(); await sleep(50);
  ok(!d.getElementById('cert-settings-modal')||!d.getElementById('cert-settings-modal').open,'Cancel closes');

  // ---- reassurance variants ----
  const mk=(boot0,ex)=>boot0;
  const day=86400000;
  const clean=(server)=>{ server.recompute=true; server.settings.warning_days=30; server.settings.critical_days=7;
    server.certs.forEach(c=>{ c.days_left=100; c.effective_not_after=new Date(Date.now()+100*day).toISOString(); c.expiry='ok'; c.attention=false; c.health='ok'; c.coverage={expected_listeners:1,observed_listeners:1,failed_listeners:0,complete:true,stale:false}; c.observations.forEach(o=>{o.state='ok';}); });
    server.certs.pop(); // drop the unknown-expiry cert
    server.caps.credhub={state:'configured'}; };
  const warnBefore=2;
  // A. complete coverage -> strong claim
  ({w,d,server}=await boot()); clean(server);
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  let r=d.querySelector('#cert-horizon .cert-horizon-gaps');
  ok(!!d.getElementById('cert-horizon') && !r,'A complete coverage shows no gap line: '+(r&&r.textContent));
  ok(!/Nothing expires/.test(d.getElementById('certificates').textContent),'A no reassurance sentence');
  // B. unknown expiry + failed listener -> weak claim, exact sentence
  ({w,d,server}=await boot()); clean(server);
  server.certs.slice(0,4).forEach(c=>{ c.expiry='unknown'; delete c.days_left; c.effective_not_after=undefined; });
  server.certs[5].coverage.failed_listeners=1; server.certs[5].coverage.complete=false; server.certs[5].observations[1].state='failed';
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  r=d.querySelector('#cert-horizon .cert-horizon-gaps');
  ok(!!r && r.textContent==='These counts cover what we could observe. 4 certificates have an unknown expiry and 1 listener did not answer.','B gap line: '+(r&&r.textContent));
  ok(bad(d)===0,'no injected (reassurance B)');
  // C. CredHub absent -> never the strong claim
  ({w,d,server}=await boot()); clean(server); server.caps.credhub={state:'not_configured',reason:'none'};
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  r=d.querySelector('#cert-horizon .cert-horizon-gaps');
  ok(!!r && /^These counts cover what we could observe\./.test(r.textContent) && /CredHub is not available/.test(r.textContent),'C CredHub absent: '+(r&&r.textContent));
  // D. stale instance -> weak
  ({w,d,server}=await boot()); clean(server); server.scan=Object.assign({},server.scan,{errors:[{instance_id:'i1',error:'x'+EVIL,last_success_at:new Date().toISOString()}]});
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  r=d.querySelector('#cert-horizon .cert-horizon-gaps'); ok(!!r && /1 instance could not be scanned/.test(r.textContent) && /^These counts cover what we could observe/.test(r.textContent),'D unscanned instance: '+(r&&r.textContent));
  // E. first scan failed -> no line
  ({w,d,server}=await boot()); server.mode='first-failed';
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  ok(!d.getElementById('cert-horizon'),'E no horizon table on first-scan failure');
  // F. disabled -> settings button hidden
  ({w,d,server}=await boot()); server.mode='disabled';
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  ok(d.getElementById('cert-fleet-actions').hidden && !d.getElementById('cert-horizon'),'F disabled: actions (incl. Settings) hidden');
  // G. the configured warning window no longer appears in any sentence
  ({w,d,server}=await boot()); clean(server); server.settings.warning_days=45;
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  ok(!!d.getElementById('cert-horizon') && !/45 days/.test(d.getElementById('cert-fleet-notices').textContent),'G the table ignores the configured window');
  expect(failures).toEqual([]);
}, 120000);
