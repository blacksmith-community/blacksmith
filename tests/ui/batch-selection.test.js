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
const selected=(T)=>[...T.rotateSelection().keys()].sort();
const next=(d)=>d.getElementById('batch-flow-next');

t('rotation opens with nothing selected and no threshold on the Options step',async()=>{
 const {w,d,T,sleep}=await setupRotate({certs});try{
 expect(d.getElementById('rotate-threshold')).toBeNull();
 expect(d.getElementById('rotate-select-matching')).toBeNull();
 expect(d.querySelector('#upgrade .upgrade-targets').textContent).not.toContain('Start with expiring certificates');
 expect(selected(T)).toEqual([]);
 T.setBatchStep(2,false);
 expect(d.getElementById('batch-flow-context').textContent).toBe('No instances selected');
 await sleep(5);expect(next(d).disabled).toBe(true);
 // the operator picks, and a refresh keeps the pick
 d.querySelector('[data-instance-id="inst-a"] .upgrade-instance-id').click();
 await T.loadRotateInventory({silent:true});await sleep(20);
 expect(selected(T)).toEqual(['inst-a']);
 expect(d.getElementById('batch-flow-context').textContent).toBe('1 instance selected');
 d.getElementById('upgrade-deselect-all').click();
 expect(T.rotateSelection().size).toBe(0);
 expect(d.getElementById('batch-flow-context').textContent).toBe('No instances selected');
 await sleep(5);expect(next(d).disabled).toBe(true);
 expect(d.getElementById('batch-flow-help').textContent).toContain('Select at least one');
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
 // clearing the selection drops the source with it
 d.getElementById('upgrade-deselect-all').click();
 expect(count.textContent).toBe('No instances selected');
 d.querySelector('[data-instance-id="inst-b"] .upgrade-instance-id').click();
 expect(count.textContent).toBe('1 instance selected');
 }finally{w.close();}
});
