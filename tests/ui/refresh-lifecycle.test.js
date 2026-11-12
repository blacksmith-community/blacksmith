const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const cases=[
 ['broker events','blacksmith','events','events-table-container','refreshBlacksmithEvents','/events'],
 ['broker VMs','blacksmith','vms','vms-table-container','refreshBlacksmithVMs','/vms'],
 ['instance VMs','services','vms','vms-table-container','refreshServiceInstanceVMs','/vms'],
 ['instance events','services','events','events-table-container','refreshServiceInstanceEvents','/events'],
 ['deployment logs','services','logs','deployment-log-table','refreshServiceInstanceDeploymentLog','/task/log'],
 ['debug logs','services','debug','debug-log-table','refreshServiceInstanceDebugLog','/task/debug'],
 ['broker tasks','blacksmith','tasks','tasks-table-container','refreshTasksTable','/b/tasks'],
 ['broker configs','blacksmith','configs','configs-table-container','refreshConfigsTable','/b/configs']
];
for(const [label,owner,section,container,method,path] of cases){
 test(`${label} late refresh cannot overwrite a different section`,async()=>{
  const {w,d}=boot();await sleep(50);
  const root=d.getElementById(owner);root.classList.add('active');
  root.innerHTML=`<div class="entity-workspace" data-section="${section}"><div class="detail-content"><div class="${container}"><button id="refresh"><span>Refresh</span></button></div></div></div>`;
  w.currentInstanceInfo={id:'one'};
  let release;w.__routes.push([path,()=>new Promise(r=>release=()=>r({ok:true,json:async()=>[],text:async()=> '[]'}))]);
  const fn=w[method]||w.__T[method]; const event={currentTarget:d.getElementById('refresh')};
  const promise=owner==='services'?fn('one',event):fn(event);
  root.querySelector('.entity-workspace').dataset.section='details';
  root.querySelector('.detail-content').innerHTML='<p>Current summary</p>';
  release();await promise;await sleep(120);
  expect(root.querySelector('.detail-content').textContent).toBe('Current summary');
  w.close();
 });
}

test('instance log refresh cannot mutate a different instance context',async()=>{
 const {w,d}=boot();await sleep(50);
 const root=d.getElementById('services');root.classList.add('active');
 root.innerHTML='<button class="service-instance-item active" data-id="one"></button><div class="entity-workspace" data-section="instance-logs"><div class="detail-content"><button id="refresh"></button></div></div>';
 w.currentInstanceInfo={id:'one'};w.instanceLogsData={original:true};
 let release;w.__routes.push(['/b/one/logs',()=>new Promise(r=>release=()=>r({ok:true,text:async()=>JSON.stringify({node:{logs:'new logs'}})}))]);
 const pending=w.refreshInstanceLogs('node',{currentTarget:d.getElementById('refresh')});
 w.currentInstanceInfo={id:'two'};release();await pending;
 expect(w.instanceLogsData).toEqual({original:true});w.close();
});

test('failed refresh retains prior results and adds a recoverable inline error',async()=>{
 const {w,d}=boot();await sleep(50);
 d.getElementById('blacksmith').innerHTML='<div class="entity-workspace" data-section="events"><div class="detail-content"><div class="events-table-container"><button id="refresh"></button><p>Prior events</p></div></div></div>';
 w.__routes.push(['/events',{ok:false,status:503,statusText:'Unavailable'}]);
 await w.refreshBlacksmithEvents({currentTarget:d.getElementById('refresh')});
 expect(d.querySelector('.detail-content').textContent).toContain('Prior events');
 expect(d.querySelector('.view-refresh-error')?.textContent).toContain('retained');w.close();
});

test('latest broker log file selection wins over earlier responses',async()=>{
 const {w,d}=boot();await sleep(50);
 d.getElementById('blacksmith').innerHTML='<div class="entity-workspace" data-section="blacksmith-logs"><div class="detail-content"><div id="blacksmith-logs-display"></div></div></div>';
 const releases=[];w.__routes.push(['/b/blacksmith/logs',()=>new Promise(r=>releases.push(r))]);
 const a=w.selectBlacksmithLogFile('a.log');const b=w.selectBlacksmithLogFile('b.log');
 releases[1]({ok:true,json:async()=>({logs:'',total_lines:0})});await b;
 releases[0]({ok:true,json:async()=>({logs:'old file contents',total_lines:1})});await a;
 expect(w.blacksmithLogsPagination.currentFile).toBe('b.log');
 expect(d.getElementById('blacksmith-logs-display').textContent).not.toContain('old file contents');w.close();
});
