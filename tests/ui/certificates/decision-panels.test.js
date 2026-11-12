const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL} = H.main;
const UUID=/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const chk=(name,passed,detail)=>({name,passed,detail});
const mkTask=(over={})=>({id:'task-1',name:'Spring '+EVIL,kind:'certificates',status:'running',paused:false,certificates:{mode:'redeploy',preview_id:'pv'},
  total_count:3,completed_count:0,failed_count:0,created_at:'2026-10-21T14:00:00Z',
  instances:[
   {instance_id:'i-rec',instance_name:'rec '+EVIL,deployment_name:'d1',status:'awaiting_decision',certificates:{phase:'regenerate',variables:[{name:'/s/'+EVIL+'_crt',old_serial:'AA11',old_not_after:'2026-10-27T09:12:00Z',new_serial:'',regenerate_state:'outcome_unknown',validation:[chk('chain '+EVIL,false,'bad '+EVIL)]}],
     pending_decision:{kind:'reconcile_regeneration',variable:'/s/'+EVIL+'_crt',since:'2026-10-21T14:20:00Z',candidates:[
       {version_id:'v-good-1234567890',created_at:'2026-10-21T14:10:00Z',serial:'BB22'+EVIL,not_after:'2027-10-21T00:00:00Z',valid:true,checks:[chk('signed',true,'')]},
       {version_id:'v-bad-0987654321',created_at:'2026-10-21T14:11:00Z',serial:'CC33',not_after:'2027-10-21T00:00:00Z',valid:false,checks:[chk('chain',false,'wrong CA '+EVIL)]}]}}},
   {instance_id:'i-app',instance_name:'app',status:'awaiting_decision',certificates:{phase:'preflight',variables:[],pending_decision:{kind:'approve_changed_effects',changes:['Manifest changed '+EVIL,'One more variable'],since:'2026-10-21T14:20:00Z'}}},
   {instance_id:'i-dep',instance_name:'dep',status:'awaiting_decision',certificates:{phase:'deploy',variables:[],pending_decision:{kind:'resolve_deploy_outcome',since:'2026-10-21T14:20:00Z'}}}
  ],...over});
t('decision panels', async () => {
  const env=boot(); const {w,d,errors,alerts,calls}=env; const T=w.__T;
  let tasks=[mkTask()], resp=null, previewResp=null;
  const posts=()=>calls.filter(c=>c.startsWith('POST'));
  const lastPost=(re)=>{ const l=posts().filter(c=>re.test(c)); return l[l.length-1]; };
  const bodyOf=(c)=>JSON.parse(c.slice(c.indexOf('{')));
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/certificates/inventory',(u,o,j)=>j(200,{certificates:[],capabilities:{rotation:{enabled:true}}})],
    [/\/instances\/[^/]+\/preview$/,(u,o,j)=>previewResp?j(previewResp.status,previewResp.body):j(200,{preview_id:'pv-new',instances:[{instance_id:'i-app',instance_name:'app',vms:3,gate:'g',selected:[{name:'/s/a_crt',days_left:5}],additional:[],blocked:null,impact:'Restarts.'}],warnings:[]})],
    [/\/instances\/[^/]+\/(reconcile|approve|resolve)$/,(u,o,j)=>resp?j(resp.status,resp.body):j(200,tasks[0])],
    [/\/b\/upgrade\/tasks\/[^/]+$/,(u,o,j)=>j(200,tasks[0])],
    [/\/b\/upgrade\/tasks$/,(u,o,j)=>j(200,tasks.map(t=>({id:t.id,name:t.name,kind:t.kind,status:t.status,target:'Redeploy, 1 certificate',total_count:3,completed_count:0,failed_count:0,awaiting_decision_count:t.awaitingCount===undefined?3:t.awaitingCount,created_at:t.created_at})))]);
  w.serviceInstances={}; await sleep(50); w.plansData={services:[]};
  d.getElementById('upgrade').classList.add('active'); await T.initUpgradeTab(); await sleep(100);
  const waitDialog=async()=>{ for(let i=0;i<50;i++){ if(d.querySelector('#dialog-modal[open]')) return true; await sleep(10);} return false; };
  const confirmBtn=()=>d.querySelector('#dialog-modal [data-dialog-role="confirm"]');
  const cancelBtn=()=>d.querySelector('#dialog-modal [data-dialog-role="cancel"]');
  const dialogTitle=()=>d.querySelector('#dialog-modal .dialog-modal-title').textContent;
  const panel=()=>d.querySelector('#upgrade .upgrade-tasks');
  const btn=(key,action)=>[...panel().querySelectorAll('[data-decision-action]')].find(b=>b.dataset.decisionKey===key&&b.dataset.decisionAction===action);
  const live=()=>d.getElementById('blacksmith-live-region').textContent;

  // ---- list
  await T.fetchUpgradeTasks(); await sleep(60);
  const item=panel().querySelector('.upgrade-task-item');
  ok(!!item&&!!item.querySelector('.task-kind-badge.task-kind-badge--certificates'),'list: kind badge uses task-kind-badge and the certificates modifier');
  ok(panel().textContent.includes('Redeploy, 1 certificate')&&!/\s\/\s*$/m.test(item.querySelector('.upgrade-task-target').textContent),'list: certificate summary reads Redeploy, 1 certificate');
  await T.fetchUpgradeTasks(); await sleep(80);
  ok(!!panel().querySelector('.task-awaiting-badge')&&panel().querySelector('.task-awaiting-badge').textContent==='Awaiting decision','list: Awaiting decision badge comes from awaiting_decision_count with no holder poll');
  d.getElementById('upgrade').classList.remove('active'); await T.fetchUpgradeTasks(); await sleep(60);
  ok(!!panel().querySelector('.task-awaiting-badge'),'list: badge shows with the Batch tab inactive');
  d.getElementById('upgrade').classList.add('active');
  tasks[0].awaitingCount=0; tasks[0].status='cancelled'; await T.fetchUpgradeTasks(); await sleep(60);
  ok(!panel().querySelector('.task-awaiting-badge'),'list: no badge when the count is 0');
  tasks[0].awaitingCount=1; await T.fetchUpgradeTasks(); await sleep(60);
  ok(!!panel().querySelector('.task-awaiting-badge'),'list: cancelled task with a pending decision still shows the badge');
  tasks[0].status='running'; tasks[0].awaitingCount=3; await T.fetchUpgradeTasks(); await sleep(60);
  ok(panel().querySelectorAll('img,script').length===0&&panel().textContent.includes(EVIL),'list: hostile task name renders as text');
  ok(T.getInstanceStatusIcon('awaiting_decision').includes('awaiting-decision')&&T.getInstanceStatusClass('awaiting_decision')==='instance-awaiting-decision','icon and class for awaiting_decision');

  // ---- detail render
  let threw=null; try{ T.renderTaskDetail(mkTask()); }catch(e){ threw=e; }
  ok(!threw,'detail: renders a certificate task with all three decisions without throwing '+(threw&&threw.message));
  T._setSelectedTask('task-1');
  ok(!!panel().querySelector('.upgrade-task-instance.instance-awaiting-decision .status-icon.awaiting-decision'),'detail: awaiting instance shows the awaiting-decision icon and class');
  ok(panel().querySelectorAll('img,script,iframe').length===0&&panel().textContent.includes(EVIL),'detail: hostile strings are inert text');
  const txt=panel().textContent;
  ok((panel().querySelector('.instance-heading .cert-task-phase')||{}).textContent==='Regenerate'&&txt.includes('AA11')&&txt.includes('2026-10-27')&&txt.includes('Failed: chain')&&txt.includes('Version v-good-1'),'detail: phase, old serial and expiry, validation check, candidate');
  ok(!!btn('task-1|i-rec','adopt:v-good-1234567890')&&btn('task-1|i-rec','adopt:v-good-1234567890').textContent==='Adopt version v-good-1'&&![...panel().querySelectorAll('[data-decision-action]')].some(b=>b.dataset.decisionAction==='adopt:v-bad-0987654321'),'reconcile: Adopt offered only for the valid candidate');

  // ---- request bodies
  const rec='task-1|i-rec', app='task-1|i-app', dep='task-1|i-dep';
  btn(rec,'adopt:v-good-1234567890').click(); await waitDialog();
  ok(/Adopt version v-good-1/.test(dialogTitle()),'adopt opens a confirm dialog');
  confirmBtn().click(); await sleep(80);
  let post=lastPost(/\/reconcile( |$)/); let b=post&&bodyOf(post);
  ok(post&&post.startsWith('POST /b/upgrade/tasks/task-1/instances/i-rec/reconcile')&&b.action==='adopt'&&b.version_id==='v-good-1234567890'&&b.variable==='/s/'+EVIL+'_crt'&&UUID.test(b.client_request_id),'adopt body: variable, action, version_id, client_request_id');
  const adoptId=b.client_request_id;
  T._setSelectedTask('task-1'); T.renderTaskDetail(mkTask()); 
  btn(rec,'abandon').click(); await waitDialog(); confirmBtn().click(); await sleep(80);
  post=lastPost(/\/reconcile( |$)/); b=bodyOf(post);
  ok(b.action==='abandon'&&!('version_id' in b)&&UUID.test(b.client_request_id)&&b.client_request_id!==adoptId,'abandon body has its own client_request_id');
  // cancel -> no post, focus back to the control
  const n0=posts().length; T.renderTaskDetail(mkTask());
  btn(rec,'abandon').focus(); btn(rec,'abandon').click(); await waitDialog(); cancelBtn().click(); await sleep(80);
  ok(posts().length===n0&&d.activeElement===btn(rec,'abandon'),'cancel posts nothing and returns focus to the control: '+(d.activeElement&&d.activeElement.textContent));
  // double click -> one request
  const n1=posts().length; T.renderTaskDetail(mkTask());
  const ab=btn(rec,'abandon'); ab.click(); ab.click(); await waitDialog(); confirmBtn().click(); await sleep(100);
  ok(posts().length===n1+1,'double click applies once: '+(posts().length-n1)+' posts');
  // approve flow
  T.renderTaskDetail(mkTask());
  btn(app,'preview').click(); await waitDialog();
  ok(/POST .*\/instances\/i-app\/preview$/.test(lastPost(/\/preview$/)||'')&&!lastPost(/\/preview$/).includes('{'),'Preview again posts without a body');
  ok(confirmBtn().textContent==='Approve changes','preview dialog shown for confirmation');
  confirmBtn().click(); await sleep(80);
  post=lastPost(/\/approve( |$)/); b=bodyOf(post);
  ok(b.preview_id==='pv-new'&&UUID.test(b.client_request_id)&&!('action' in b),'approve body: preview_id and client_request_id');
  // approving a patch preview asks for the typed word
  { const patchTask=mkTask({certificates:{mode:'patch',preview_id:'pv'}}); T.renderTaskDetail(patchTask);
    previewResp={status:200,body:{preview_id:'pv-patch',instances:[{instance_id:'i-app',instance_name:'app',vms:1,gate:'g',selected:[{name:'/s/a_crt',days_left:5}],additional:[],blocked:null,impact:'x'}],warnings:[]}};
    const nA=posts().filter(c=>/\/approve( |$)/.test(c)).length;
    btn(app,'preview').click(); await waitDialog();
    const ackA=d.querySelector('#dialog-modal .dialog-modal-ack-input');
    ok(!!ackA&&confirmBtn().classList.contains('dialog-modal-btn-danger'),'patch approve requires the typed word');
    ok(d.querySelector('#dialog-modal .dialog-modal-ack-label').textContent==='To continue, type "patch in place" below.'&&d.querySelector('#dialog-modal .dialog-modal-ack-label strong').textContent==='"patch in place"','patch approve asks for patch in place');
    confirmBtn().click(); await sleep(40);
    ok(posts().filter(c=>/\/approve( |$)/.test(c)).length===nA&&d.querySelector('#dialog-modal[open]'),'patch approve without the word posts nothing');
    ackA.value='patch in place'; ackA.dispatchEvent(new w.Event('input',{bubbles:true})); confirmBtn().click(); await sleep(80);
    ok(posts().filter(c=>/\/approve( |$)/.test(c)).length===nA+1,'patch approve posts after the word');
    previewResp=null; }
  T.renderTaskDetail(mkTask());
  btn(app,'skip').click(); await waitDialog(); confirmBtn().click(); await sleep(80);
  b=bodyOf(lastPost(/\/approve( |$)/));
  ok(b.action==='skip'&&!('preview_id' in b)&&UUID.test(b.client_request_id),'skip body: action skip and client_request_id');
  ok(panel().textContent.includes('Manifest changed')&&panel().querySelectorAll('img,script').length===0,'changes listed as text');
  // resolve
  T.renderTaskDetail(mkTask());
  const n2=posts().length; btn(dep,'verify').click(); await sleep(80);
  b=bodyOf(lastPost(/\/resolve( |$)/));
  ok(posts().length===n2+1&&!d.querySelector('#dialog-modal[open]')&&b.action==='verify'&&UUID.test(b.client_request_id),'verify posts at once with no confirmation');
  for(const act of ['failed','abandon']){
    T.renderTaskDetail(mkTask());
    const n=posts().length; btn(dep,act).click(); await waitDialog();
    ok(posts().length===n,act+' waits for confirmation');
    confirmBtn().click(); await sleep(80);
    b=bodyOf(lastPost(/\/resolve( |$)/));
    ok(b.action===act&&UUID.test(b.client_request_id),act+' body after confirm');
  }

  // ---- error mapping, branching on code
  const errCase=async(label,status,code,message,expectText)=>{
    resp={status,body:{success:false,error:message,code}};
    T.renderTaskDetail(mkTask()); const g0=calls.filter(c=>/GET .*tasks\/task-1$/.test(c)).length;
    btn(dep,'verify').click(); await sleep(120);
    const box=panel().querySelector('[data-decision-error]');
    ok(!!box&&box.getAttribute('role')==='alert'&&box.textContent.includes(message)&&(!expectText||box.textContent.includes(expectText)),label+': inline alert shows message'+(expectText?' and advice':'')+' => '+(box&&box.textContent));
    ok(d.activeElement===box,label+': focus stays on the error message');
    ok(live().includes(message),label+': live region announces it');
    return g0;
  };
  const g=await errCase('deployment_locked',409,'deployment_locked','xyz still running','Try again when its task finishes');
  await errCase('invalid_request',400,'invalid_request','bad action','Check what you sent');
  const gb=calls.filter(c=>/GET .*tasks\/task-1$/.test(c)).length;
  await errCase('not_awaiting_decision',409,'not_awaiting_decision','moved on','any more');
  await sleep(60);
  ok(calls.filter(c=>/GET .*tasks\/task-1$/.test(c)).length>gb,'not_awaiting_decision: the task is read again');
  // message text never decides
  await errCase('wrong-code-with-lookalike-text',500,'internal_error','deployment is locked, not awaiting a decision','');
  ok(!panel().querySelector('[data-decision-error]').textContent.includes('Try again when its task finishes'),'advice follows the code, not the message text');
  resp=null;
  // retry works and clears the error
  T.renderTaskDetail(mkTask()); btn(dep,'verify').click(); await sleep(100);
  ok(!panel().querySelector('[data-decision-error]'),'a later success clears the error');
  ok(d.activeElement&&d.activeElement.dataset&&d.activeElement.dataset.decisionRow==='task-1|i-dep','success focuses the instance row');


  // ---- request id reuse across a lost response
  { T.renderTaskDetail(mkTask());
    let mode='lost'; const seen=[];
    w.__routes.unshift([/\/instances\/i-dep\/resolve$/,(u,o,j)=>{ seen.push(JSON.parse(o.body).client_request_id); if(mode==='lost') return Promise.reject(new TypeError('network down')); if(mode==='coded') return j(409,{error:'not now',code:'not_awaiting_decision'}); return j(200,tasks[0]); }]);
    const go=async()=>{ T.renderTaskDetail(mkTask()); btn(dep,'failed').click(); await waitDialog(); confirmBtn().click(); await sleep(90); };
    await go(); await go();
    ok(seen.length===2&&seen[0]===seen[1]&&UUID.test(seen[0]),'a retry after a lost response reuses the client_request_id');
    mode='coded'; await go();
    ok(seen[2]===seen[0],'the third send still reuses it, then a coded answer ends it');
    await go(); mode='ok';
    ok(seen[3]!==seen[0]&&UUID.test(seen[3]),'after a coded error the next send makes a new id');
    await go(); await go();
    ok(seen.length===6&&seen[4]!==seen[3]&&seen[5]!==seen[4],'after a success the next send makes a new id');
    w.__routes.shift();
  }
  // ---- empty preview
  { T.renderTaskDetail(mkTask());
    w.__routes.unshift([/\/instances\/i-app\/preview$/,(u,o,j)=>j(200,{})]);
    btn(app,'preview').click(); await sleep(100);
    const err=panel().querySelector('[data-decision-error]');
    ok(err&&/preview was empty/.test(err.textContent)&&!/did not accept/.test(err.textContent)&&/choose Preview again/.test(err.textContent),'a missing preview_id gets its own accurate advice');
    w.__routes.shift(); }

  // ---- control copy
  const base=mkTask({instances:[{instance_id:'i-1',instance_name:'one',status:'running',certificates:{phase:'deploy',variables:[]}}]});
  T.renderTaskDetail({...base,status:'paused',paused:true});
  ok(panel().textContent.includes('Pause requested'),'pause: running instance shows Pause requested');
  T.renderTaskDetail({...base,status:'paused',paused:true,instances:[{instance_id:'i-1',status:'pending',certificates:{phase:'preflight',variables:[]}}]});
  ok(panel().querySelector('.status-paused').textContent==='Paused','pause: settled shows Paused');
  const failed=mkTask({status:'paused',paused:true,failed_count:1,instances:[{instance_id:'i-1',instance_name:'one',status:'failed',error:'boom',certificates:{phase:'deploy',variables:[{name:'/x/a',old_serial:'01',new_serial:'02'}]}}]});
  T.renderTaskDetail(failed);
  ok(panel().querySelector('.resume-btn').textContent==='Continue remaining instances'&&panel().textContent.includes('The Continue remaining instances button never retries the failed instance. It moves on to the instances still waiting, so look at that failure before you resume.'),'resume: Continue remaining instances with the failed-instance note');
  ok(btn('task-1|i-1','remediate').textContent==='Redeploy this instance'&&!btn('task-1|i-1','remediate').disabled,'failed redeploy instance offers Redeploy this instance, enabled');
  { const b=btn('task-1|i-1','remediate'); const note=d.getElementById(b.getAttribute('aria-describedby')); ok(!!note&&/CredHub already holds/.test(note.textContent),'remediation note explains the redeploy as text'); const n=posts().length; b.click(); await sleep(60); ok(posts().length>n&&/rotation\/preview/.test(posts().slice(n)[0]||''),'enabled remediation requests a converge preview'); if(d.querySelector('#dialog-modal[open]')) d.querySelector('#dialog-modal [data-dialog-role="cancel"]').click(); }
  { const r=d.getElementById('rotate-op-converge'); ok(r&&!r.disabled&&!d.getElementById('rotate-op-converge-reason'),'Batch Converge radio is enabled with no reason paragraph'); }
  { const noNew=mkTask({status:'paused',paused:true,failed_count:1,instances:[{instance_id:'i-1',instance_name:'one',status:'failed',error:'boom',certificates:{phase:'regenerate',variables:[{name:'/x/a',old_serial:'01',new_serial:''},{name:'/x/b',old_serial:'05',new_serial:'05'}]}}]}); T.renderTaskDetail(noNew); ok(!btn('task-1|i-1','remediate'),'failed redeploy with no new version offers no redeploy'); T.renderTaskDetail({...noNew,certificates:{mode:'patch'}}); ok(!!btn('task-1|i-1','remediate'),'failed patch instance still offers Converge without a new version'); }
  T.renderTaskDetail({...failed,certificates:{mode:'patch'}});
  ok(btn('task-1|i-1','remediate').textContent==='Converge this instance'&&!btn('task-1|i-1','remediate').disabled,'failed patch instance offers Converge this instance, enabled');
  T.renderTaskDetail(failed); 
  w.cancelTask('task-1'); await waitDialog();
  const msg=d.querySelector('#dialog-modal .dialog-modal-message').textContent;
  ok(/BOSH deploy is cancelled/.test(msg)&&/partly rolled/.test(msg),'cancel (redeploy) says the deploy is cancelled and the instance may be partly rolled'); cancelBtn().click(); await sleep(40);
  T.renderTaskDetail({...failed,certificates:{mode:'patch'}});
  w.cancelTask('task-1'); await waitDialog();
  ok(confirmBtn().textContent==='Finish current VM and stop','cancel (patch) confirm reads Finish current VM and stop'); cancelBtn().click(); await sleep(40);
  // upgrade tasks are untouched
  T.renderTaskDetail({id:'u1',name:'Up',kind:'upgrade',status:'paused',failed_count:1,total_count:1,completed_count:0,target_stemcell:{},instances:[{instance_id:'x',status:'failed'}]});
  ok(panel().querySelector('.resume-btn').textContent==='Resume'&&!panel().querySelector('[data-decision-action]'),'upgrade task detail unchanged');
  ok(alerts.length===0,'no alert/confirm/prompt: '+alerts.join('|'));
  ok(!errors.some(e=>/Reference|TypeError|SyntaxError|LOAD/.test(e)),'no script errors: '+errors.join('|'));
  expect(failures).toEqual([]);
}, 120000);
