const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const SECRET='echo SECRET-COMMAND-TEXT-7733';
const PREVIEW_URL='/b/upgrade/commands/preview';

// Every env a test boots, checked once the test ends so a dialog error that
// the UI code swallows still fails it.
const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); expect(env.alerts).toEqual([]); expect(env.logs.some(l=>String(l).includes(SECRET))).toBe(false); } });
async function setup(){
 const env=boot({noDialogs:true}),{w}=env;envs.push(env);
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])],
  [PREVIEW_URL,(u,o,j)=>j(400,{success:false,error:'Select at least one instance to preview.',code:'no_selections'})]);
 w.plansData={services:[{id:'valkey',name:'valkey'},{id:'redis',name:'redis'}]};
 await sleep(30);
 w.serviceInstances={
  'val-a':{service_id:'valkey',plan_id:'small',plan:{id:'small',name:'small'}},
  'val-b':{service_id:'valkey',plan_id:'small',plan:{id:'small',name:'small'}},
  'red-a':{service_id:'redis',plan_id:'small',plan:{id:'small',name:'small'}}
 };
 await w.__T.initUpgradeTab();await sleep(10);
 return env;
}
const type=(w,el,value)=>{el.value=value;el.dispatchEvent(new w.Event('input',{bubbles:true}));};
async function openInstances(env){
 const {w,d}=env;
 await w.__T.requestOperation('command');
 w.__T.setBatchStep(1,false);
 type(w,d.getElementById('command-text'),SECRET);
 w.__T.setBatchStep(2,false);
 await sleep(5);
}
const pick=(d,id)=>d.querySelector(`#upgrade .upgrade-instance-item[data-instance-id="${id}"] input[type="checkbox"]`).click();
const ids=(set)=>[...set].sort();

for(const state of ['disabled','loading','error']){
 t(`a command run with the certificate inventory ${state} uses the upgrade instance list`,async()=>{
  const env=await setup();const {w,d}=env;try{
  w.__T.rotateState.loadState=state;
  await openInstances(env);
  expect(w.__T.batchOptionIssue()).toBe('');
  const rows=[...d.querySelectorAll('#upgrade .upgrade-instances .upgrade-instance-item')];
  expect(rows.map(r=>r.dataset.instanceId).sort()).toEqual(['red-a','val-a','val-b']);
  expect(d.querySelector('#upgrade .upgrade-instances .rotate-row')).toBeNull();
  expect(ids(w.__T.batchMatchingRows().map(([id])=>id))).toEqual(['red-a','val-a','val-b']);
  expect(w.__T.batchSelectedIds()).toBe(w.__T.selectedUpgradeInstances);
  const action=d.getElementById('command-preview');
  expect(action).not.toBeNull();expect(action.disabled).toBe(true);
  expect(w.__T.batchInstanceIssue()).toContain('Select at least one');
  pick(d,'val-a');
  expect(ids(w.__T.batchSelectedIds())).toEqual(['val-a']);
  expect(action.disabled).toBe(false);expect(w.__T.batchInstanceIssue()).toBe('');
  pick(d,'val-a');
  expect(action.disabled).toBe(true);expect(w.__T.batchInstanceIssue()).toContain('Select at least one');
  }finally{w.close();}
 });
}

t('a selection that spans two services is allowed and the summary says so',async()=>{
 const env=await setup();const {w,d}=env;try{
 await openInstances(env);
 const summary=d.getElementById('batch-options-summary');
 pick(d,'val-a');
 expect(summary.textContent).toBe('Run as vcap, 1 command, 1 instance');
 pick(d,'val-b');pick(d,'red-a');
 expect(w.__T.batchInstanceIssue()).toBe('');
 expect(summary.textContent).toBe('Run as vcap, 1 command, 3 instances, across 2 services');
 d.getElementById('command-user-root').click();
 expect(summary.textContent).toBe('Run as root, 1 command, 3 instances, across 2 services');
 d.getElementById('command-user-vcap').click();
 d.getElementById('command-parallel').click();
 type(w,d.getElementById('command-max-in-flight'),'2');
 expect(summary.textContent).toBe('Run as vcap, 1 command, 3 instances, across 2 services, 2 at a time');
 expect(summary.hidden||summary.closest('[hidden]')).toBeFalsy();
 expect(summary.textContent.includes(SECRET)).toBe(false);
 }finally{w.close();}
});

t('the review step on a command run requests only the command preview',async()=>{
 const env=await setup();const {w,d}=env;try{
 w.__T.selectedUpgradeInstances.add('val-a');
 w.__T.rotateSelection().set('val-b',new Set(['/s/b_crt']));
 // A stemcell target and a ready inventory, so a stray certificate or
 // upgrade preview would post.
 w.__T.rotateState.loadState='ready';
 const stemcell=d.getElementById('upgrade-stemcell-filter');
 stemcell.innerHTML='<option value="ubuntu-noble/1.1">ubuntu-noble/1.1</option>';stemcell.value='ubuntu-noble/1.1';
 await openInstances(env);
 const before=env.calls.length;
 const next=d.getElementById('batch-flow-next');
 expect(next.disabled).toBe(false);
 next.click();await sleep(20);
 const sent=env.calls.slice(before);
 expect(sent.some(c=>c.startsWith('POST '+PREVIEW_URL))).toBe(true);
 expect(sent.every(c=>c.startsWith('POST '+PREVIEW_URL)||c==='GET /b/upgrade/tasks')).toBe(true);
 expect(env.errors.some(e=>e.includes(SECRET))).toBe(false);
 }finally{w.close();}
});

t('a selected instance the list no longer knows counts as a service of its own',async()=>{
 const env=await setup();const {w,d}=env;try{
 await openInstances(env);
 pick(d,'val-a');
 w.__T.selectedUpgradeInstances.add('gone-instance');
 w.__T.refreshUpgradeInstanceList();
 expect(d.getElementById('batch-options-summary').textContent).toBe('Run as vcap, 1 command, 2 instances, across 2 services');
 }finally{w.close();}
});
