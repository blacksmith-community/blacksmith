const {test,expect}=require('bun:test');
const {JSDOM}=require('./certificates/support.js');
const {setupRotate,cert}=require('./rotate-flow-support.js');
const t=JSDOM?test:test.skip;
const certs=[cert('/s/a_crt','a_crt',5,'inst-a','cache-a')];
t('job name and reason say what to enter and why',async()=>{
 const {w,d}=await setupRotate({certs});try{
 const fields=[['rotate-name','Job name',/Activity/,/October certificate renewal/],['rotate-reason','Reason',/change ticket/,/CHG-1234/]];
 fields.forEach(([id,label,why,example])=>{
  const input=d.getElementById(id);
  expect(d.querySelector(`label[for="${id}"]`).textContent).toBe(label);
  expect(input.placeholder).not.toBe('Optional');
  expect(input.placeholder).toMatch(example);
  const hint=d.getElementById(input.getAttribute('aria-describedby'));
  expect(hint).toBeTruthy();
  expect(hint.id).toBe(`${id}-help`);
  expect(hint.textContent).toMatch(why);
  expect(hint.closest('.rotate-name-row')).toBe(input.closest('.rotate-name-row'));
 });
 }finally{w.close();}
});
t('the converge job name carries the same hint',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 await T.requestOperation('converge');
 expect(d.getElementById('rotate-name').getAttribute('aria-describedby')).toBe('rotate-name-help');
 expect(d.getElementById('rotate-name-help').textContent).toContain('Activity');
 }finally{w.close();}
});
t('the typed name and reason are kept in state and across operations',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 const type=(id,v)=>{const i=d.getElementById(id);i.value=v;i.dispatchEvent(new w.Event('input',{bubbles:true}));};
 type('rotate-name','October cert rotation');type('rotate-reason','CHG-1234');
 expect(T.rotateState.name).toBe('October cert rotation');expect(T.rotateState.reason).toBe('CHG-1234');
 // the fields survive a trip to another operation and back
 await T.requestOperation('upgrade');await T.requestOperation('rotate');
 expect(d.getElementById('rotate-name').value).toBe('October cert rotation');
 expect(d.getElementById('rotate-reason').value).toBe('CHG-1234');
 }finally{w.close();}
});
