const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {expectNoAlerts}=require('./certificates/no-alerts.js');
const {setupRotate,cert}=require('./rotate-flow-support.js');
const t=JSDOM?test:test.skip;

// Every env a test boots, checked once the test ends so a dialog call that
// the UI code swallows still fails it, along with any console error.
const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); expectNoAlerts(env); expect(env.errors).toEqual([]); } });
// The data the page loads at boot, so its first refresh logs no error.
const BOOT={noDialogs:true,guidance:true,routes:[[/\/b\/status$/,(u,o,j)=>j(200,{env:'Local fixture',instances:{},plans:{}})],[/\/b\/instance$/,(u,o,j)=>j(200,{deployment:'blacksmith'})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/b/cf/endpoints',(u,o,j)=>j(200,{})]]};

const REL=[{name:'bpm',release_versions:[{version:'1.4.26'},{version:'1.4.0'}]}];
async function setupUpgrade(){
 const env=boot(BOOT),{w,d}=env;envs.push(env);
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,REL)],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{'small-cache':['bpm/1.4.0']})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],
  ['/b/upgrade/preview',(u,o,j)=>j(200,{id:'p1',diverged:[]})],
  [/\/b\/upgrade\/tasks\/task-1$/,(u,o,j)=>j(200,{id:'task-1',kind:'upgrade',status:'running',total_count:1,instances:[]})],
  [/\/b\/upgrade\/tasks$/,(u,o,j)=>o.method==='POST'?j(201,{id:'task-1'}):j(200,[])]);
 await sleep(30);
 w.plansData={services:[{id:'valkey',name:'valkey'}]};w.serviceInstances={cache:{service_id:'valkey',plan_id:'small',deployment_name:'small-cache',plan:{id:'small',releases:[]}}};
 d.getElementById('upgrade').classList.add('active');
 await w.__T.initUpgradeTab();await sleep(60);
 return env;
}
const pick=(w,el,v)=>{el.value=v;el.dispatchEvent(new w.Event('change',{bubbles:true}));};
const tick=(w,box,on)=>{box.checked=on;box.dispatchEvent(new w.Event('change',{bubbles:true}));};
const posts=(calls,re)=>calls.filter(c=>c.startsWith('POST')&&re.test(c)).map(c=>JSON.parse(c.slice(c.indexOf(' {')+1)));
// Opens the Review step for an upgrade of cache to a new bpm.
const reviewUpgrade=async(env)=>{
 const {w,d}=env;
 pick(w,d.getElementById('upgrade-advrel-bpm'),'1.4.26');
 w.__T.selectedUpgradeInstances.add('cache');w.__T.updateUpgradeButton();
 w.__T.runBatchReview();await sleep(60);
 expect(d.getElementById('batch-review-pause')!==null).toBe(true);
};
const createUpgrade=async(env)=>{
 await reviewUpgrade(env);
 env.d.getElementById('batch-flow-next').click();await sleep(60);
 return posts(env.calls,/\/b\/upgrade\/tasks /);
};

t('an upgrade job starts with pausing off and says so beside a help button',async()=>{
 const env=await setupUpgrade();const {w,d}=env;try{
 const box=d.getElementById('upgrade-pause-on-failure');
 expect(box.type).toBe('checkbox');
 expect(box.checked).toBe(false);
 const label=box.closest('label');
 expect(label.textContent).toBe('Pause the job if an instance fails');
 expect(label.parentElement.classList.contains('field-guidance-row')).toBe(true);
 expect(label.parentElement.querySelector('button')!==null).toBe(true);
 expect(d.getElementById(box.getAttribute('aria-describedby')).textContent).toContain('It starts off for every upgrade.');
 }finally{w.close();}
});
t('an upgrade create sends pause_on_failure false when the box is left alone',async()=>{
 const env=await setupUpgrade();const {w}=env;try{
 const created=await createUpgrade(env);
 expect(created.length).toBe(1);
 expect(created[0].pause_on_failure).toBe(false);
 }finally{w.close();}
});
t('an upgrade create sends pause_on_failure true once the box is ticked',async()=>{
 const env=await setupUpgrade();const {w,d}=env;try{
 tick(w,d.getElementById('upgrade-pause-on-failure'),true);
 const created=await createUpgrade(env);
 expect(created.length).toBe(1);
 expect(created[0].pause_on_failure).toBe(true);
 const preview=posts(env.calls,/\/b\/upgrade\/preview/);
 expect(preview.length).toBe(1);
 expect('pause_on_failure' in preview[0]).toBe(false);
 }finally{w.close();}
});
t('a ticked upgrade box goes back to off for the next upgrade once the job is made',async()=>{
 const env=await setupUpgrade();const {w,d}=env;try{
 tick(w,d.getElementById('upgrade-pause-on-failure'),true);
 const first=await createUpgrade(env);
 expect(first.map(b=>b.pause_on_failure)).toEqual([true]);
 expect(d.getElementById('upgrade-pause-on-failure').checked).toBe(false);
 const both=await createUpgrade(env);
 expect(both.map(b=>b.pause_on_failure)).toEqual([true,false]);
 }finally{w.close();}
});
t('the upgrade review says what a failure does to the job',async()=>{
 const env=await setupUpgrade();const {w,d}=env;try{
 tick(w,d.getElementById('upgrade-pause-on-failure'),true);
 await reviewUpgrade(env);
 expect(d.getElementById('batch-review-pause').textContent).toBe('If an instance fails, the job pauses before its next instance, and any deploy already running finishes first.');
 }finally{w.close();}
});
t('the upgrade review says the job goes on when pausing is off',async()=>{
 const env=await setupUpgrade();const {w,d}=env;try{
 await reviewUpgrade(env);
 expect(d.getElementById('batch-review-pause').textContent).toBe('If an instance fails, the job records the failure and goes on to the next instance.');
 }finally{w.close();}
});

const K='/s/a_crt';
const certs=[cert(K,'a_crt',5,'inst-a','cache-a')];
const pvInst={instance_id:'inst-a',instance_name:'cache-a',service:'valkey',plan:'small',vms:1,mode:'redeploy',gate:'',manifest_digest:'d',selected:[{name:K,days_left:5}],additional:[],consumers:[],blocked:null,impact:'Restarts one VM.'};
async function openRotate(settings){
 const env=await setupRotate({certs,start:false,bootOptions:BOOT});envs.push(env);
 const {w,setInventory}=env;
 if(settings) setInventory({settings:{warning_days:30,critical_days:7,...settings}});
 w.__routes.unshift(['/b/certificates/rotation/preview',(u,o,j)=>j(200,{preview_id:'pv-1',expires_at:'2027-01-01T00:00:00Z',instances:[pvInst],warnings:[]})],
  [/\/b\/upgrade\/tasks$/,(u,o,j)=>o.method==='POST'?j(201,{id:'task-1'}):j(200,[])]);
 await w.__T.requestOperation('rotate');await sleep(80);
 return env;
}
const createRotate=async(env)=>{
 const {w,d,T,calls}=env;
 w.blacksmithCerts.selection.set('inst-a',new Set([K]));T.renderRotateInstances();T.updateOperationFooter();await sleep(20);
 d.getElementById('rotate-preview').click();
 for(let i=0;i<50&&!d.querySelector('#dialog-modal[open]');i++) await sleep(10);
 d.querySelector('#dialog-modal [data-dialog-role="confirm"]').click();await sleep(80);
 return posts(calls,/\/b\/upgrade\/tasks /);
};

t('a certificate job starts from the certificate setting when it is on',async()=>{
 const env=await openRotate({pause_on_failure:true});const {w,d}=env;try{
 const box=d.getElementById('rotate-pause-on-failure');
 expect(box.checked).toBe(true);
 expect(box.closest('label').parentElement.classList.contains('field-guidance-row')).toBe(true);
 const created=await createRotate(env);
 expect(created.length).toBe(1);
 expect(created[0].pause_on_failure).toBe(true);
 }finally{w.close();}
});
t('a certificate job starts from the certificate setting when it is off',async()=>{
 const env=await openRotate({pause_on_failure:false});const {w,d}=env;try{
 expect(d.getElementById('rotate-pause-on-failure').checked).toBe(false);
 const created=await createRotate(env);
 expect(created.length).toBe(1);
 expect(created[0].pause_on_failure).toBe(false);
 }finally{w.close();}
});
t('an inventory without the setting leaves a certificate job pausing',async()=>{
 const env=await openRotate();const {w,d}=env;try{
 expect(d.getElementById('rotate-pause-on-failure').checked).toBe(true);
 }finally{w.close();}
});
t('unticking the box sends false for this job and leaves the setting alone',async()=>{
 const env=await openRotate({pause_on_failure:true});const {w,d,T,calls}=env;try{
 tick(w,d.getElementById('rotate-pause-on-failure'),false);
 const created=await createRotate(env);
 expect(created.length).toBe(1);
 expect(created[0].pause_on_failure).toBe(false);
 expect(T.rotateState.pauseOnFailure).toBe(true);
 expect(calls.some(c=>/^(PUT|POST|PATCH) \/b\/(certificates|upgrade)\/settings/.test(c))).toBe(false);
 }finally{w.close();}
});

t('an unticked certificate box goes back to the setting for the next job once the job is made',async()=>{
 const env=await openRotate({pause_on_failure:true});const {w,d}=env;try{
 tick(w,d.getElementById('rotate-pause-on-failure'),false);
 const first=await createRotate(env);
 expect(first.map(b=>b.pause_on_failure)).toEqual([false]);
 await w.__T.requestOperation('rotate');await sleep(80);
 expect(d.getElementById('rotate-pause-on-failure').checked).toBe(true);
 const both=await createRotate(env);
 expect(both.map(b=>b.pause_on_failure)).toEqual([false,true]);
 }finally{w.close();}
});

t('a command job shows fixed text that it always pauses, with no checkbox',async()=>{
 const env=boot(BOOT),{w,d}=env;envs.push(env);try{
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
 await sleep(30);w.serviceInstances={cache:{service_id:'valkey',plan_id:'small'}};
 await w.__T.initUpgradeTab();await sleep(10);
 await w.__T.requestOperation('command');w.__T.setBatchStep(1,false);await sleep(10);
 const fixed=d.getElementById('command-pause-on-failure');
 expect(fixed.textContent).toContain('this job always pauses before the next deployment');
 expect(d.getElementById('rotate-pause-on-failure')).toBe(null);
 }finally{w.close();}
});

const detail=(over={})=>({id:'task-9',kind:'upgrade',status:'paused',target_stemcell:'ubuntu-noble/1.1',total_count:2,completed_count:1,failed_count:1,created_at:new Date().toISOString(),
 instances:[{instance_id:'inst-a',instance_name:'cache-a',status:'failed'},{instance_id:'inst-b',instance_name:'cache-b',status:'pending'}],...over});
async function renderDetail(task){
 const env=boot(BOOT),{w,d}=env;envs.push(env);
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
 await sleep(30);w.plansData={services:[]};
 await w.__T.initUpgradeTab();await sleep(10);
 w.__T.renderTaskDetail(task);
 return {...env,summary:d.querySelector('#upgrade .task-detail-summary'),buttons:[...d.querySelectorAll('#upgrade .job-control-btn')].map(b=>b.textContent)};
}
const REASON='Instance cache-a (inst-a) failed, so the job paused before its remaining instances. Continue remaining instances moves on to them without retrying inst-a.';

t('an upgrade paused after a failure shows its reason and offers Continue remaining instances',async()=>{
 const env=await renderDetail(detail({pause_on_failure:true,pause_reason:REASON}));const {w,summary,buttons}=env;try{
 expect(summary.querySelector('.task-pause-on-failure').textContent).toBe('Pauses before its next instance');
 expect(summary.querySelector('.task-pause-reason').textContent).toBe(REASON);
 expect(buttons.includes('Continue remaining instances')).toBe(true);
 expect(buttons.includes('Resume')).toBe(false);
 }finally{w.close();}
});
t('an upgrade an operator paused keeps the plain Resume',async()=>{
 const env=await renderDetail(detail({pause_on_failure:true,failed_count:0,instances:[{instance_id:'inst-a',status:'success'},{instance_id:'inst-b',status:'pending'}]}));const {w,summary,buttons}=env;try{
 expect(summary.querySelector('.task-pause-reason')).toBe(null);
 expect(buttons.includes('Resume')).toBe(true);
 }finally{w.close();}
});
t('the task detail shows what a failure does for each kind of stored task',async()=>{
 const cases=[
  [detail({status:'running',pause_on_failure:false}),'Goes on to the next instance'],
  [detail({status:'running'}),'Goes on to the next instance'],
  [detail({status:'running',kind:'certificates',certificates:{mode:'redeploy'},pause_on_failure:false}),'Goes on to the next instance'],
  [detail({status:'running',kind:'certificates',certificates:{mode:'redeploy'},pause_on_failure:true}),'Pauses before its next instance'],
  [detail({status:'running',kind:'certificates',certificates:{mode:'redeploy'}}),'Follows the broker\'s certificate setting']
 ];
 for(const [task,want] of cases){
  const env=await renderDetail(task);const {w,summary}=env;
  try{ expect(summary.querySelector('.task-pause-on-failure').textContent).toBe(want); }finally{w.close();}
 }
});
