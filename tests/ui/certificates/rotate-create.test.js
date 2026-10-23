const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL} = H.main;
const K='/secret/dir/valkey_crt', K2='/secret/dir/redis_cluster_crt', KP='/secret/dir/pg_crt';
const cert=(key,variable,kind,days,allowed,id,name,more={})=>({key,variable,kind,days_left:days,owners:[{kind:'instance',instance_id:id,instance_name:name}],rotation:{allowed,reasons:allowed?[]:['Deploy is pending.']},findings:[],...more});
const inv={certificates:[cert(K,'valkey_crt','leaf',10,true,'inst-valkey','orders-cache'),cert(K2,'redis_cluster_crt','leaf',200,true,'inst-valkey','orders-cache'),cert(KP,'pg_crt','leaf',5,false,'inst-pg','billing-db',{findings:[{code:'deploy_pending'}]})],capabilities:{rotation:{enabled:true}},settings:{warning_days:30,critical_days:7}};
const pvInst=(id,over={})=>({instance_id:id,instance_name:id+'-name',service:'valkey',plan:'small',vms:3,mode:'redeploy',gate:'valkey-cluster',manifest_digest:'d',selected:[{name:K,days_left:10}],additional:[],consumers:[],blocked:null,impact:'Restarts one VM at a time.',...over});
t('rotate preview and create', async () => {
  const env=boot(); const {w,d,errors,alerts,calls}=env; const T=w.__T;
  let previewAnswer=()=>({status:200,body:{preview_id:'pv-1',expires_at:'2027-01-01T00:00:00Z',instances:[pvInst('inst-valkey',{additional:[{name:'/secret/dir/other_crt'}]}),pvInst('inst-x',{blocked:{code:'instance_busy',message:'Another task holds inst-x.'}})],warnings:['Heads up '+EVIL]}});
  let createAnswers=[]; 
  const J=(j,r)=>j(r.status,r.body);
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/certificates/inventory',(u,o,j)=>j(200,inv)],
    ['/b/certificates/rotation/preview',(u,o,j)=>J(j,previewAnswer(JSON.parse(o.body)))],
    [/\/b\/upgrade\/tasks$/,(u,o,j)=>{ if(o.method==='POST'){ const a=createAnswers.shift(); return J(j,a?a:{status:201,body:{id:'task-1'}}); } return j(200,[]); }]);
  w.serviceInstances={'inst-valkey':{service_id:'svc1',plan_id:'p1',plan:{id:'p1',name:'small'}},'inst-pg':{service_id:'svc1',plan_id:'p1',plan:{id:'p1',name:'small'}}};
  await sleep(50);
  w.plansData={services:[{id:'svc1',name:'valkey'}]};
  d.getElementById('upgrade').classList.add('active'); await T.initUpgradeTab(); await sleep(100);
  const posts=(re)=>calls.filter(c=>c.startsWith('POST')&&re.test(c)).map(c=>JSON.parse(c.slice(c.indexOf(' {')+1)));
  const waitDialog=async()=>{ for(let i=0;i<50;i++){ if(d.querySelector('#dialog-modal-overlay.active')) return true; await sleep(10);} return false; };
  const confirmBtn=()=>d.querySelector('#dialog-modal-overlay [data-dialog-role="confirm"]');
  const cancelBtn=()=>d.querySelector('#dialog-modal-overlay [data-dialog-role="cancel"]');
  const dlg=()=>d.querySelector('#dialog-modal-overlay');
  const radio=d.getElementById('rotate-op-rotate'); radio.checked=true; radio.dispatchEvent(new w.Event('change',{bubbles:true})); await sleep(120);
  const sel=w.blacksmithCerts.selection;
  ok(d.getElementById('rotate-mode-redeploy').checked&&d.getElementById('rotate-mode-patch').disabled,'Mode radio: Redeploy checked, Patch disabled');
  ok(d.getElementById('rotate-mode').textContent.includes('Patch in place is not available'),'Patch reason visible as text');
  ok(sel.get('inst-valkey')&&sel.get('inst-valkey').has(K),'valkey leaf preselected');
  const btn=d.getElementById('rotate-preview');
  ok(btn.textContent==='Preview rotation of 1 certificate on 1 instance'&&!btn.disabled,'footer label and enabled: '+btn.textContent);
  d.getElementById('rotate-reason').value='Quarterly'; d.getElementById('rotate-reason').dispatchEvent(new w.Event('input'));
  d.getElementById('rotate-name').value='My job'; d.getElementById('rotate-name').dispatchEvent(new w.Event('input'));
  btn.click(); 
  ok(await waitDialog(),'confirm dialog opened');
  const pv=posts(/rotation\/preview/);
  ok(pv.length===1&&JSON.stringify(pv[0])===JSON.stringify({mode:'redeploy',selections:[{instance_id:'inst-valkey',variables:[K]}]}),'preview body: mode and selections, no converge: '+JSON.stringify(pv[0]));
  const txt=dlg().querySelector('.dialog-modal-details').textContent;
  ok(/Mode: Redeploy/.test(txt)&&/3 VMs, health gate valkey-cluster. Restarts one VM at a time/.test(txt),'details: mode, VMs, gate, impact');
  ok(/Also changes on inst-valkey-name: \/secret\/dir\/other_crt/.test(txt),'details: additional variables');
  ok(/Excluded: inst-x-name: Another task holds inst-x\./.test(txt),'details: excluded instance with reason');
  ok(txt.includes('Warning: Heads up')&&txt.includes(EVIL)&&dlg().querySelectorAll('img,script').length===0,'details: warning shown, hostile inert');
  ok(!dlg().querySelector('.dialog-modal-ack-input'),'no typed confirmation for 1 instance');
  confirmBtn().click(); await sleep(80);
  const cr=posts(/"kind":"certificates"/);
  ok(cr.length===1&&cr[0].preview_id==='pv-1'&&/^00000000-0000-4000-8000-/.test(cr[0].client_request_id)&&cr[0].name==='My job'&&cr[0].reason==='Quarterly'&&cr[0].kind==='certificates','create body: kind, preview_id, client_request_id, name, reason');
  const host=d.getElementById('rotate-result');
  ok(host&&host.textContent.includes('Rotation started')&&host.textContent.includes('task-1'),'result panel shows task');
  ok(sel.size===0,'batch selection cleared after create');
  ok(btn.id.startsWith('rotate-')&&[...d.querySelectorAll('#upgrade [id]')].filter(n=>/^rotate-/.test(n.id)).length>=8,'new ids start with rotate-');

  // 200 -> already started; default name; cancel
  sel.set('inst-valkey',new Set([K2]));
  createAnswers=[{status:200,body:{id:'task-0'}}];
  d.getElementById('rotate-name').value=''; d.getElementById('rotate-name').dispatchEvent(new w.Event('input'));
  d.getElementById('rotate-select-visible').click();
  d.getElementById('rotate-preview').click(); await waitDialog(); confirmBtn().click(); await sleep(80);
  const cr2=posts(/"kind":"certificates"/);
  ok(cr2.length===2&&cr2[1].name==='Rotate 1 certificate on 1 instance','default name when the field is empty: '+cr2[1].name);
  ok(d.getElementById('rotate-result').textContent.includes('already started'),'200 shows already-started title');
  // cancel
  d.getElementById('rotate-select-visible').click();
  d.getElementById('rotate-preview').click(); await waitDialog(); cancelBtn().click(); await sleep(50);
  ok(posts(/"kind":"certificates"/).length===2&&sel.size>0,'cancel creates nothing and keeps selection');
  ok(!d.getElementById('rotate-preview').disabled&&d.getElementById('rotate-preview').textContent.startsWith('Preview rotation'),'button usable after cancel');

  // retry reuses client request id
  createAnswers=[{status:503,body:{success:false,error:'Tasks are loading.',code:'tasks_loading'}},{status:201,body:{id:'task-2'}}];
  d.getElementById('rotate-preview').click(); await waitDialog(); confirmBtn().click(); await sleep(80);
  let c=posts(/"kind":"certificates"/);
  ok(c.length===3&&d.getElementById('rotate-result').textContent.includes('Tasks are loading.')&&d.getElementById('rotate-result').textContent.includes('still loading'),'create error shown in panel with message and advice');
  d.querySelector('#rotate-result .result-panel-retry').click(); await sleep(80);
  c=posts(/"kind":"certificates"/);
  ok(c.length===4&&c[3].client_request_id===c[2].client_request_id&&c[3].preview_id===c[2].preview_id,'Retry reuses the client_request_id');
  ok(d.getElementById('rotate-result').textContent.includes('task-2'),'retry success shows task');

  // 410
  sel.set('inst-valkey',new Set([K]));
  let n=0; previewAnswer=()=>({status:200,body:{preview_id:'pv-'+(++n+1),instances:[pvInst('inst-valkey')],warnings:[]}});
  createAnswers=[{status:410,body:{success:false,error:'The preview expired.',code:'preview_expired'}},{status:201,body:{id:'task-3'}}];
  d.getElementById('rotate-select-visible').click();
  const before=posts(/"kind":"certificates"/).length, pb=posts(/rotation\/preview/).length;
  d.getElementById('rotate-preview').click(); await waitDialog(); confirmBtn().click(); 
  await sleep(80); await waitDialog();
  ok(dlg().querySelector('.dialog-modal-message').textContent.includes('The state changed since the preview, so here is a fresh one.'),'410 reopens preview with the fresh-one message');
  ok(posts(/rotation\/preview/).length===pb+2,'a second preview was requested');
  confirmBtn().click(); await sleep(80);
  c=posts(/"kind":"certificates"/);
  ok(c.length===before+2&&c[c.length-1].preview_id!==c[c.length-2].preview_id&&c[c.length-1].client_request_id!==c[c.length-2].client_request_id,'fresh preview gets its own id and request id');
  ok(d.getElementById('rotate-result').textContent.includes('task-3'),'fresh create shows task');

  // rotation_disabled on preview
  previewAnswer=()=>({status:403,body:{success:false,error:'Certificate rotation is turned off on this broker.',code:'rotation_disabled'}});
  d.getElementById('rotate-select-visible').click();
  d.getElementById('rotate-preview').click(); await sleep(80);
  ok(!d.querySelector('#dialog-modal-overlay.active'),'no dialog on preview failure');
  ok(d.getElementById('rotate-result').textContent.includes('Certificate rotation is turned off on this broker.')&&!!d.querySelector('#rotate-result .result-panel-retry'),'403 rotation_disabled message in panel with Retry');
  // all blocked
  previewAnswer=()=>({status:200,body:{preview_id:'pv-b',instances:[pvInst('inst-valkey',{blocked:{code:'instance_busy',message:'Held by a task.'}})],warnings:[]}});
  d.getElementById('rotate-preview').click(); await sleep(80);
  ok(!d.querySelector('#dialog-modal-overlay.active')&&d.getElementById('rotate-result').textContent.includes('Every selected instance is excluded')&&d.getElementById('rotate-result').textContent.includes('Held by a task.'),'all blocked: reasons in panel, no dialog');
  // >10 instances
  previewAnswer=()=>({status:200,body:{preview_id:'pv-big',instances:Array.from({length:11},(_,i)=>pvInst('inst-'+i)),warnings:[]}});
  createAnswers=[];
  const bigBefore=posts(/"kind":"certificates"/).length;
  d.getElementById('rotate-preview').click(); await waitDialog();
  ok(!!dlg().querySelector('.dialog-modal-ack-input')&&/Type "rotate"/.test(dlg().textContent),'11 instances require typing rotate');
  confirmBtn().click(); await sleep(30);
  ok(dlg().classList.contains('active')&&posts(/"kind":"certificates"/).length===bigBefore,'confirm without typing does not proceed');
  dlg().querySelector('.dialog-modal-ack-input').value='rotate'; confirmBtn().click(); await sleep(80);
  ok(posts(/"kind":"certificates"/).length===bigBefore+1,'typing rotate proceeds');
  // exactly 10 does not
  previewAnswer=()=>({status:200,body:{preview_id:'pv-ten',instances:Array.from({length:10},(_,i)=>pvInst('inst-'+i)),warnings:[]}});
  sel.set('inst-valkey',new Set([K])); d.getElementById('rotate-select-visible').click();
  d.getElementById('rotate-preview').click(); await waitDialog();
  ok(!dlg().querySelector('.dialog-modal-ack-input'),'10 instances need no typed confirmation'); cancelBtn().click(); await sleep(30);

  // direct calls
  const r0=await w.blacksmithRotate.previewAndCreate({mode:'redeploy',selections:[],origin:'x'});
  ok(r0.ok===false&&d.getElementById('rotate-result').textContent.includes('Select at least one certificate first.'),'empty selections reported in panel');
  previewAnswer=(b)=>{ lastBody=b; return {status:200,body:{preview_id:'pv-p',instances:[pvInst('inst-valkey',{mode:'patch'})],warnings:[]}}; };
  var lastBody=null;
  const pp=w.blacksmithRotate.previewAndCreate({mode:'patch',converge:'pending',selections:[{instance_id:'inst-valkey',variables:[K]}],origin:'instance'});
  await waitDialog(); ok(posts(/rotation\/preview/).pop().converge==='pending','patch preview carries the converge policy'); cancelBtn().click(); const pr=await pp; ok(pr.cancelled===true,'cancel resolves cancelled');
  const cv=w.blacksmithRotate.previewAndCreate({mode:'converge',converge:'pending',selections:[{instance_id:'inst-pg',variables:[K]}],origin:'instance'});
  await waitDialog(); const lastPv=posts(/rotation\/preview/).pop();
  ok(JSON.stringify(lastPv)===JSON.stringify({mode:'converge',selections:[{instance_id:'inst-pg',variables:[]}]}),'converge preview: empty variables, no converge policy: '+JSON.stringify(lastPv)); cancelBtn().click(); await cv;
  // hostile result
  createAnswers=[{status:201,body:{id:EVIL}}];
  const hp=w.blacksmithRotate.previewAndCreate({mode:'redeploy',selections:[{instance_id:'inst-valkey',variables:[K]}],origin:'instance',name:EVIL,reason:EVIL});
  await waitDialog(); confirmBtn().click(); const hr=await hp;
  const hostBody=posts(/"kind":"certificates"/).pop();
  ok(hr.ok&&hostBody.name===EVIL&&hostBody.reason===EVIL,'name and reason sent verbatim as JSON');
  const rh=d.getElementById('rotate-result');
  ok(rh.querySelectorAll('img,script').length===0&&rh.textContent.includes(EVIL)&&rh.querySelector('.result-panel').dataset.origin==='instance','hostile task id inert in the result panel');
  // converge flow from batch
  d.getElementById('rotate-op-converge').checked=true; d.getElementById('rotate-op-converge').dispatchEvent(new w.Event('change',{bubbles:true})); await sleep(30);
  if(dlg().classList.contains('active')){ confirmBtn().click(); await sleep(80); }
  sel.clear(); await sleep(10);
  ok(d.getElementById('rotate-preview').disabled&&d.getElementById('rotate-preview').title.length>0,'converge button disabled with reason when nothing selected');
  ok(alerts.length===0,'no alerts: '+alerts.join('|'));
  ok(!errors.some(e=>/Reference|TypeError|LOAD/.test(e)),'no script errors: '+errors.join('|'));
  expect(failures).toEqual([]);
}, 120000);
