const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// Restart and Recreate on a service instance's VMs tab ask in the page's
// dialog, which names the instance and the action, and send nothing until
// the operator confirms. A started action links to the BOSH task it queued.

const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); }finally{ env.w.close(); } } });

const vms=[{id:'vm-1',instance:'redis/0',job_name:'redis',index:0,state:'running',job_state:'running',ips:['10.0.0.5'],az:'z1',resurrection_paused:false}];
function mount(instanceId='7a1b'){
 const env=boot({noDialogs:true}); envs.push(env);
 const {w,d}=env;
 w.serviceInstances={'7a1b':{instance_name:'orders-cache',service_id:'redis',plan_id:'small'}};
 const host=d.createElement('div'); host.id='vms-host'; d.body.appendChild(host);
 host.innerHTML=w.__T.formatVMs(vms,instanceId);
 return {...env,host};
}
const dialog=(d)=>d.getElementById('dialog-modal');
const actionButton=(host,action)=>host.querySelector(`[data-instance-action="${action}"]`);
const posts=(env,action)=>env.calls.filter(c=>c.startsWith(`POST /b/7a1b/${action}`));
const status=(d)=>d.getElementById('instance-action-status-7a1b');

t('a service instance shows Restart and Recreate with their own keep keys',()=>{
 const {host,d}=mount();
 const restart=actionButton(host,'restart'), recreate=actionButton(host,'recreate');
 expect(restart.textContent.trim()).toBe('Restart');
 expect(recreate.textContent.trim()).toBe('Recreate');
 expect(restart.getAttribute('type')).toBe('button');
 const keys=[...d.querySelectorAll('[data-keep-key]')].map(n=>n.dataset.keepKey);
 expect(keys).toContain(restart.dataset.keepKey);
 expect(keys).toContain(recreate.dataset.keepKey);
 expect(new Set(keys).size).toBe(keys.length);
 expect(restart.dataset.keepKey).not.toBe(recreate.dataset.keepKey);
});

t('the broker VMs view shows no instance actions',()=>{
 const {host}=mount(null);
 expect(host.querySelector('[data-instance-action]')).toBeNull();
});

for(const [action,label] of [['restart','Restart'],['recreate','Recreate']]){
 t(`${label} asks in the page dialog naming the instance, and Cancel sends nothing`,async()=>{
  const env=mount(); const {host,d}=env;
  actionButton(host,action).click(); await sleep(10);
  const overlay=dialog(d);
  expect(overlay.open).toBe(true);
  expect(overlay.querySelector('.dialog-modal-title').textContent).toBe(`${label} orders-cache?`);
  const text=overlay.textContent;
  expect(text).toContain('orders-cache');
  expect(text).toContain('7a1b');
  expect(overlay.querySelector('[data-dialog-role="confirm"]').textContent).toBe(label);
  expect(posts(env,action)).toEqual([]);
  overlay.querySelector('[data-dialog-role="cancel"]').click(); await sleep(10);
  expect(posts(env,action)).toEqual([]);
  expect(status(d).textContent).toBe('');
 });

 t(`${label} runs once after Confirm and links the BOSH task`,async()=>{
  const env=mount(); const {w,host,d}=env;
  let release;
  w.__routes.unshift([`/b/7a1b/${action}`,()=>new Promise(resolve=>{release=resolve;})]);
  actionButton(host,action).click(); await sleep(10);
  dialog(d).querySelector('[data-dialog-role="confirm"]').click(); await sleep(10);
  expect(posts(env,action).length).toBe(1);
  expect(actionButton(host,'restart').disabled).toBe(true);
  expect(actionButton(host,'recreate').disabled).toBe(true);
  expect(status(d).textContent).toContain(`${label} of orders-cache is starting`);
  actionButton(host,action).click(); await sleep(10);
  expect(dialog(d).open).toBe(false);
  release(env.json(202,{instance_id:'7a1b',deployment:'redis-small-7a1b',operation:action,task_id:4321,state:'queued'})); await sleep(10);
  expect(posts(env,action).length).toBe(1);
  expect(actionButton(host,'restart').disabled).toBe(false);
  const link=status(d).querySelector('a.bosh-task-link');
  expect(link.textContent).toBe('BOSH task 4321');
  expect(link.dataset.historyTaskId).toBe('4321');
  expect(link.dataset.keepKey).toBe(`instance-action-task:7a1b:4321`);
  expect(status(d).textContent).toContain(`${label} of orders-cache started`);
  let opened=null; w.showTaskDetails=(id)=>{ opened=id; };
  link.click();
  expect(opened).toBe(4321);
 });
}

t('a refused action shows the reason as text and leaves no task link',async()=>{
 const env=mount(); const {w,host,d}=env;
 w.__routes.unshift(['/b/7a1b/restart',(u,o,json)=>json(409,{success:false,code:'instance_busy',error:'instance 7a1b is busy with certificate rotation'})]);
 actionButton(host,'restart').click(); await sleep(10);
 dialog(d).querySelector('[data-dialog-role="confirm"]').click(); await sleep(20);
 expect(status(d).textContent).toContain('Restart of orders-cache failed');
 expect(status(d).textContent).toContain('instance 7a1b is busy with certificate rotation');
 expect(status(d).dataset.state).toBe('failed');
 expect(status(d).querySelector('a')).toBeNull();
 expect(actionButton(host,'restart').disabled).toBe(false);
});

t('the started task stays linked when the VMs table renders again',async()=>{
 const env=mount(); const {w,host,d}=env;
 w.__routes.unshift(['/b/7a1b/recreate',(u,o,json)=>json(202,{task_id:99,state:'queued'})]);
 actionButton(host,'recreate').click(); await sleep(10);
 dialog(d).querySelector('[data-dialog-role="confirm"]').click(); await sleep(20);
 host.innerHTML=w.__T.formatVMs(vms,'7a1b');
 const link=status(d).querySelector('a.bosh-task-link');
 expect(link.textContent).toBe('BOSH task 99');
 expect(status(d).getAttribute('role')).toBe('status');
});

t('an instance without a recorded name is named by its ID',async()=>{
 const env=mount(); const {w,host,d}=env;
 w.serviceInstances={};
 actionButton(host,'restart').click(); await sleep(10);
 expect(dialog(d).querySelector('.dialog-modal-title').textContent).toBe('Restart 7a1b?');
 dialog(d).querySelector('[data-dialog-role="cancel"]').click(); await sleep(10);
});

t('a lost director answer says the action may have started and links the deployment tasks',async()=>{
 const env=mount(); const {w,host,d}=env;
 w.__routes.unshift(['/b/7a1b/restart',(u,o,json)=>json(504,{success:false,code:'outcome_unknown',error:"The director's answer was lost.",instance_id:'7a1b',deployment:'redis-small-7a1b',operation:'restart'})]);
 actionButton(host,'restart').click(); await sleep(10);
 dialog(d).querySelector('[data-dialog-role="confirm"]').click(); await sleep(20);
 const node=status(d);
 expect(node.dataset.state).toBe('unknown');
 expect(node.textContent).toContain('Restart of orders-cache may have started');
 expect(node.textContent).not.toContain('failed');
 const link=node.querySelector('a');
 expect(link.getAttribute('href')).toBe('#/instances/7a1b/events');
 expect(link.textContent).toBe('the deployment\'s tasks in Events');
 expect(link.dataset.keepKey).toBe('instance-action-events:7a1b');
 expect(node.querySelector('button')).toBeNull();
 const keys=[...d.querySelectorAll('[data-keep-key]')].map(n=>n.dataset.keepKey);
 expect(new Set(keys).size).toBe(keys.length);
 // The next attempt warns that the last one may have started.
 actionButton(host,'recreate').click(); await sleep(10);
 expect(dialog(d).textContent).toContain('The last restart of orders-cache may have started');
 dialog(d).querySelector('[data-dialog-role="cancel"]').click(); await sleep(10);
 expect(posts(env,'recreate')).toEqual([]);
});

t('a locked deployment names the action and links the running task',async()=>{
 const env=mount(); const {w,host,d}=env;
 w.__routes.unshift(['/b/7a1b/recreate',(u,o,json)=>json(409,{success:false,code:'deployment_locked',error:'The director is running task 812.',instance_id:'7a1b',deployment:'redis-small-7a1b',operation:'recreate',task_id:812})]);
 actionButton(host,'recreate').click(); await sleep(10);
 dialog(d).querySelector('[data-dialog-role="confirm"]').click(); await sleep(20);
 const node=status(d);
 expect(node.dataset.state).toBe('blocked');
 expect(node.textContent).toBe('Recreate of orders-cache didn\'t start, because BOSH task 812 is running on its deployment.');
 const link=node.querySelector('a.bosh-task-link');
 expect(link.textContent).toBe('BOSH task 812');
 expect(link.dataset.historyTaskId).toBe('812');
 expect(link.dataset.keepKey).toBe('instance-action-running-task:7a1b:812');
 expect(actionButton(host,'recreate').disabled).toBe(false);
 let opened=null; w.showTaskDetails=(id)=>{ opened=id; };
 link.click();
 expect(opened).toBe(812);
});

t('the dialog names the org and space when the broker knows them',async()=>{
 const env=mount(); const {w,host,d}=env;
 w.serviceInstances['7a1b'].organization_name='payments';
 w.serviceInstances['7a1b'].space_name='prod';
 actionButton(host,'restart').click(); await sleep(10);
 const rows=[...dialog(d).querySelectorAll('.dialog-modal-details li')].map(n=>n.textContent);
 expect(rows).toEqual(['Instance: orders-cache','Instance ID: 7a1b','Organization: payments','Space: prod']);
 dialog(d).querySelector('[data-dialog-role="cancel"]').click(); await sleep(10);
});

// When the broker's own answer never reaches the browser, the action may
// still have started, so the tab shows the unknown outcome, not a failure.
const expectUnknown=(d,label)=>{
 const node=status(d);
 expect(node.dataset.state).toBe('unknown');
 expect(node.textContent).toContain(`${label} of orders-cache may have started`);
 expect(node.textContent).not.toContain('failed');
 expect(node.querySelector('a').getAttribute('href')).toBe('#/instances/7a1b/events');
 expect(node.querySelector('button')).toBeNull();
};

t('a fetch that fails shows the unknown outcome',async()=>{
 const env=mount(); const {w,host,d}=env;
 w.__routes.unshift(['/b/7a1b/restart',()=>Promise.reject(new w.TypeError('Failed to fetch'))]);
 actionButton(host,'restart').click(); await sleep(10);
 dialog(d).querySelector('[data-dialog-role="confirm"]').click(); await sleep(20);
 expectUnknown(d,'Restart');
 expect(actionButton(host,'restart').disabled).toBe(false);
});

t('a gateway error with no broker code shows the unknown outcome',async()=>{
 const env=mount(); const {w,host,d}=env;
 w.__routes.unshift(['/b/7a1b/recreate',()=>Promise.resolve({ok:false,status:504,statusText:'Gateway Timeout',json:()=>Promise.reject(new w.SyntaxError('Unexpected token <')),text:()=>Promise.resolve('<html>Gateway Timeout</html>')})]);
 actionButton(host,'recreate').click(); await sleep(10);
 dialog(d).querySelector('[data-dialog-role="confirm"]').click(); await sleep(20);
 expectUnknown(d,'Recreate');
});

t('a broker 5xx that carries its own code still reads as a failure',async()=>{
 const env=mount(); const {w,host,d}=env;
 w.__routes.unshift(['/b/7a1b/restart',(u,o,json)=>json(503,{success:false,code:'tasks_loading',error:'the stored tasks are still loading'})]);
 actionButton(host,'restart').click(); await sleep(10);
 dialog(d).querySelector('[data-dialog-role="confirm"]').click(); await sleep(20);
 expect(status(d).dataset.state).toBe('failed');
 expect(status(d).textContent).toContain('the stored tasks are still loading');
});
