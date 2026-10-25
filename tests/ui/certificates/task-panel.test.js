const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL} = H.main;
t('task panel and API errors', async () => {
  const env=boot(); const {w,d,errors,alerts}=env;
  await sleep(100);
  ok(!errors.some(e=>/LOAD ERROR|ReferenceError|TypeError/.test(e)),'file loads with no throws: '+errors.filter(e=>/LOAD|Reference|Type/.test(e)).join('|'));
  const T=w.__T;
  ok(typeof T.readApiError==='function'&&typeof T.renderTaskDetail==='function'&&typeof T.rerenderTaskPanel==='function','hooks present');
  ok(typeof w.blacksmithNav.openTask==='function'&&typeof w.blacksmithResultPanel==='function','openTask and result panel exported');

  // error codes
  const mk=(status,body)=>({ok:false,status,text:()=>Promise.resolve(typeof body==='string'?body:JSON.stringify(body))});
  const cases=[
    [409,{success:false,error:'task is not running',code:'invalid_task_state'},'invalid_task_state','task is not running',null],
    [503,{success:false,error:'tasks are loading',code:'tasks_loading'},'tasks_loading','tasks are loading','still loading'],
    [409,{success:false,error:'director busy',code:'deployment_locked'},'deployment_locked','director busy','director is still working'],
    [409,{success:false,error:'nope',code:'not_awaiting_decision'},'not_awaiting_decision','nope','not waiting for a decision'],
    [403,{success:false,error:'Rotation is off.',code:'rotation_disabled'},'rotation_disabled','Rotation is off.','certificate_rotation'],
    [403,{success:false,error:'Patch is off.',code:'patch_disabled'},'patch_disabled','Patch is off.','certificate_patch_in_place'],
    [400,{success:false,error:'bad body',code:'invalid_request'},'invalid_request','bad body','did not accept that request'],
    [503,{success:false,error:'vault down',code:'vault_unavailable'},'vault_unavailable','vault down','try again'],
    [503,{success:false,error:'credhub down',code:'credhub_unavailable'},'credhub_unavailable','credhub down','try again'],
    [409,{success:false,error:'ca moved',code:'signing_ca_changed'},'signing_ca_changed','ca moved','runbook'],
    [409,{success:false,error:'reused',code:'request_id_conflict'},'request_id_conflict','reused','fresh one'],
    [403,{success:false,error:'no credhub',code:'credhub_not_configured'},'credhub_not_configured','no credhub','runbook'],
    [409,{success:false,error:'no gate',code:'no_health_gate'},'no_health_gate','no gate','Redeploy'],
    [404,{success:false,error:'gone',code:'instance_not_found'},'instance_not_found','gone','Refresh the page'],
  ];
  for(const [st,body,code,msg,adv] of cases){
    const e=await T.readApiError(mk(st,body)); const t=T.apiErrorText(e);
    ok(e.code===code&&e.status===st&&t.startsWith(msg)&&(adv?t.includes(adv):t===msg),`error ${code} -> code kept, message shown${adv?' with advice':''}`);
  }
  const e2=await T.readApiError(mk(502,'<html>Bad gateway</html>')); ok(e2.code===''&&e2.message.includes('Bad gateway'),'non-JSON body shown as is, no code');
  const e3=await T.readApiError(mk(500,'')); ok(e3.message==='HTTP 500','empty body falls back to HTTP status');
  // text changes must not change code branch
  const e4=await T.readApiError(mk(409,{error:'tasks_loading',code:'invalid_task_state'})); ok(e4.code==='invalid_task_state'&&T.apiErrorText(e4)==='tasks_loading','branch on code never text');

  // hostile list render
  const tasks=[{id:EVIL,name:EVIL,kind:'certificates',target:EVIL,status:'running',total_count:2,completed_count:1,failed_count:0,created_at:'2026-10-22T10:00:00Z'},
               {id:'t2',name:'',kind:'upgrade',target_stemcell:EVIL,target_releases:[EVIL],status:'failed',total_count:3,completed_count:1,failed_count:2,created_at:EVIL}];
  T._setTasks(tasks); T._setSelectedTask(null);
  const panel=d.querySelector('#upgrade .upgrade-tasks');
  T.renderUpgradeTaskList();
  ok(panel.querySelectorAll('img,script').length===0,'task list: hostile strings render inert (no img/script)');
  ok(panel.textContent.includes(EVIL),'task list: hostile text shown literally');
  ok(panel.querySelector('.upgrade-task-item').getAttribute('data-task-id')===EVIL,'task list: id attribute intact and not broken out');
  ok(!!panel.querySelector('.task-kind-badge'),'task list: certificates kind badge');

  // Every operation uses readable status and complete outcome accounting.
  T._setTasks([{id:'mixed',kind:'upgrade',status:'paused',total_count:4,completed_count:1,cancelled_count:1,skipped_count:1,unknown_count:1}]);
  T.renderUpgradeTaskList();
  ok(/Paused/.test(panel.textContent),'paused status is visible text');
  ok(/1 cancelled/.test(panel.textContent)&&/1 skipped/.test(panel.textContent)&&/1 outcome unknown/.test(panel.textContent),'all settled outcomes appear on activity cards');
  ok(!/undefined|NaN/.test(panel.textContent),'missing counters have safe defaults');
  ok(panel.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')==='100','progress counts every settled outcome accessibly');
  T._setTasks([]);T.renderUpgradeTaskList();
  ok(/New Operation/.test(panel.textContent)&&!/No upgrade tasks/.test(panel.textContent),'empty activity applies to every operation');

  // hostile detail
  const detail={id:EVIL,name:EVIL,kind:'certificates',status:EVIL,certificates:{mode:EVIL,reason:EVIL},total_count:1,completed_count:0,failed_count:1,created_at:EVIL,
    instances:[{instance_id:EVIL,instance_name:EVIL,deployment_name:EVIL,status:'failed',error:EVIL,bosh_task_id:'7"><img src=x onerror=alert(3)>'}]};
  T.renderTaskDetail(detail);
  ok(panel.querySelectorAll('img,script').length===0,'task detail: hostile strings render inert');
  ok(panel.textContent.includes(EVIL),'task detail: hostile text shown literally');
  ok(panel.querySelectorAll('[onclick]').length===0&&[...panel.querySelectorAll('.bosh-task-btn')].every(n=>/^\d+$/.test(n.dataset.boshTaskId)),'task detail: no inline handlers, and the BOSH task button carries a numeric id');

  { const shown=[]; w.showTaskDetails=(id,ev)=>{ shown.push(id); };
    T.renderTaskDetail({...detail,instances:[{instance_id:'i9',instance_name:'nine',status:'failed',bosh_task_id:42}]});
    const bb=panel.querySelector('.bosh-task-btn'); bb.click();
    ok(bb&&bb.dataset.boshTaskId==='42'&&!bb.hasAttribute('onclick')&&shown.length===1&&shown[0]===42,'BOSH task button opens its task through a listener'); }

  // controls: refusal via Feedback, no alert
  const running={id:'t1',name:'job',kind:'upgrade',status:'running',total_count:1,completed_count:0,failed_count:0,created_at:'2026-10-22T10:00:00Z',instances:[{instance_id:'i1',status:'running'}]};
  let listBody=[{...running}];
  w.__routes.push([/\/b\/upgrade\/tasks\/t1\/pause$/,(u,o,json)=>json(409,{success:false,error:'The task "x" is not running.',code:'invalid_task_state'})]);
  w.__routes.push([/\/b\/upgrade\/tasks\/t1$/,(u,o,json)=>json(200,running)]);
  w.__routes.push([/\/b\/upgrade\/tasks$/,(u,o,json)=>json(200,listBody)]);
  T._setTasks(listBody); T._setSelectedTask('t1'); T.renderTaskDetail(running);
  const pauseBtn=panel.querySelector('[data-task-action="pause"]'); ok(!!pauseBtn,'pause button rendered with listener hook');
  pauseBtn.click(); await sleep(30);
  const fb=d.querySelector('.feedback-panel[data-feedback-context="upgrade-tasks"]');
  ok(fb&&fb.classList.contains('is-error')&&fb.textContent.includes('The task "x" is not running.'),'pause refusal shows message in Feedback panel');
  ok(alerts.length===0,'no window.alert/confirm/prompt used');
  w.__routes.unshift([/\/b\/upgrade\/tasks\/t1\/resume$/,(u,o,json)=>json(503,{success:false,error:'loading',code:'tasks_loading'})]);
  await w.resumeTask('t1'); ok(d.querySelector('.feedback-panel[data-feedback-context="upgrade-tasks"]').textContent.includes('try again in a few seconds')||d.querySelector('.feedback-panel[data-feedback-context="upgrade-tasks"]').textContent.includes('Try again in a few seconds'),'resume tasks_loading advice shown');

  { const shown=[];w.showTaskDetails=id=>shown.push(id);
    for (const certificates of [{mode:'patch',converge:{bosh_task_id:71}},{deploy_outcome:{bosh_task_id:72}}]) {
      T.renderTaskDetail({...detail,instances:[{instance_id:'patch-i',status:'running',certificates}]});
      const button=panel.querySelector('.bosh-task-btn');
      ok(!!button,'certificate deployment exposes its BOSH task');
      if(button)button.click();
    }
    ok(shown.join(',')==='71,72','nested BOSH task links use the existing task modal');
    T.renderTaskDetail({...detail,instances:[{instance_id:'patch-only',status:'running',certificates:{phase:'patch'}}]});
    ok(!panel.querySelector('.bosh-task-btn'),'SSH-only patch does not invent a BOSH task');
  }
  // network failure
  w.__routes.unshift([/\/b\/upgrade\/tasks\/t1\/cancel$/,()=>Promise.reject(new Error('network down'))]);
  // cancel opens confirm dialog; auto confirm by clicking confirm
  const cp=w.cancelTask('t1'); await sleep(20); d.querySelector('[data-dialog-role="confirm"]').click(); await cp;
  ok(d.querySelector('.feedback-panel[data-feedback-context="upgrade-tasks"]').textContent.includes('network down'),'cancel network failure reported through Feedback');

  // poll status
  T._setSelectedTask(null);
  let r=await T.fetchUpgradeTasks();
  ok(r===true&&/^Updated \d\d:\d\d:\d\d$/.test(d.querySelector('.upgrade-poll-updated').textContent),'poll success shows "Updated HH:MM:SS"');
  const stamp=d.querySelector('.upgrade-poll-updated').textContent.replace('Updated ','');
  w.__routes.unshift([/\/b\/upgrade\/tasks$/,(u,o,json)=>json(500,{success:false,error:'boom',code:'internal_error'})]);
  r=await T.fetchUpgradeTasks();
  const failed=d.querySelector('.upgrade-poll-failed');
  ok(r===false&&!failed.hidden&&failed.textContent.includes('Could not refresh, showing '+stamp)&&!!d.getElementById('upgrade-poll-retry'),'failed poll shows "Could not refresh, showing <time>" with Retry');
  ok(panel.querySelector('.upgrade-task-item'),'list stays on screen after a failed poll');
  w.__routes.shift();
  d.getElementById('upgrade-poll-retry').click(); await sleep(30);
  ok(d.querySelector('.upgrade-poll-failed').hidden&&/^Updated/.test(d.querySelector('.upgrade-poll-updated').textContent),'Retry recovers and clears the note');

  // keep expanded, focus, scroll
  T._setSelectedTask('t1');
  T.renderTaskDetail(running);
  const list=panel.querySelector('.instances-list'); list.scrollTop=40;
  panel.scrollTop=25;
  panel.querySelector('[data-task-action="cancel"]').focus();
  const before=d.activeElement;
  await T.fetchUpgradeTasks();
  ok(d.activeElement!==before&&d.activeElement.dataset.keepKey==='action:cancel','focus returns to the same control after a re-render');
  ok(panel.scrollTop===25&&panel.querySelector('.instances-list').scrollTop===40,'scroll position kept across re-render');
  // details open: synthetic markup only. The real task panel renders no details elements today, so this exercises the helper alone, not the render path.
  const c=d.createElement('div'); d.body.appendChild(c); c.dataset.view='v';
  c.innerHTML='<details data-keep-key="a" open><summary>a</summary></details><details data-keep-key="b"><summary>b</summary></details>';
  T.rerenderTaskPanel(c,'v',()=>{ c.innerHTML='<details data-keep-key="a"><summary>a</summary></details><details data-keep-key="b"><summary>b</summary></details>'; });
  ok(c.querySelector('[data-keep-key=a]').open&&!c.querySelector('[data-keep-key=b]').open,'helper only (synthetic details, real render has none): expanded stay expanded, collapsed stay collapsed');
  T.rerenderTaskPanel(c,'other',()=>{ c.innerHTML='<details data-keep-key="a"><summary>a</summary></details>'; });
  ok(!c.querySelector('[data-keep-key=a]').open,'helper only (synthetic details): a different view does not inherit expanded state');

  // openTask + result panel
  const rbox=d.createElement('div'); d.body.appendChild(rbox);
  w.blacksmithNav.switchTab=async()=>true;
  listBody=[{...running}];
  let res=await w.blacksmithNav.openTask('t1'); ok(res===true&&T._getSelectedTask()==='t1','openTask selects the task');
  let threw=''; try{ await w.blacksmithNav.openTask('missing'); }catch(e){ threw=e.message; }
  ok(/does not include/.test(threw),'openTask rejects when the task is not listed');
  w.blacksmithNav.switchTab=async()=>false; threw=''; try{ await w.blacksmithNav.openTask('t1'); }catch(e){ threw=e.message; } ok(/could not be opened/.test(threw),'openTask rejects when the tab cannot open');
  w.blacksmithNav.switchTab=async()=>true;
  const rp=w.blacksmithResultPanel(rbox,{title:EVIL,taskId:EVIL});
  ok(rbox.querySelectorAll('img,script').length===0&&rbox.textContent.includes(EVIL),'result panel inert with hostile title and id');
  ok(rbox.querySelector('.result-panel-error').hidden,'no error shown at first');
  ok(rbox.querySelector('.result-panel-open').textContent==='View batch job','batch navigation distinguished from BOSH logs');
  rbox.querySelector('.result-panel-open').click(); await sleep(40);
  ok(!rbox.querySelector('.result-panel-error').hidden&&/does not include/.test(rbox.querySelector('.result-panel-error-text').textContent)&&!rbox.querySelector('.result-panel-retry').hidden,'navigation failure shown with Retry');
  listBody=[{...running,id:EVIL}]; w.__routes.unshift([/\/b\/upgrade\/tasks\/.*$/,(u,o,json)=>json(200,{...running,id:EVIL})]);
  rbox.querySelector('.result-panel-retry').click(); await sleep(60);
  ok(rbox.querySelector('.result-panel-error').hidden&&T._getSelectedTask()===EVIL,'Retry reopens the task');
  ok(d.activeElement===panel.querySelector('.task-detail-header h3'),'job navigation focuses job heading');
  const rp2=w.blacksmithResultPanel(rbox,{title:'Failed',error:'Preview failed',retry:()=>{ w.__retried=true; }});
  ok(!rbox.querySelector('.result-panel-error').hidden&&rbox.querySelector('.result-panel-error-text').textContent==='Preview failed','error option shows message');
  rbox.querySelector('.result-panel-retry').click(); ok(w.__retried===true,'retry callback runs');
  rbox.querySelector('.result-panel-close').click(); ok(rbox.children.length===0,'Close removes the panel');
  expect(failures).toEqual([]);
}, 120000);
