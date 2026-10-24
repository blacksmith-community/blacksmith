const {test,expect}=require('bun:test');
const {JSDOM}=require('./certificates/support.js');
const {setupRotate,cert}=require('./rotate-flow-support.js');
const t=JSDOM?test:test.skip;
const certs=[cert('/s/a_crt','a_crt',10,'inst-a','cache-a'),cert('/s/b_crt','b_crt',20,'inst-b','cache-b')];
t('each step header is one heading row with no eyebrow or description',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 const header=d.querySelector('.batch-flow-header');
 const titles=[];
 for(const step of [0,1,2,3]){
  T.setBatchStep(step,false);
  expect(header.querySelectorAll('h1,h2,h3,h4').length).toBe(1);
  expect(header.querySelectorAll('p').length).toBe(1);
  expect(d.getElementById('batch-flow-description')).toBeNull();
  expect(d.getElementById('batch-selection-summary')).toBeNull();
  expect(header.querySelector('p').id).toBe('batch-flow-context');
  titles.push(d.getElementById('batch-flow-title').textContent);
 }
 expect(titles).toEqual(['What would you like to do?','Rotate certificates','Rotate certificates: choose instances','Rotate certificates: review and confirm']);
 expect(d.getElementById('batch-flow-title').tagName).toBe('H2');
 expect(d.getElementById('batch-flow-title').closest('.batch-flow-heading').parentElement).toBe(header);
 }finally{w.close();}
});
t('the selection count shows only on the Instances and Review steps',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 const count=d.getElementById('batch-flow-context');
 expect(count.getAttribute('role')).toBe('status');
 T.rotateSelection().clear();T.rotateSelection().set('inst-a',new Set(['/s/a_crt']));
 for(const step of [0,1]){T.setBatchStep(step,false);expect(count.hidden).toBe(true);expect(count.textContent).toBe('');}
 T.setBatchStep(2,false);
 expect(count.hidden).toBe(false);expect(count.textContent).toBe('1 instance selected');
 T.rotateSelection().set('inst-b',new Set(['/s/b_crt']));T.refreshUpgradeInstanceList();
 expect(count.textContent).toBe('2 instances selected');
 T.setBatchStep(3,false);
 expect(count.hidden).toBe(false);expect(count.textContent).toBe('2 instances selected');
 // the Instances step carries no second copy of the count
 T.setBatchStep(2,false);
 const copies=[...d.querySelectorAll('#upgrade')].flatMap(p=>[...p.querySelectorAll('*')].filter(n=>!n.children.length&&/\d+ instances? selected/.test(n.textContent)));
 expect(copies.map(n=>n.id)).toEqual(['batch-flow-context']);
 }finally{w.close();}
});
t('the count changes inside its live region as the selection changes',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 T.rotateSelection().clear();T.setBatchStep(2,false);T.refreshUpgradeInstanceList();
 const count=d.getElementById('batch-flow-context');
 expect(count.textContent).toBe('No instances selected');
 d.getElementById('upgrade-select-all').click();
 expect(count.textContent).toBe('2 instances selected');
 expect(count.closest('[aria-live="off"]')).toBeNull();
 }finally{w.close();}
});
