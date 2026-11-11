const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL} = H.main;
const UUID=/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const mk=(id,key,variable,inst,name,days,exp,extra={})=>({id,key,variable,kind:'leaf',days_left:days,expiry:exp,health:'ok',owners:[{kind:'instance',instance_id:inst,instance_name:name}],rotation:{allowed:true,reasons:[]},findings:[],...extra});
t('instance rotation and overview', async () => {
  const env=boot({oat:true}); const {w,d,errors,alerts,calls}=env; const T=w.__T;
  let itemGate=null; let caps={rotation:{enabled:true}}, holdTasks=[], previewStatus=200, createStatus=201;
  const certs=[mk('c1','/s/a_crt','a_crt','inst-a','a '+EVIL,5,'critical'),mk('c2','/s/b_crt','b_crt','inst-a','a '+EVIL,200,'ok'),mk('c3','/s/c_crt','c_crt','inst-b','b',3,'critical')];
  const posts=()=>calls.filter(c=>c.startsWith('POST'));
  const lastPost=(re)=>{const l=posts().filter(c=>re.test(c));return l[l.length-1];};
  const bodyOf=(c)=>JSON.parse(c.slice(c.indexOf('{')));
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],
    [/certificates\/inventory/,(u,o,j)=>j(200,{certificates:certs,capabilities:caps,settings:{warning_days:30,critical_days:7},scan:{state:'idle',completed_at:'2026-10-21T10:00:00Z'},summary:{certificates:3,needs_attention:0},filtered:{certificates:certs.length,affected_instances:2}})],
    [/certificates\/items\//,(u,o,j)=>itemGate?itemGate.then(()=>j(200,{...certs[0],pem:''})):j(200,{...certs[0],pem:''})],
    [/certificates\/instances\/inst-a$/,(u,o,j)=>j(200,{instance:{id:'inst-a',name:'a '+EVIL},scan:{},certificates:certs.slice(0,2),capabilities:caps})],
    [/rotation\/preview$/,(u,o,j)=>previewStatus===200?j(200,{preview_id:'pv1',instances:[{instance_id:'inst-a',instance_name:'a',vms:1,gate:'g',selected:[{name:'/s/a_crt',days_left:5}],additional:[],blocked:null,impact:'Restarts.'}],warnings:[]}):j(previewStatus,{error:'boom '+EVIL,code:'rotation_disabled'})],
    [/\/b\/upgrade\/tasks\/[^/]+$/,(u,o,j)=>j(200,holdTasks[0])],
    [/\/b\/upgrade\/tasks$/,(u,o,j)=>o.method==='POST'?j(createStatus,{id:'t-new',name:'n',kind:'certificates',status:'pending'}):j(200,holdTasks.map(t=>({id:t.id,status:t.status,kind:t.kind,name:t.name})))]);
  w.serviceInstances={}; await sleep(50); w.plansData={services:[]};
  // The sub-tab strip of the instance, which the rotation watch listens to.
  d.body.insertAdjacentHTML('beforeend','<bs-tabs id="watch-strip"><div role="tablist" aria-label="Fixture" id="service-cert-tablist-inst-a"><button type="button" role="tab" data-tab="one">One</button><button type="button" role="tab" data-tab="two">Two</button></div><div role="tabpanel">One</div><div role="tabpanel">Two</div></bs-tabs>');
  await sleep(20);
  const watchTabs=[...d.querySelectorAll('#watch-strip [role=tab]')];
  const R=w.blacksmithRotate;
  ok(!!R&&typeof R.openInstanceDialog==='function'&&typeof R.readHolders==='function'&&typeof R.availability==='function','blacksmithRotate exposes dialog, holders, availability');
  ok(!!w.blacksmithBatch&&typeof w.blacksmithBatch.openRotate==='function','blacksmithBatch.openRotate exists');
  const waitDialog=async()=>{ for(let i=0;i<50;i++){ if(d.querySelector('#dialog-modal[open]')) return true; await sleep(10);} return false; };
  const confirmBtn=()=>d.querySelector('#dialog-modal [data-dialog-role="confirm"]');
  const cancelBtn=()=>d.querySelector('#dialog-modal [data-dialog-role="cancel"]');
  const live=()=>d.getElementById('blacksmith-live-region').textContent;

  // ---- availability
  const A=R.availability;
  ok(A({rotation:{enabled:false,reason:'Switched off'}},3,'').state==='disabled'&&A({rotation:{enabled:false,reason:'Switched off'}},3,'').note==='Switched off','availability: rotation off gives the reason');
  ok(A(caps,3,'rotation "x"').label==='Busy'&&A(caps,3,'rotation "x"').note.includes('rotation "x"'),'availability: held instance reads Busy with the holder');
  ok(A(caps,0,'').state==='disabled'&&A(caps,0,'').note.length>0,'availability: nothing rotatable gives a reason');
  ok(A(caps,2,'').state==='enabled','availability: enabled');

  // ---- readHolders
  holdTasks=[{id:'tk',name:'N',kind:'certificates',status:'running',instances:[{instance_id:'inst-b',status:'running'}]}];
  const h=await R.readHolders();
  ok(h.get('inst-b')&&h.get('inst-b').includes('rotation'),'readHolders reports the held instance');

  // ---- instance overview
  const host=d.createElement('div'); d.body.appendChild(host);
  let threw=null; try{ await T.CertOverview.mountInstance(host,'inst-a'); }catch(e){threw=e;}
  await sleep(50);
  ok(!threw,'overview mounts without throwing '+(threw&&threw.message));
  let rb=host.querySelector('.cert-ov-rotate');
  ok(rb&&!rb.disabled&&rb.textContent==='Rotate...','overview: Rotate enabled when rotation is on and a certificate is eligible');
  rb.focus(); rb.click(); await sleep(20);
  const dlg=host.querySelector('.cert-rotate-dialog');
  ok(!!dlg,'overview: Rotate opens the dialog');
  const boxes=[...dlg.querySelectorAll('input[type=checkbox]')];
  ok(boxes.length===2&&boxes[0].getAttribute('aria-label')==='Rotate a_crt on a '+EVIL&&boxes[0].checked&&!boxes[1].checked,'overview: first open preselects only certificates under the threshold, labels name the instance');
  ok(dlg.querySelectorAll('img,script').length===0&&dlg.textContent.includes(EVIL),'overview: hostile instance name is inert text');
  const patch=dlg.querySelector('input[value=patch]'); const redeploy=dlg.querySelector('input[value=redeploy]');
  ok(patch.disabled&&redeploy.checked&&dlg.textContent.includes('not available'),'overview: Redeploy default, Patch disabled with a reason');
  const prev=dlg.querySelector('.cert-rotate-preview');
  boxes[0].checked=false; boxes[0].dispatchEvent(new w.Event('change',{bubbles:true}));
  ok(prev.disabled&&dlg.querySelector('.cert-rotate-hint').textContent.includes('Select at least one'),'overview: Preview disabled with visible reason when nothing is selected');
  boxes[0].checked=true; boxes[0].dispatchEvent(new w.Event('change',{bubbles:true}));
  boxes[1].checked=true; boxes[1].dispatchEvent(new w.Event('change',{bubbles:true}));
  dlg.querySelector('.cert-rotate-reason input').value='  because '+EVIL+' ';
  // error mapping: preview fails
  previewStatus=403;
  prev.click(); await sleep(120);
  const pb=bodyOf(lastPost(/rotation\/preview( |$)/));
  ok(pb.mode==='redeploy'&&pb.selections.length===1&&pb.selections[0].instance_id==='inst-a'&&pb.selections[0].variables.join()==='/s/a_crt,/s/b_crt','preview body: redeploy, instance, chosen full variable names');
  const panel=host.querySelector('.cert-rotate-result [role=alert], .cert-rotate-result [tabindex]')||host.querySelector('.cert-rotate-result');
  ok(host.querySelector('.cert-rotate-result').children.length>0&&live().includes('The preview failed'),'error: result panel shown and announced');
  ok(host.contains(d.activeElement)&&d.activeElement!==prev&&host.querySelector('.cert-rotate-result').contains(d.activeElement),'error: focus stays on the result panel: '+(d.activeElement&&d.activeElement.className));
  ok(host.querySelector('.cert-rotate-result').querySelectorAll('img,script').length===0,'error: hostile server text inert');
  // cancel path
  previewStatus=200;
  host.querySelector('.cert-rotate-preview').focus();
  host.querySelector('.cert-rotate-preview').click(); await waitDialog();
  cancelBtn().click(); await sleep(80);
  const np=posts().length;
  ok(d.activeElement===host.querySelector('.cert-rotate-preview')&&!posts().some(c=>/\/b\/upgrade\/tasks( |$)/.test(c)),'cancel: no create posted, focus back on Preview rotation');
  // success path
  host.querySelector('.cert-rotate-preview').click(); await waitDialog();
  confirmBtn().click(); await sleep(150);
  const cb=bodyOf(lastPost(/\/b\/upgrade\/tasks( |$)/));
  ok(cb.kind==='certificates'&&cb.preview_id==='pv1'&&UUID.test(cb.client_request_id)&&cb.reason==='because '+EVIL,'create body: kind, preview_id, client_request_id, trimmed reason');
  ok(!host.querySelector('.cert-rotate-form')&&host.querySelector('.cert-rotate-result').children.length>0,'success: form removed, result panel kept');
  ok(d.activeElement===host.querySelector('.cert-ov-rotate'),'success: focus returns to the Rotate control');
  // reopen keeps choices
  host.querySelector('.cert-ov-rotate').click(); await sleep(20);
  const again=[...host.querySelectorAll('.cert-rotate-dialog input[type=checkbox]')];
  ok(again.length===2&&again[0].checked&&again[1].checked,'reopen keeps what the operator chose');
  host.querySelector('.cert-rotate-cancel').click();
  ok(!host.querySelector('.cert-rotate-dialog')&&d.activeElement===host.querySelector('.cert-ov-rotate'),'Cancel closes the dialog and returns focus to the opener');

  // overview refresh after the rotation ends
  { let st='running'; w.__routes.unshift([/\/b\/upgrade\/tasks\/t-new$/,(u,o,j)=>j(200,{id:'t-new',status:st})]);
    const instGets=()=>calls.filter(c=>/GET .*certificates\/instances\/inst-a$/.test(c)).length;
    const g0=instGets(); const tg0=calls.filter(c=>/GET .*tasks\/t-new$/.test(c)).length;
    await sleep(30); ok(instGets()===g0&&calls.filter(c=>/GET .*tasks\/t-new$/.test(c)).length===tg0,'overview: no polling while the operator stays on the page');
    w.document.dispatchEvent(new w.Event('visibilitychange')); await sleep(60);
    ok(calls.filter(c=>/GET .*tasks\/t-new$/.test(c)).length===tg0+1&&instGets()===g0,'overview: returning reads the task once and keeps the view while it runs');
    watchTabs[1].click(); await sleep(60);
    ok(calls.filter(c=>/GET .*tasks\/t-new$/.test(c)).length===tg0+2&&instGets()===g0,'overview: changing the sub-tab reads the task once and keeps the view while it runs');
    st='completed'; w.document.dispatchEvent(new w.Event('visibilitychange')); await sleep(120);
    ok(instGets()===g0+1,'overview: returning after the task ended scans the instance again');
    const tg1=calls.filter(c=>/GET .*tasks\/t-new$/.test(c)).length;
    w.document.dispatchEvent(new w.Event('visibilitychange')); await sleep(60);
    ok(calls.filter(c=>/GET .*tasks\/t-new$/.test(c)).length===tg1,'overview: the watch is gone once it refreshed');
    watchTabs[0].click(); await sleep(60);
    ok(calls.filter(c=>/GET .*tasks\/t-new$/.test(c)).length===tg1,'overview: a sub-tab change after the watch ended reads nothing');
    w.__routes.shift(); }
  // overview busy / off
  holdTasks=[{id:'tk',name:'N',kind:'certificates',status:'running',instances:[{instance_id:'inst-a',status:'running'}]}];
  const host2=d.createElement('div'); d.body.appendChild(host2);
  await T.CertOverview.mountInstance(host2,'inst-a'); await sleep(50);
  rb=host2.querySelector('.cert-ov-rotate');
  ok(rb.disabled&&rb.textContent==='Busy'&&host2.querySelector('.cert-ov-rotate-note').textContent.includes('rotation "N"'),'overview: held instance shows Busy with the holder');
  holdTasks=[]; caps={rotation:{enabled:false,reason:'Rotation is not enabled'}};
  const host3=d.createElement('div'); d.body.appendChild(host3);
  await T.CertOverview.mountInstance(host3,'inst-a'); await sleep(50);
  rb=host3.querySelector('.cert-ov-rotate');
  ok(rb.disabled&&host3.querySelector('.cert-ov-rotate-note').textContent==='Rotation is not enabled'&&rb.getAttribute('aria-describedby')==='cert-ov-rotate-note','overview: rotation off shows the reason');
  caps={rotation:{enabled:true}};

  // ---- fleet
  d.getElementById('certificates').classList.add('active');
  await T.CertsFleet.init(); await sleep(150);
  const rs=d.querySelector('.cert-rotate-selected');
  ok(!!rs&&rs.disabled&&d.querySelector('.cert-rotate-selected-note').textContent.includes('Select certificates'),'fleet: Rotate selected disabled with a visible reason when nothing is selected');
  const sel=w.blacksmithCerts.selection; sel.clear();
  const cbxs=[...d.querySelectorAll('.cert-row input[type=checkbox]')]; cbxs[0].click(); cbxs[2].click();
  await sleep(30);
  ok(!rs.disabled&&d.querySelector('.cert-rotate-selected-note').textContent==='','fleet: Rotate selected enabled with a selection');
  rs.click(); await sleep(150);
  ok(d.getElementById('upgrade').dataset.operation==='rotate'||(T.rotateState&&true),'fleet: Rotate selected opens Batch in rotate mode');
  ok(d.activeElement&&d.activeElement.id==='rotate-op-rotate','fleet: focus lands on the rotate mode radio');
  const imported=T.rotateSelection();
  ok(imported&&imported.size===2&&[...imported.get('inst-a')].join()==='/s/a_crt'&&[...imported.get('inst-b')].join()==='/s/c_crt','fleet: Batch holds exactly the shared selection');
  caps={rotation:{enabled:false,reason:'Off for now'}};
  await w.blacksmithCerts.refresh({silent:true}); await sleep(100);
  ok(d.querySelector('.cert-rotate-selected').disabled&&d.querySelector('.cert-rotate-selected-note').textContent.includes('Off for now'),'fleet: rotation off disables Rotate selected with the reason');
  caps={rotation:{enabled:true}};
  await w.blacksmithCerts.refresh({silent:true}); await sleep(100);

  // ---- drawer
  w.blacksmithCerts.selection.clear();
  let openGate; itemGate=new Promise(r=>{openGate=r;});
  const row=d.querySelector('.cert-row'); row.click(); await sleep(150);
  const dr=d.querySelector('.cert-drawer-rotate');
  ok(dr&&!dr.disabled&&dr.textContent==='Rotate...','drawer: Rotate enabled for a rotatable certificate');
  dr.focus(); dr.click(); await sleep(150);
  ok(!d.querySelector('.cert-fleet-drawer .cert-rotate-dialog'),'drawer: Rotate opens no dialog of its own');
  const hand=w.blacksmithCerts.selection;
  ok(hand.size===1&&[...hand.get('inst-a')].join()==='/s/a_crt','drawer: this certificate is the whole shared selection');
  ok(d.getElementById('upgrade').dataset.operation==='rotate','drawer: Rotate opens Batch in rotate mode');
  itemGate=null; openGate(); previewStatus=200;
  ok(alerts.length===0,'no alert, confirm, or prompt used');
  ok(errors.filter(e=>!/^Error loading data: /.test(e)).length===0,'no console errors '+errors.filter(e=>!/^Error loading data: /.test(e)).join('|'));
  expect(failures).toEqual([]);
}, 120000);
