const {test,expect}=require('bun:test');
const {JSDOM}=require('./certificates/support.js');
const {setupRotate,cert}=require('./rotate-flow-support.js');
const t=JSDOM?test:test.skip;
const certs=[
 cert('/s/a_crt','a_crt',5,'inst-a','cache-a'),
 cert('/s/b_crt','b_crt',20,'inst-b','cache-b'),
 cert('/s/c_crt','c_crt',100,'inst-c','cache-c')
];
const selected=(T)=>[...T.rotateSelection().keys()].sort();
const next=(d)=>d.getElementById('batch-flow-next');
const press=(d,days)=>d.querySelector(`#batch-horizons [data-horizon="${days}"]`).click();
const pvInst=(id)=>({instance_id:id,instance_name:id+'-name',service:'valkey',plan:'small',vms:1,mode:'redeploy',gate:'g',manifest_digest:'d',selected:[],additional:[],consumers:[],blocked:null,impact:'x'});

t('a create in Expiring within does not pick the same instances again',async()=>{
 const {w,d,T,sleep}=await setupRotate({certs});try{
 w.__routes.unshift(['/b/certificates/rotation/preview',(u,o,j)=>j(200,{preview_id:'pv-1',expires_at:'2027-01-01T00:00:00Z',instances:[pvInst('inst-a'),pvInst('inst-b')],warnings:[]})],
  [/\/b\/upgrade\/tasks$/,(u,o,j)=>o.method==='POST'?j(201,{id:'task-1'}):j(200,[])]);
 T.setBatchStep(2,false);
 d.getElementById('batch-pick-expiring').click();press(d,30);await sleep(5);
 expect(selected(T)).toEqual(['inst-a','inst-b']);
 next(d).click();await sleep(80);
 expect(T.batchFlow.step).toBe(3);
 expect(T.batchFlow.review).toBeTruthy();
 next(d).click();await sleep(150);
 expect(T.rotateState.pick).toBe('expiring');
 expect(T.rotateState.horizon).toBe(0);
 expect(T.rotateSelection().size).toBe(0);
 expect(d.querySelectorAll('#batch-horizons [aria-pressed="true"]').length).toBe(0);
 }finally{w.close();}
});
t('Expiring within leaves the Certificates tab selection alone',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 T.setBatchStep(2,false);
 d.getElementById('batch-pick-expiring').click();press(d,30);
 const shared=w.blacksmithCerts.selection;
 expect(shared.size).toBe(0);
 expect(T.rotateState.derived.size).toBe(2);
 // a later render does not copy the derived pairs into the shared map
 T.renderRotateInstances();
 expect(shared.size).toBe(0);expect(selected(T)).toEqual(['inst-a','inst-b']);
 }finally{w.close();}
});
t('the Certificates tab label goes away on the first manual change',async()=>{
 const {w,d,T,sleep}=await setupRotate({certs,start:false});try{
 const sel=w.blacksmithCerts.selection;
 sel.set('inst-a',new Set(['/s/a_crt']));sel.set('inst-b',new Set(['/s/b_crt']));
 expect(await w.blacksmithBatch.openRotate()).not.toBe(false);await sleep(100);
 const count=d.getElementById('batch-flow-context');
 expect(count.textContent).toBe('2 instances selected from the Certificates tab');
 d.querySelector('[data-instance-id="inst-c"] .upgrade-instance-id').click();
 expect(count.textContent).toBe('3 instances selected');
 expect(T.rotateState.source).toBe('');
 }finally{w.close();}
});
t('the unavailable Patch reason follows the Redeploy description',async()=>{
 const {w,d}=await setupRotate({certs,caps:{rotation:{enabled:true},patch_in_place:{enabled:false,reason:'the switch is off'}}});try{
 const root=d.getElementById('rotate-mode');
 const kids=[...root.children];
 const panel=d.getElementById('rotate-mode-redeploy').getAttribute('aria-controls');
 const reason=d.getElementById('rotate-mode-patch-reason');
 expect(kids.indexOf(root.querySelector('[role="tablist"]'))).toBe(0);
 expect(kids[1].id).toBe(panel);
 expect(kids.indexOf(reason)).toBeGreaterThan(1);
 expect(d.getElementById(panel).firstElementChild.className).toContain('rotate-mode-description');
 }finally{w.close();}
});
t('the window counts against the server clock',async()=>{
 const far=[cert('/s/f_crt','f_crt',12,'inst-f','cache-f')];
 const {w,d,T}=await setupRotate({certs:far});try{
 T.setBatchStep(2,false);
 d.getElementById('batch-pick-expiring').click();
 w.blacksmithCerts.serverNow=()=>Date.now()+10*86400000;
 press(d,7);
 expect(selected(T)).toEqual(['inst-f']);
 }finally{w.close();}
});
t('a background refresh keeps the focus on a horizon button',async()=>{
 const {w,d,T,sleep}=await setupRotate({certs});try{
 T.setBatchStep(2,false);
 d.getElementById('batch-pick-expiring').click();press(d,30);
 const button=d.querySelector('#batch-horizons [data-horizon="60"]');
 button.focus();
 await T.loadRotateInventory({silent:true});await sleep(30);
 expect(d.querySelector('#batch-horizons [data-horizon="60"]')).toBe(button);
 expect(d.activeElement).toBe(button);
 }finally{w.close();}
});
t('the Review count stays with the list that was previewed',async()=>{
 const {w,d,T,sleep,setInventory}=await setupRotate({certs});try{
 w.__routes.unshift(['/b/certificates/rotation/preview',(u,o,j)=>j(400,{success:false,error:'stop here',code:'bad_request'})]);
 T.setBatchStep(2,false);
 d.getElementById('batch-pick-expiring').click();press(d,30);await sleep(5);
 next(d).click();await sleep(80);
 expect(T.batchFlow.step).toBe(3);
 const count=d.getElementById('batch-flow-context');
 expect(count.textContent).toBe('2 instances selected');
 setInventory({certificates:certs.filter(c=>c.key!=='/s/a_crt')});
 await T.loadRotateInventory({silent:true});await sleep(30);
 expect(count.textContent).toBe('2 instances selected');
 }finally{w.close();}
});
t('rotate rows draw their boxes from the selection',async()=>{
 const {w,d,T,sleep}=await setupRotate({certs});try{
 T.setBatchStep(2,false);
 const box=(id)=>d.querySelector(`[data-instance-id="${id}"] input`);
 d.getElementById('upgrade-select-all').click();await sleep(5);
 expect(box('inst-a').checked).toBe(true);expect(box('inst-c').checked).toBe(true);
 d.getElementById('upgrade-deselect-all').click();await sleep(5);
 expect(box('inst-a').checked).toBe(false);
 d.querySelector('[data-instance-id="inst-b"] .upgrade-instance-id').click();
 expect(box('inst-b').checked).toBe(true);
 expect(d.querySelector('[data-instance-id="inst-b"]').classList.contains('selected')).toBe(true);
 }finally{w.close();}
});
t('a handover from the Certificates tab wins over an open Expiring within',async()=>{
 const {w,d,T,sleep}=await setupRotate({certs});try{
 T.setBatchStep(2,false);
 d.getElementById('batch-pick-expiring').click();
 expect(T.rotateState.pick).toBe('expiring');expect(T.rotateSelection().size).toBe(0);
 const sel=w.blacksmithCerts.selection;
 sel.set('inst-b',new Set(['/s/b_crt']));sel.set('inst-c',new Set(['/s/c_crt']));
 expect(await w.blacksmithBatch.openRotate()).not.toBe(false);await sleep(100);
 expect(T.rotateState.pick).toBe('instances');
 expect(selected(T)).toEqual(['inst-b','inst-c']);
 expect(d.getElementById('batch-flow-context').textContent).toBe('2 instances selected from the Certificates tab');
 const box=(id)=>d.querySelector(`[data-instance-id="${id}"] input`);
 expect(box('inst-b').checked).toBe(true);expect(box('inst-c').checked).toBe(true);expect(box('inst-a').checked).toBe(false);
 }finally{w.close();}
});
t('the window picker says expired certificates are included',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 T.setBatchStep(2,false);
 expect(d.getElementById('batch-pick-expiring').textContent).toContain('expired included');
 expect(d.getElementById('batch-horizons').getAttribute('aria-label')).toContain('expired included');
 }finally{w.close();}
});
