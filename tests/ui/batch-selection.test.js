const {test,expect}=require('bun:test');
const {JSDOM}=require('./certificates/support.js');
const {setupRotate,cert}=require('./rotate-flow-support.js');
const t=JSDOM?test:test.skip;
const certs=[
 cert('/s/a_crt','a_crt',5,'inst-a','cache-a'),
 cert('/s/b_crt','b_crt',20,'inst-b','cache-b'),
 cert('/s/c_crt','c_crt',100,'inst-c','cache-c'),
 cert('/s/d_crt','d_crt',400,'inst-d','cache-d'),
 cert('/s/e_crt','e_crt',-3,'inst-e','cache-e')
];
const rowIds=(d)=>[...d.querySelectorAll('#upgrade .rotate-row')].map(r=>r.dataset.instanceId);
const selected=(T)=>[...T.rotateSelection().keys()].sort();
const next=(d)=>d.getElementById('batch-flow-next');

t('rotation opens with nothing selected and Next stays disabled',async()=>{
 const {w,d,T,sleep}=await setupRotate({certs});try{
 // a, b, and c all expire within 30 days or a window the old default used
 expect(T.rotateSelection().size).toBe(0);
 T.setBatchStep(2,false);
 expect(d.getElementById('batch-flow-context').textContent).toBe('No instances selected');
 expect(next(d).disabled).toBe(true);
 expect(d.getElementById('batch-flow-help').textContent).toContain('Select at least one');
 await T.loadRotateInventory({silent:true});await sleep(20);
 expect(T.rotateSelection().size).toBe(0);
 d.querySelector('[data-instance-id="inst-a"] .upgrade-instance-id').click();
 expect(selected(T)).toEqual(['inst-a']);
 expect(d.getElementById('batch-flow-context').textContent).toBe('1 instance selected');
 await sleep(5);expect(next(d).disabled).toBe(false);
 expect(d.getElementById('rotate-threshold')).toBeNull();
 }finally{w.close();}
});
t('a selection handed over from the Certificates tab keeps its count and names its source',async()=>{
 const {w,d,T,sleep}=await setupRotate({certs,start:false});try{
 const sel=w.blacksmithCerts.selection;
 sel.set('inst-a',new Set(['/s/a_crt']));sel.set('inst-c',new Set(['/s/c_crt']));
 expect(await w.blacksmithBatch.openRotate()).not.toBe(false);await sleep(100);
 expect(selected(T)).toEqual(['inst-a','inst-c']);
 expect(T.batchFlow.step).toBe(2);
 const count=d.getElementById('batch-flow-context');
 expect(count.textContent).toBe('2 instances selected from the Certificates tab');
 await sleep(5);expect(next(d).disabled).toBe(false);
 expect(d.getElementById('batch-pick-instances').getAttribute('aria-pressed')).toBe('true');
 // clearing the selection drops the source with it
 d.getElementById('upgrade-deselect-all').click();
 expect(count.textContent).toBe('No instances selected');
 d.querySelector('[data-instance-id="inst-b"] .upgrade-instance-id').click();
 expect(count.textContent).toBe('1 instance selected');
 }finally{w.close();}
});
t('Expiring within selects exactly the instances the window matches',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 T.setBatchStep(2,false);
 d.getElementById('batch-pick-expiring').click();
 const horizons=[...d.querySelectorAll('#batch-horizons [data-horizon]')];
 expect(horizons.map(b=>b.dataset.horizon)).toEqual(['7','14','30','60','90','180','365']);
 expect(horizons.map(b=>b.firstChild.textContent)).toEqual(['1 week','2 weeks','30 days','60 days','90 days','180 days','1 year']);
 expect(T.rotateSelection().size).toBe(0);
 expect(rowIds(d)).toEqual([]);
 const press=(days)=>d.querySelector(`#batch-horizons [data-horizon="${days}"]`).click();
 press(7);expect(selected(T)).toEqual(['inst-a']);expect(rowIds(d)).toEqual(['inst-a']);
 expect(d.getElementById('batch-flow-context').textContent).toBe('1 instance selected');
 expect(d.getElementById('batch-pick-note').textContent).toContain('1 instance has a certificate expiring within 1 week');
 press(30);expect(selected(T)).toEqual(['inst-a','inst-b']);
 press(365);expect(selected(T)).toEqual(['inst-a','inst-b','inst-c']);
 expect(d.querySelector('#batch-horizons [data-horizon="365"]').getAttribute('aria-pressed')).toBe('true');
 expect(d.querySelector('#batch-horizons [data-horizon="7"]').getAttribute('aria-pressed')).toBe('false');
 // expired and later-expiring certificates never match
 expect(selected(T)).not.toContain('inst-d');expect(selected(T)).not.toContain('inst-e');
 expect(d.querySelector('#batch-horizons [data-horizon="365"]').getAttribute('aria-label')).toBe('1 year, 3 instances');
 // the matched list is read only
 d.querySelector('[data-instance-id="inst-a"] input').click();
 d.querySelector('[data-instance-id="inst-a"] .upgrade-instance-id').click();
 expect(selected(T)).toEqual(['inst-a','inst-b','inst-c']);
 press(365);expect(T.rotateSelection().size).toBe(0);
 }finally{w.close();}
});
t('the two ways of choosing clear each other',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 T.setBatchStep(2,false);
 d.querySelector('[data-instance-id="inst-d"] .upgrade-instance-id').click();
 expect(selected(T)).toEqual(['inst-d']);
 d.getElementById('batch-pick-expiring').click();
 expect(T.rotateSelection().size).toBe(0);
 expect(d.getElementById('batch-horizons').hidden).toBe(false);
 expect(d.querySelector('#upgrade .upgrade-actions').hidden).toBe(true);
 d.querySelector('#batch-horizons [data-horizon="30"]').click();
 expect(selected(T)).toEqual(['inst-a','inst-b']);
 d.getElementById('batch-pick-instances').click();
 expect(T.rotateSelection().size).toBe(0);
 expect(d.getElementById('batch-horizons').hidden).toBe(true);
 expect(d.querySelector('#upgrade .upgrade-actions').hidden).toBe(false);
 expect(rowIds(d).length).toBe(5);
 d.getElementById('batch-pick-expiring').click();
 expect(T.rotateState.horizon).toBe(0);expect(T.rotateSelection().size).toBe(0);
 }finally{w.close();}
});
t('the matched instances follow an inventory refresh',async()=>{
 const {w,d,T,sleep,setInventory}=await setupRotate({certs});try{
 T.setBatchStep(2,false);
 d.getElementById('batch-pick-expiring').click();
 d.querySelector('#batch-horizons [data-horizon="30"]').click();
 expect(selected(T)).toEqual(['inst-a','inst-b']);
 setInventory({certificates:[...certs,cert('/s/c_crt2','c2_crt',25,'inst-c','cache-c')]});
 await T.loadRotateInventory({silent:true});await sleep(20);
 expect(selected(T)).toEqual(['inst-a','inst-b','inst-c']);
 expect(d.getElementById('batch-flow-context').textContent).toBe('3 instances selected');
 setInventory({certificates:certs.filter(c=>c.key!=='/s/a_crt')});
 await T.loadRotateInventory({silent:true});await sleep(20);
 expect(selected(T)).toEqual(['inst-b']);
 }finally{w.close();}
});
t('the preview request carries the matched instances and their certificates',async()=>{
 const {w,d,T,sleep,calls}=await setupRotate({certs});try{
 w.__routes.unshift(['/b/certificates/rotation/preview',(u,o,j)=>j(400,{success:false,error:'stop here',code:'bad_request'})]);
 T.setBatchStep(2,false);
 d.getElementById('batch-pick-expiring').click();
 d.querySelector('#batch-horizons [data-horizon="30"]').click();await sleep(5);
 next(d).click();await sleep(80);
 const post=calls.filter(c=>c.startsWith('POST')&&c.includes('rotation/preview')).pop();
 expect(post).toBeTruthy();
 const body=JSON.parse(post.slice(post.indexOf(' {')+1));
 expect(body.selections.map(s=>s.instance_id).sort()).toEqual(['inst-a','inst-b']);
 expect(body.selections.find(s=>s.instance_id==='inst-a').variables).toEqual(['/s/a_crt']);
 }finally{w.close();}
});
