const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL} = H.main;
const K='/secret/dir/valkey_crt', K2='/secret/dir/redis_cluster_crt', KP='/secret/dir/pg_crt';
const cert=(key,variable,days,id,name,modes,reasons,more={})=>({key,variable,kind:'leaf',days_left:days,owners:[{kind:'instance',instance_id:id,instance_name:name}],rotation:{allowed:true,modes,reasons},findings:[],...more});
let caps={rotation:{enabled:true},patch_in_place:{enabled:true}};
let certs=[cert(K,'valkey_crt',10,'inst-valkey','orders-cache',['redeploy','patch'],[]),cert(K2,'redis_cluster_crt',200,'inst-valkey','orders-cache',['redeploy','patch'],[]),cert(KP,'pg_crt',5,'inst-pg','billing-db',['redeploy'],['No health gate exists for this service, so patch in place is unavailable on a multi-VM instance.'])];
const pvInst=(id,over={})=>({instance_id:id,instance_name:id+'-name',service:'valkey',plan:'small',vms:3,mode:'patch',gate:'g',manifest_digest:'d',selected:[{name:K,days_left:10}],additional:[],consumers:[],blocked:null,impact:'x',...over});
t('patch in place controls', async () => {
  const env=boot(); const {w,d,errors,alerts,calls}=env; const T=w.__T;
  const J=(j,r)=>j(r.status,r.body);
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/certificates/inventory',(u,o,j)=>j(200,{certificates:certs,capabilities:caps,settings:{warning_days:30,critical_days:7}})],
    ['/b/certificates/rotation/preview',(u,o,j)=>j(200,{preview_id:'pv-1',expires_at:'2027-01-01T00:00:00Z',instances:[pvInst('inst-valkey')],warnings:['Temporary repair: BOSH can restore the old certificate until this instance converges.']})],
    [/\/b\/upgrade\/tasks$/,(u,o,j)=>{ if(o.method==='POST') return j(201,{id:'task-1'}); return j(200,[]); }]);
  w.serviceInstances={'inst-valkey':{service_id:'svc1',plan_id:'p1',plan:{id:'p1',name:'small'}},'inst-pg':{service_id:'svc1',plan_id:'p1',plan:{id:'p1',name:'small'}}};
  await sleep(50); w.plansData={services:[{id:'svc1',name:'valkey'}]};
  d.getElementById('upgrade').classList.add('active'); await T.initUpgradeTab(); await sleep(100);
  const posts=(re)=>calls.filter(c=>c.startsWith('POST')&&re.test(c)).map(c=>JSON.parse(c.slice(c.indexOf(' {')+1)));
  const waitDialog=async()=>{ for(let i=0;i<50;i++){ if(d.querySelector('#dialog-modal-overlay.active')) return true; await sleep(10);} return false; };
  const confirmBtn=()=>d.querySelector('#dialog-modal-overlay [data-dialog-role="confirm"]');
  const cancelBtn=()=>d.querySelector('#dialog-modal-overlay [data-dialog-role="cancel"]');
  const dlg=()=>d.querySelector('#dialog-modal-overlay');
  const fire=(el)=>el.dispatchEvent(new w.Event('change',{bubbles:true}));
  const pick=(id)=>{ const r=d.getElementById(id); if(r.getAttribute('role')==='tab'){ r.click(); return; } r.checked=true; fire(r); };

  // Converge radio enabled, no reason
  const cv=d.getElementById('rotate-op-converge');
  ok(cv&&!cv.disabled&&!d.getElementById('rotate-op-converge-reason'),'Converge radio enabled with no reason paragraph');
  pick('rotate-op-rotate'); await sleep(150);
  const sel=w.blacksmithCerts.selection;
  sel.delete('inst-pg'); T.renderRotateInstances(); T.updateOperationFooter(); await sleep(30);
  ok(d.getElementById('rotate-mode-patch')&&d.getElementById('rotate-mode-patch').getAttribute('aria-disabled')==='false','Patch tab enabled when the switch is on and the selection allows it');
  ok(/Temporary repair.*BOSH can restore the old certificate/.test(d.getElementById('rotate-mode-patch-panel').textContent),'Patch note shown in the Patch panel');
  ok(d.getElementById('rotate-mode-converge').hidden,'converge choice hidden before Patch');
  pick('rotate-mode-patch'); await sleep(10);
  ok(!d.getElementById('rotate-mode-converge').hidden,'choosing Patch reveals the converge choice');
  const cp=d.getElementById('rotate-mode-converge-pending'), ci=d.getElementById('rotate-mode-converge-immediate');
  ok(cp&&ci&&!cp.checked&&!ci.checked,'neither converge choice is selected');
  const fs=d.getElementById('rotate-mode-converge').textContent;
  ok(/Leave unconverged/.test(fs)&&/Converge right away/.test(fs)&&/stays flagged as unconverged until a converge/.test(fs)&&/every job restarts a second time/i.test(fs),'both choices carry a consequence sentence');
  ok(sel.get('inst-valkey')&&sel.get('inst-valkey').has(K),'valkey leaf preselected');
  const btn=d.getElementById('rotate-preview');
  ok(btn.disabled&&/Leave unconverged or Converge right away/.test(btn.title+d.getElementById('rotate-selection-line').textContent+(btn.getAttribute('aria-description')||'')+(d.getElementById('rotate-footer-hint')?d.getElementById('rotate-footer-hint').textContent:'')),'preview disabled until a converge choice is made: title='+btn.title);
  pick('rotate-mode-converge-pending'); await sleep(10);
  ok(!d.getElementById('rotate-preview').disabled,'preview enabled after a choice');
  d.getElementById('rotate-preview').click();
  ok(await waitDialog(),'confirm dialog opened');
  const pb=posts(/rotation\/preview/).pop();
  ok(pb.mode==='patch'&&pb.converge==='pending','preview body carries patch and converge: '+JSON.stringify(pb));
  const txt=dlg().querySelector('.rotate-preview').textContent; const pbody=dlg().querySelector('.rotate-preview-scroll');
  ok(/Temporary repair/.test(txt)&&(txt.match(/Temporary repair/g)||[]).length===1,'temporary-repair warning shown once');
  ok(/nobody runs director commands against the instance while the patch runs/.test(txt),'prerequisite shown');
  ok(pbody.querySelector('.rotate-preview-callout')===pbody.querySelector('.rotate-preview-callout')&&!!(pbody.querySelector('.rotate-preview-callout').compareDocumentPosition(pbody.querySelector('.rotate-preview-cards'))&4),'patch warnings sit above the instance list');
  ok(/Leave unconverged/.test(txt)&&/flagged as unconverged/.test(txt),'chosen converge policy and its consequence');
  ok(confirmBtn().textContent==='Patch in place'&&confirmBtn().classList.contains('dialog-modal-btn-danger'),'patch keeps the danger button');
  const ack=dlg().querySelector('.dialog-modal-ack-input');
  ok(!!ack,'patch confirm requires typing even for one instance'); confirmBtn().click(); await sleep(40);
  ok(posts(/"kind":"certificates"/).length===0&&dlg().classList.contains('active'),'confirm without the word creates nothing');
  // Enter on Cancel never submits
  cancelBtn().focus(); ack.value='rotate'; ack.dispatchEvent(new w.Event('input',{bubbles:true}));
  cancelBtn().dispatchEvent(new w.KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true})); await sleep(60);
  ok(posts(/"kind":"certificates"/).length===0,'Enter on Cancel creates nothing');
  if(dlg().classList.contains('active')) cancelBtn().click(); await sleep(30);
  ok(posts(/"kind":"certificates"/).length===0,'Cancel creates no task');
  // typed confirm
  d.getElementById('rotate-preview').click(); await waitDialog();
  const ack2=dlg().querySelector('.dialog-modal-ack-input'); ack2.value='nope'; ack2.dispatchEvent(new w.Event('input',{bubbles:true}));
  confirmBtn().click(); await sleep(40); ok(posts(/"kind":"certificates"/).length===0&&dlg().classList.contains('active'),'wrong word does not submit');
  ack2.value='rotate'; ack2.dispatchEvent(new w.Event('input',{bubbles:true}));
    confirmBtn().click(); await sleep(100);
  ok(posts(/"kind":"certificates"/).length===1,'patch create posted after typing');

  // switching to Immediate and back to redeploy clears
  sel.set('inst-valkey',new Set([K])); w.__T.rotateState.mode='patch'; pick('rotate-mode-redeploy'); await sleep(10);
  ok(d.getElementById('rotate-mode-converge').hidden&&T.rotateState.converge==='','redeploy hides and clears converge');

  // gate: select a multi-VM cert without patch mode
  sel.set('inst-pg',new Set([KP])); T.renderRotateInstances(); T.updateOperationFooter(); await sleep(50);
  const pg=d.getElementById('rotate-mode-patch');
  ok(pg.getAttribute('aria-disabled')==='true'&&/No health gate exists/.test(d.getElementById('rotate-mode-patch-reason').textContent)&&!d.getElementById('rotate-mode-patch-reason').hidden,'Patch disabled with the gate reason for a multi-VM instance with no gate: '+d.getElementById('rotate-mode-patch-reason').textContent);
  sel.delete('inst-pg'); T.renderRotateInstances(); T.updateOperationFooter();

  // fallback announces
  { const live=()=>d.getElementById('blacksmith-live-region').textContent;
    sel.set('inst-valkey',new Set([K])); T.renderRotateInstances(); T.updateOperationFooter(); pick('rotate-mode-patch'); pick('rotate-mode-converge-pending'); await sleep(10);
    sel.set('inst-pg',new Set([KP])); T.renderRotateInstances(); T.updateOperationFooter(); await sleep(150);
    ok(d.getElementById('rotate-mode-redeploy').getAttribute('aria-selected')==='true'&&/mode is back to Redeploy/.test(live()),'Patch falling back to Redeploy is announced');
    sel.delete('inst-pg'); T.renderRotateInstances(); T.updateOperationFooter(); }
  // Switch off
  caps={rotation:{enabled:true},patch_in_place:{enabled:false,reason:'certificates.patch_in_place.enabled is false'}};
  await T.loadRotateInventory(true); await sleep(80); pick('rotate-op-converge'); await sleep(20); pick('rotate-op-rotate'); await sleep(60);
  ok(d.getElementById('rotate-mode-patch').getAttribute('aria-disabled')==='true'&&/certificates\.patch_in_place\.enabled is false/.test(d.getElementById('rotate-mode-patch-reason').textContent),'Patch disabled with the server reason when the switch is off');
  caps={rotation:{enabled:true},patch_in_place:{enabled:true}};

  // Instance dialog
  const host=d.createElement('div'); d.body.appendChild(host);
  const dc=[{key:K,variable:'valkey_crt',daysLeft:10,expiry:'critical',modes:['redeploy','patch'],patchReason:''},{key:KP,variable:EVIL,daysLeft:5,expiry:'critical',modes:['redeploy'],patchReason:'No health gate '+EVIL}];
  const dia=w.blacksmithRotate.openInstanceDialog(host,{instanceId:'inst-valkey',instanceName:EVIL,certs:dc,capabilities:caps,preselect:new w.Set([K]),origin:'instance'});
  const prefix='rotate-dialog-inst-valkey';
  const pr=host.querySelector('#'+prefix+'-patch');
  ok(pr&&!pr.disabled&&host.querySelector('#'+prefix+'-patch-note').textContent.includes('Temporary repair'),'dialog: Patch enabled with its note');
  ok([...host.querySelectorAll('input[type=radio]')].every(r=>/^rotate-/.test(r.id)),'dialog: every radio id starts with rotate-');
  const dprev=host.querySelector('.cert-rotate-preview');
  pr.checked=true; fire(pr);
  ok(dprev.disabled&&!host.querySelector('#'+prefix+'-converge').hidden,'dialog: converge choice revealed, preview disabled until chosen');
  const dimm=host.querySelector('#'+prefix+'-converge-immediate'); dimm.checked=true; fire(dimm);
  ok(!dprev.disabled,'dialog: preview enabled after a choice');
  // select the cert with no patch mode -> Patch closes
  const boxes=[...host.querySelectorAll('input[type=checkbox]')]; boxes[1].checked=true; fire(boxes[1]);
  ok(pr.disabled&&host.querySelector('#'+prefix+'-patch-reason').textContent.includes('No health gate')&&!host.querySelector('#'+prefix+'-patch-reason').querySelector('img,script')&&host.querySelectorAll('img,script').length===0,'dialog: Patch closes with a hostile-safe reason when a picked cert has no patch mode');
  ok(host.querySelector('#'+prefix+'-redeploy').checked&&!dprev.disabled,'dialog: falls back to Redeploy');
  boxes[1].checked=false; fire(boxes[1]);
  pr.checked=true; fire(pr); dimm.checked=true; fire(dimm);
  dprev.click(); await waitDialog();
  const dpb=posts(/rotation\/preview/).pop();
  ok(dpb.mode==='patch'&&dpb.converge==='immediate'&&dpb.selections[0].variables.join()===K,'dialog: preview body patch and immediate: '+JSON.stringify(dpb));
  cancelBtn().click(); await sleep(30);
  // hostile switch-off reason
  const host2=d.createElement('div'); d.body.appendChild(host2);
  w.blacksmithRotate.openInstanceDialog(host2,{instanceId:'i2',certs:dc.slice(0,1),capabilities:{patch_in_place:{enabled:false,reason:EVIL}}});
  ok(host2.querySelector('#rotate-dialog-i2-patch').disabled&&host2.querySelector('#rotate-dialog-i2-patch-reason').textContent.includes(EVIL)&&host2.querySelectorAll('img,script').length===0,'dialog: switch-off reason shown as inert text');

  // a broker that does not report the capability is treated as closed
  const host3=d.createElement('div'); d.body.appendChild(host3);
  w.blacksmithRotate.openInstanceDialog(host3,{instanceId:'i3',certs:dc.slice(0,1),capabilities:{}});
  ok(host3.querySelector('#rotate-dialog-i3-patch').disabled,'dialog: patch stays off when the capability is missing');

  // vault_unavailable advice
  ok(/Vault did not answer/.test(T.apiErrorText({code:'vault_unavailable',message:'x'})||''),'vault_unavailable advice');
  ok(alerts.length===0,'no alerts: '+alerts.join('|'));
  ok(!errors.some(e=>/Reference|TypeError|LOAD/.test(e)),'no script errors: '+errors.join('|'));
  expect(failures).toEqual([]);
}, 120000);
