const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL} = H.fleet;
const bad=(d)=>d.querySelectorAll('#certificates img,#certificates script,#certificates [onerror],#certificates [onclick],.modal-overlay img,.modal-overlay script,#blacksmith-live-region img').length;
const clearAll=async(w,d)=>{ w.blacksmithCerts.state.attention=false; await w.blacksmithCerts.refresh({announce:true}); await sleep(80); };
t('fleet views and drawer', async () => {
  let {w,d,server,log,errors}=await boot();
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  // ---- timeline ----
  const tl=Array.from(d.querySelectorAll('.cert-tl-btn'));
  ok(tl.length===14,'timeline has 14 buttons: '+tl.length);
  ok(/^Expired, 1 certificate$/.test(tl[0].getAttribute('aria-label')),'expired label: '+tl[0].getAttribute('aria-label'));
  ok(/^Week of \d+ \w+, 1 critical$/.test(tl[1].getAttribute('aria-label')),'week 1 label: '+tl[1].getAttribute('aria-label'));
  ok(/^Week of \d+ \w+, 1 critical$/.test(tl[3].getAttribute('aria-label')),'week 3 label: '+tl[3].getAttribute('aria-label'));
  ok(tl.every(b=>b.getAttribute('aria-pressed')==='false'),'none pressed');
  ok(d.querySelectorAll('#cert-tbody tr.cert-row').length===3,'attention filter still on (3 rows), timeline counts unfiltered');
  tl[1].click(); await sleep(100);
  const st=w.blacksmithCerts.state;
  ok(!!st.expires_after && !!st.expires_before && (Date.parse(st.expires_before)-Date.parse(st.expires_after))===7*86400000,'week range set '+st.expires_after+' .. '+st.expires_before);
  ok(d.querySelectorAll('.cert-tl-btn')[1].getAttribute('aria-pressed')==='true','week 1 pressed');
  ok(/expires_after=/.test(w.blacksmithCerts.toQuery(st)) && /expires_before=/.test(w.blacksmithCerts.toQuery(st)),'query has range');
  const rowsNow=d.querySelectorAll('#cert-tbody tr.cert-row'); ok(rowsNow.length===1,'one row in week 1: '+rowsNow.length);
  const anchorBefore=w.blacksmithCerts.state.expires_after;
  await w.blacksmithCerts.refresh({announce:true}); await sleep(80);
  ok(w.blacksmithCerts.state.expires_after===anchorBefore,'anchor stable across refresh');
  d.querySelectorAll('.cert-tl-btn')[1].click(); await sleep(100);
  ok(!st.expires_after && !st.expires_before && d.querySelectorAll('.cert-tl-btn')[1].getAttribute('aria-pressed')==='false','toggle off clears');
  d.querySelectorAll('.cert-tl-btn')[0].click(); await sleep(100);
  ok(!st.expires_after && !!st.expires_before && d.querySelectorAll('#cert-tbody tr.cert-row').length===1,'expired button: before only, 1 row');
  d.querySelectorAll('.cert-tl-btn')[0].click(); await sleep(100);
  ok(bad(d)===0,'no injected (timeline)');
  // ---- group by owner ----
  server.certs[3].rotation={allowed:true,reasons:[]}; server.certs[6].rotation={allowed:true,reasons:[]};
  await clearAll(w,d);
  const gb=Array.from(d.querySelectorAll('.cert-groupby-btn'));
  ok(gb.length===3 && gb[0].getAttribute('aria-pressed')==='true','group-by buttons, flat default');
  gb[1].click(); await sleep(50);
  let heads=d.querySelectorAll('tr.cert-group-row');
  ok(heads.length===4,'owner groups='+heads.length+' '+Array.from(heads).map(h=>h.textContent.slice(0,25)).join('|'));
  ok(heads[0].querySelector('.cert-group-toggle').getAttribute('aria-expanded')==='true','group expanded');
  ok(heads[0].querySelector('.cert-pill, [class*=pill]')!==null,'group header has pills');
  ok(bad(d)===0,'no injected (owner groups)');
  const tog=d.querySelector('.cert-group-toggle'); const gid=tog.dataset.groupToggle; const before=d.querySelectorAll('#cert-tbody tr.cert-row').length;
  tog.focus(); tog.click(); await sleep(20);
  ok(d.querySelector('[data-group-toggle="'+gid+'"]').getAttribute('aria-expanded')==='false' && d.querySelectorAll('#cert-tbody tr.cert-row').length<before,'collapse hides members');
  ok(d.activeElement && d.activeElement.dataset && d.activeElement.dataset.groupToggle===gid,'focus kept on group toggle');
  d.querySelector('[data-group-toggle="'+gid+'"]').click(); await sleep(20);
  // mixed checkbox
  const grpWith=Array.from(d.querySelectorAll('tr.cert-group-row')).find(h=>h.querySelector('input:not([disabled])'));
  ok(!!grpWith,'a group has an enabled checkbox');
  const gInput=()=>Array.from(d.querySelectorAll('tr.cert-group-row')).find(h=>h.querySelector('input:not([disabled])')).querySelector('input');
  const rowBoxes=Array.from(d.querySelectorAll('#cert-tbody tr.cert-row input:not([disabled])'));
  ok(rowBoxes.length===2,'two selectable rows '+rowBoxes.length);
  rowBoxes[0].checked=true; rowBoxes[0].dispatchEvent(new w.Event('change',{bubbles:true}));
  ok(gInput().indeterminate===true && gInput().checked===false,'group mixed after one row');
  gInput().checked=true; gInput().dispatchEvent(new w.Event('change',{bubbles:true})); await sleep(20);
  ok(gInput().checked===true && gInput().indeterminate===false && w.blacksmithCerts.selection.size===1,'group check selects all, selection size '+w.blacksmithCerts.selection.size);
  ok(Array.from(d.querySelectorAll('#cert-tbody tr.cert-row input:not([disabled])')).every(i=>i.checked),'rows follow group');
  // ---- group by CA ----
  const extra=JSON.parse(JSON.stringify(server.certs[2])); extra.id='orph'; extra.authority_key_id='ZZZ'; extra.issuer='CN=Other CA'+EVIL; extra.expiry='ok'; extra.attention=false; server.certs.push(extra);
  gb[2].click(); await sleep(30); await w.blacksmithCerts.refresh({announce:true}); await sleep(80);
  const labels=Array.from(d.querySelectorAll('tr.cert-group-row .cert-group-name')).map(n=>n.textContent);
  ok(labels.some(l=>l.startsWith('Services CA')) && labels.includes('Self-signed roots') && labels.includes('Issuer not in the inventory'),'CA groups: '+labels.map(l=>l.slice(0,22)).join(' | '));
  const caHead=Array.from(d.querySelectorAll('tr.cert-group-row')).find(h=>h.querySelector('.cert-group-name').textContent.startsWith('Services CA'));
  ok(caHead.querySelector('input')===null,'CA group has no checkbox');
  const orphHead=Array.from(d.querySelectorAll('tr.cert-group-row')).find(h=>h.querySelector('.cert-group-name').textContent==='Issuer not in the inventory');
  ok(!!orphHead && orphHead.textContent.includes('We could not match'),'unmatched group text');
  ok(bad(d)===0,'no injected (CA groups)');
  gb[0].click(); await sleep(30);
  ok(d.querySelectorAll('tr.cert-group-row').length===0 && d.querySelectorAll('#cert-tbody tr.cert-row').length>=10,'back to flat');
  // ---- drawer ----
  w.blacksmithCerts.selection.clear();
  const calls=[]; w.inspectCertificate=(x)=>{calls.push(x);return true;};
  const row=d.querySelector('#cert-tbody tr.cert-row[data-cert-id="c2"]');
  row.focus(); row.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true})); await sleep(100);
  const dr=d.getElementById('cert-fleet-drawer');
  ok(!dr.hidden && dr.getAttribute('role')==='dialog' && dr.getAttribute('aria-modal')==='false','drawer open via Enter, role dialog');
  ok(d.activeElement===d.getElementById('cert-drawer-title'),'focus on heading');
  ok(log.some(l=>l==='GET /b/certificates/items/c2'),'item fetched');
  ok(dr.querySelector('.cert-details-table')!==null && dr.textContent.includes('Subject Alt Names'),'detail table rendered');
  ok(bad(d)===0 && dr.querySelectorAll('img,script,[onerror]').length===0,'no injected (drawer)');
  ok(dr.textContent.includes('"><img src=x'),'hostile text shown literally in drawer');
  ok(dr.textContent.includes('sha256 8f2e1c0a'),'observation fingerprint shown');
  { const rot=dr.querySelector('.cert-drawer-rotate'); const note=d.getElementById('cert-drawer-rotate-note');
    ok(!!rot && rot.disabled,'drawer Rotate is disabled while rotation is off');
    ok(!!note && dr.contains(note) && note.textContent.trim().length>0 && !note.hidden,'disabled drawer Rotate shows its reason as visible text: '+(note&&note.textContent));
    ok(!!rot && rot.getAttribute('aria-describedby')==='cert-drawer-rotate-note','disabled drawer Rotate is described by its note'); }
  ok(!dr.querySelector('.cert-runbook'),'no runbook for a leaf');
  ok(w.blacksmithSurfaces.size()===1,'drawer registered as surface');
  ok(d.querySelector('tr[data-cert-id="c2"]').classList.contains('cert-row--open'),'row marked open');
  Array.from(dr.querySelectorAll('button')).find(b=>b.textContent==='Inspect').click();
  ok(calls.length===1 && typeof calls[0].pemEncoded==='string' && calls[0].subjectAltNames.length===3 && calls[0].subject.startsWith('CN=cn2'),'Inspect passes legacy shape');
  // Escape with another surface on top
  const other={close(){}}; w.blacksmithSurfaces.push(other);
  d.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
  ok(!dr.hidden,'Escape ignored when not top');
  w.blacksmithSurfaces.remove(other);
  const prevented=new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}); d.addEventListener('keydown',function h(e){e.preventDefault();d.removeEventListener('keydown',h,true);},true);
  d.dispatchEvent(prevented); ok(!dr.hidden,'Escape ignored when defaultPrevented');
  d.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
  ok(dr.hidden && w.blacksmithSurfaces.size()===0,'Escape closes and unregisters');
  ok(d.activeElement && d.activeElement.dataset.certId==='c2','focus returns to the row');
  // click opens, close button
  d.querySelector('#cert-tbody tr.cert-row[data-cert-id="c3"] .cert-col-owner').click(); await sleep(80);
  ok(!dr.hidden && d.getElementById('cert-drawer-title').textContent.startsWith('var3'),'row click opens');
  Array.from(dr.querySelectorAll('.cert-drawer-close')).forEach(b=>b.click());
  ok(dr.hidden,'Close button closes');
  d.querySelector('#cert-tbody tr.cert-row[data-cert-id="c3"] .cert-expand-btn').click(); await sleep(20);
  ok(dr.hidden,'expand button does not open drawer');
  // CA runbook
  d.querySelector('#cert-tbody tr.cert-row[data-cert-id="ca1"]').click(); await sleep(80);
  ok(dr.textContent.includes('genesis <env> rotate-secrets broker/ca') && dr.textContent.includes('genesis <env> deploy') && /ekey/.test(dr.textContent),'CA runbook');
  ok(dr.querySelector('.cert-runbook .cert-copy-btn')!==null,'runbook copy button');
  // broker runbook
  d.querySelector('#cert-tbody tr.cert-row[data-cert-id="unk1"]').click(); await sleep(80);
  ok(dr.textContent.includes('rotate-secrets broker/server') && /-P/.test(dr.textContent) && !dr.textContent.includes('broker/ca'),'broker Genesis commands, switched cert');
  ok(w.blacksmithSurfaces.size()===1,'one surface after switching certs');
  // Open instance
  const svc=d.createElement('div'); svc.id='services-fake'; const host=d.getElementById('services'); const item=d.createElement('div'); item.className='service-item'; item.dataset.instanceId='inst1'; let clicked=0; item.addEventListener('click',()=>clicked++); host.appendChild(item);
  d.querySelector('#cert-tbody tr.cert-row[data-cert-id="c1"]').click(); await sleep(80);
  Array.from(dr.querySelectorAll('button')).find(b=>b.textContent==='Open instance').click(); await sleep(400);
  ok(clicked===1 && dr.hidden,'Open instance clicks matching service item and closes drawer, clicked='+clicked);
  // switch tab away closes drawer
  await w.blacksmithNav.switchTab('certificates'); await sleep(100);
  d.querySelector('#cert-tbody tr.cert-row[data-cert-id="c4"]').click(); await sleep(80);
  ok(!dr.hidden,'reopened'); await w.blacksmithNav.switchTab('blacksmith'); await sleep(50);
  ok(dr.hidden && w.blacksmithSurfaces.size()===0,'leaving the tab closes the drawer');
  await w.blacksmithNav.switchTab('certificates'); await sleep(100);
  // item failure
  server.itemFail=true;
  d.querySelector('#cert-tbody tr.cert-row[data-cert-id="c5"]').click(); await sleep(100);
  ok(/item broke/.test(dr.textContent) && dr.querySelector('.cert-retry-button')!==null && d.getElementById('cert-fleet-state').hidden && !d.getElementById('cert-fleet-main').hidden,'item error in drawer only, page intact');
  ok(dr.querySelectorAll('img,script').length===0,'no injected (item error)');
  ok(dr.querySelector('.cert-obs-list')!==null,'list data still shown with item error');
  server.itemFail=false; dr.querySelector('.cert-retry-button').click(); await sleep(100);
  ok(dr.querySelector('.cert-details-table')!==null && !dr.querySelector('.cert-retry-button'),'Retry recovers');
  d.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
  // 404
  const gone=server.certs.find(c=>c.id==='c6'); server.certs=server.certs.filter(c=>c!==gone);
  d.querySelector('#cert-tbody tr.cert-row[data-cert-id="c6"]').click(); await sleep(100);
  ok(/no longer in the inventory/.test(dr.textContent),'404 message');
  d.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
  // refresh with drawer open keeps drawer, focus
  d.querySelector('#cert-tbody tr.cert-row[data-cert-id="c7"]').click(); await sleep(80);
  await w.blacksmithCerts.refresh({announce:true}); await sleep(80);
  ok(!dr.hidden && d.querySelector('tr[data-cert-id="c7"]').classList.contains('cert-row--open'),'refresh keeps drawer and open mark');
  expect(failures).toEqual([]);
}, 120000);
