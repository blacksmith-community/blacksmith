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
t('the unavailable Patch reason sits between the method tabs and their panel',async()=>{
 const {w,d}=await setupRotate({certs,caps:{rotation:{enabled:true},patch_in_place:{enabled:false,reason:'the switch is off'}}});try{
 const root=d.querySelector('#rotate-mode bs-tabs');
 const kids=[...root.children];
 const reason=d.getElementById('rotate-mode-patch-reason');
 const panel=d.getElementById(d.getElementById('rotate-mode-redeploy-tab').getAttribute('aria-controls'));
 expect(reason.hidden).toBe(false);
 expect(kids.indexOf(root.querySelector('[role="tablist"]'))).toBeLessThan(kids.indexOf(reason));
 expect(kids.indexOf(reason)).toBeLessThan(kids.indexOf(panel));
 expect(panel.firstElementChild.className).toContain('batch-mode-description');
 }finally{w.close();}
});
t('the Review count stays with the list that was previewed',async()=>{
 const {w,d,T,sleep,setInventory}=await setupRotate({certs});try{
 w.__routes.unshift(['/b/certificates/rotation/preview',(u,o,j)=>j(400,{success:false,error:'stop here',code:'bad_request'})]);
 T.setBatchStep(2,false);
d.querySelector('[data-instance-id="inst-a"] .upgrade-instance-id').click();
 d.querySelector('[data-instance-id="inst-b"] .upgrade-instance-id').click();
 expect(selected(T)).toEqual(['inst-a','inst-b']);await sleep(5);
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
t('a handover from the Certificates tab skips the expiring preselection',async()=>{
 const {w,d,T,sleep}=await setupRotate({certs,start:false});try{
 const sel=w.blacksmithCerts.selection;
 sel.set('inst-b',new Set(['/s/b_crt']));sel.set('inst-c',new Set(['/s/c_crt']));
 expect(await w.blacksmithBatch.openRotate()).not.toBe(false);await sleep(100);
 expect(selected(T)).toEqual(['inst-b','inst-c']);
 expect(d.getElementById('batch-flow-context').textContent).toBe('2 instances selected from the Certificates tab');
 const box=(id)=>d.querySelector(`[data-instance-id="${id}"] input`);
 expect(box('inst-b').checked).toBe(true);expect(box('inst-c').checked).toBe(true);expect(box('inst-a').checked).toBe(false);
 }finally{w.close();}
});
