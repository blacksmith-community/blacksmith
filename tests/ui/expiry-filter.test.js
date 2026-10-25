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
const shown=(d)=>[...d.querySelectorAll('#upgrade .upgrade-instances [data-instance-id]')].map(r=>r.dataset.instanceId).sort();
const setFilter=(w,d,value)=>{const f=d.getElementById('batch-expiring');f.value=value;f.dispatchEvent(new w.Event('change',{bubbles:true}));};

t('the filter sits with the other filters and adds no heading or description',async()=>{
 const {w,d}=await setupRotate({certs});try{
 const f=d.getElementById('batch-expiring');
 expect(f).not.toBeNull();
 const row=f.closest('.filter-row');
 expect(row.parentElement).toBe(d.getElementById('batch-eligibility').closest('.filter-row').parentElement);
 expect(row.parentElement).toBe(d.getElementById('batch-search').closest('.filter-row').parentElement);
 expect(row.querySelector('label').textContent).toBe('Certificates expiring');
 expect(row.querySelector('h1,h2,h3,h4,p')).toBeNull();
 expect(row.children.length).toBe(2);
 expect(f.value).toBe('');
 expect(f.options[0].textContent).toBe('Any');
 expect([...f.options].slice(1).map(o=>o.value)).toEqual(['7','14','30','60','90','180','365']);
 expect(row.hidden).toBe(false);
 }finally{w.close();}
});
t('the filter narrows the list, counts expired certificates, and selects nothing',async()=>{
 const {w,d,T,sleep}=await setupRotate({certs});try{
 T.setBatchStep(2,false);
 expect(shown(d)).toEqual(['inst-a','inst-b','inst-c','inst-d','inst-e']);
 setFilter(w,d,'30');await sleep(10);
 expect(shown(d)).toEqual(['inst-a','inst-b','inst-e']);
 expect(selected(T)).toEqual([]);
 setFilter(w,d,'7');await sleep(10);
 expect(shown(d)).toEqual(['inst-a','inst-e']);
 expect(selected(T)).toEqual([]);
 }finally{w.close();}
});
t('Select all matching takes only the filtered instances',async()=>{
 const {w,d,T,sleep}=await setupRotate({certs});try{
 T.setBatchStep(2,false);
 setFilter(w,d,'30');await sleep(10);
 d.getElementById('batch-select-matching').click();await sleep(10);
 expect(selected(T)).toEqual(['inst-a','inst-b','inst-e']);
 }finally{w.close();}
});
t('clearing the filter restores the list and keeps the selection in agreement',async()=>{
 const {w,d,T,sleep}=await setupRotate({certs});try{
 T.setBatchStep(2,false);
 setFilter(w,d,'30');await sleep(10);
 d.querySelector('[data-instance-id="inst-a"] .upgrade-instance-id').click();
 setFilter(w,d,'7');await sleep(10);
 setFilter(w,d,'');await sleep(10);
 expect(shown(d)).toEqual(['inst-a','inst-b','inst-c','inst-d','inst-e']);
 expect(selected(T)).toEqual(['inst-a']);
 const row=(id)=>d.querySelector(`[data-instance-id="${id}"]`);
 expect(row('inst-a').querySelector('input').checked).toBe(true);
 expect(row('inst-a').classList.contains('selected')).toBe(true);
 expect(row('inst-b').querySelector('input').checked).toBe(false);
 expect(row('inst-b').classList.contains('selected')).toBe(false);
 expect(d.getElementById('batch-flow-context').textContent).toBe('1 instance selected');
 // a selection the filter hides stays selected and counted
 setFilter(w,d,'90');await sleep(10);
 d.querySelector('[data-instance-id="inst-b"] .upgrade-instance-id').click();
 setFilter(w,d,'7');await sleep(10);
 expect(shown(d)).toEqual(['inst-a','inst-e']);
 expect(selected(T)).toEqual(['inst-a','inst-b']);
 expect(d.getElementById('batch-flow-context').textContent).toBe('2 instances selected · 1 hidden by filters');
 }finally{w.close();}
});
t('an inventory refresh recomputes the filtered list',async()=>{
 const {w,d,T,sleep,setInventory}=await setupRotate({certs});try{
 T.setBatchStep(2,false);
 setFilter(w,d,'30');await sleep(10);
 setInventory({certificates:certs.filter(c=>c.key!=='/s/a_crt')});
 await T.loadRotateInventory({silent:true});await sleep(30);
 expect(shown(d)).toEqual(['inst-b','inst-e']);
 }finally{w.close();}
});
