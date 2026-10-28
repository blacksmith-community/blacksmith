const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL} = H.main;
const cert=(key,variable,id,name,days=10)=>({key,variable,kind:'leaf',days_left:days,owners:[{kind:'instance',instance_id:id,instance_name:name}],rotation:{allowed:true,reasons:[]},findings:[]});
const K='/s/valkey_crt';
const inv={certificates:[cert(K,'valkey_crt','inst-a','a-name')],capabilities:{rotation:{enabled:true}},settings:{warning_days:30,critical_days:7}};
t('holder reads and polling', async () => {
  const env=boot(); const {w,d,errors,alerts,calls}=env; const T=w.__T;
  let tasks=[], detail={}, failIds=new Set();
  const getsOf=(re)=>calls.filter(c=>c.startsWith('GET')&&re.test(c)).length;
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/certificates/inventory',(u,o,j)=>j(200,inv)],
    [/\/b\/upgrade\/tasks\/[^/]+$/,(u,o,j)=>{const id=decodeURIComponent(u.split('/').pop()); if(failIds.has(id)) return j(404,{error:'gone',code:'not_found'}); return detail[id]?j(200,detail[id]):j(404,{error:'nf'});}],
    [/\/b\/upgrade\/tasks$/,(u,o,j)=>j(200,tasks)]);
  const inst=(n)=>Array.from({length:n},(_,i)=>['inst-a','inst-b','inst-c'][i]);
  w.serviceInstances={'inst-a':{service_id:'svc1',plan_id:'p1',plan:{id:'p1',name:'small'}},'inst-b':{service_id:'svc1',plan_id:'p1',plan:{id:'p1',name:'small'}},'inst-c':{service_id:'svc1',plan_id:'p1',plan:{id:'p1',name:'small'}}};
  await sleep(50);
  w.plansData={services:[{id:'svc1',name:'valkey'}]};
  const mkTask=(id,status,held)=>{ tasks=tasks.filter(t=>t.id!==id).concat([{id,name:id,kind:'certificates',status}]); detail[id]={id,name:id,kind:'certificates',status,instances:held.map(h=>({instance_id:h,status:'running'}))}; };
  mkTask('t1','running',['inst-a']); mkTask('t2','running',['inst-b']); mkTask('t3','pending',['inst-c']);
  await T.initUpgradeTab(); await sleep(100);
  const panel=d.getElementById('upgrade');
  const rowsHeld=()=>[...d.querySelectorAll('#upgrade .upgrade-instance-item[data-held]')].map(r=>r.dataset.instanceId).sort().join();
  // ---- I1: tab not active
  const base=getsOf(/tasks\/[^/]+$/);
  await T.fetchUpgradeTasks(); await sleep(50);
  ok(getsOf(/tasks\/[^/]+$/)===base,'I1: no task detail reads while the Batch tab is not active');
  panel.classList.add('active');
  Object.defineProperty(w.document,'visibilityState',{configurable:true,get:()=>'hidden'});
  await T.fetchUpgradeTasks(); await sleep(50);
  ok(getsOf(/tasks\/[^/]+$/)===base,'I1: no task detail reads while the page is hidden');
  Object.defineProperty(w.document,'visibilityState',{configurable:true,get:()=>'visible'});
  await T.fetchUpgradeTasks(); await sleep(50);
  ok(getsOf(/tasks\/[^/]+$/)===base+3&&rowsHeld()==='inst-a,inst-b,inst-c','I1: visible active tab reads each active task once and shows holders: '+rowsHeld());
  // signature unchanged -> no reads
  await T.fetchUpgradeTasks(); await T.fetchUpgradeTasks(); await sleep(50);
  ok(getsOf(/tasks\/[^/]+$/)===base+3,'I1: unchanged id:status signatures are not read again');
  mkTask('t2','paused',['inst-b']);
  await T.fetchUpgradeTasks(); await sleep(50);
  ok(getsOf(/tasks\/[^/]+$/)===base+4,'I1: only the task whose status changed is read');
  // ---- inventory refresh gating
  const r=d.getElementById('rotate-op-rotate'); r.checked=true; r.dispatchEvent(new w.Event('change',{bubbles:true})); await sleep(80);
  const invGets=()=>getsOf(/certificates\/inventory/);
  T.rotateState.lastLoad=0; panel.classList.remove('active');
  await T.fetchUpgradeTasks(); await sleep(50);
  const g0=invGets();
  ok(g0===getsOf(/certificates\/inventory/)&&T.rotateState.lastLoad===0,'I1: no inventory refresh while the tab is not active');
  panel.classList.add('active');
  await T.fetchUpgradeTasks(); await sleep(50);
  ok(invGets()===g0+1,'I1: inventory refreshes when the tab is active in rotate mode');
  const u=d.getElementById('rotate-op-upgrade'); u.checked=true; u.dispatchEvent(new w.Event('change',{bubbles:true})); await sleep(80);
  if(d.querySelector('#dialog-modal[open]')){ d.querySelector('#dialog-modal [data-dialog-role="confirm"]').click(); await sleep(50); }
  T.rotateState.lastLoad=0; const g1=invGets();
  await T.fetchUpgradeTasks(); await sleep(50);
  ok(d.getElementById('upgrade').dataset.operation==='upgrade'&&invGets()===g1,'I1: inventory refresh stops after leaving Rotate mode');
  // ---- M1: failed read keeps holders, retries next poll
  failIds.add('t2'); mkTask('t2','running',['inst-b']); failIds.add('t2');
  const f0=getsOf(/tasks\/t2$/);
  await T.fetchUpgradeTasks(); await sleep(50);
  ok(getsOf(/tasks\/t2$/)===f0+1&&rowsHeld()==='inst-a,inst-b,inst-c','M1: a failed read keeps that task\'s last holders');
  await T.fetchUpgradeTasks(); await sleep(50);
  ok(getsOf(/tasks\/t2$/)===f0+2,'M1: the failed task is read again on the next poll');
  failIds.delete('t2'); detail.t2.instances=[{instance_id:'inst-b',status:'success'}];
  await T.fetchUpgradeTasks(); await sleep(50);
  ok(rowsHeld()==='inst-a,inst-c','M1: holders recover once the read succeeds: '+rowsHeld());
  // other tasks still update while one fails
  failIds.add('t1'); mkTask('t1','paused',['inst-a']); failIds.add('t1'); mkTask('t3','running',[]); 
  detail.t3.instances=[{instance_id:'inst-c',status:'success'}];
  await T.fetchUpgradeTasks(); await sleep(50);
  ok(rowsHeld()==='inst-a','M1: a failing task does not freeze the others: '+rowsHeld());
  // ---- M5: focus and scroll kept when holders change in Upgrade mode
  failIds.clear(); mkTask('t1','running',['inst-a']); mkTask('t3','pending',['inst-c']);
  await T.fetchUpgradeTasks(); await sleep(50);
  const list=d.querySelector('#upgrade .upgrade-instances');
  let st=0; Object.defineProperty(list,'scrollTop',{configurable:true,get:()=>st,set:(v)=>{st=v;}});
    const cb=[...d.querySelectorAll('#upgrade .upgrade-instance-item')].find(x=>x.dataset.instanceId==='inst-b').querySelector('input');
  cb.focus(); st=37;
  mkTask('t3','paused',['inst-c']); detail.t3.instances=[{instance_id:'inst-c',status:'success'}];
  await T.fetchUpgradeTasks(); await sleep(50);
  const cb2=[...d.querySelectorAll('#upgrade .upgrade-instance-item')].find(x=>x.dataset.instanceId==='inst-b').querySelector('input');
    ok(rowsHeld()==='inst-a'&&cb2!==cb&&d.activeElement===cb2,'M5: focus returns to the same row after the holder redraw');
  ok(st===37,'M5: scroll position kept across the redraw');
  ok(alerts.length===0&&!errors.some(e=>/Reference|TypeError|LOAD/.test(e)),'no alerts or script errors: '+errors.join('|'));
  expect(failures).toEqual([]);
}, 120000);
