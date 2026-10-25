const {test,expect}=require('bun:test');
const fs=require('fs'),path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {setupRotate,cert}=require('./rotate-flow-support.js');
const t=JSDOM?test:test.skip;
const css=fs.readFileSync(path.resolve(__dirname,'..','..','ui','forge-theme.css'),'utf8');
const certs=[cert('/s/a_crt','a_crt',5,'inst-a','cache-a'),cert('/s/b_crt','b_crt',20,'inst-b','cache-b')];
const row=(d,id)=>d.querySelector(`#upgrade .upgrade-instance-item[data-instance-id="${id}"]`);
async function setupUpgrade(){
 const env=boot(),{w,d}=env;
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
 await sleep(30);
 w.serviceInstances={'inst-1':{service_id:'valkey',plan_id:'cluster',name:'Cache 1'},'inst-2':{service_id:'valkey',plan_id:'cluster',name:'Cache 2'}};
 d.getElementById('upgrade').classList.add('active');
 await w.__T.initUpgradeTab();await sleep(30);
 w.__T.setBatchStep(2,false);
 return env;
}
t('upgrade rows have no Select button and lay their cells out in columns',async()=>{
 const {w,d}=await setupUpgrade();try{
 const r=row(d,'inst-1');
 expect(r.querySelector('.batch-select-state')).toBeNull();
 expect(r.textContent).not.toMatch(/\bSelected?\b/);
 expect(r.querySelector('.upgrade-instance-service').textContent).toBe('valkey');
 expect(r.querySelector('.upgrade-instance-plan').textContent).toBe('cluster');
 expect(css).toContain('#upgrade .upgrade-instance-item { display: grid; grid-template-columns:');
 expect(css).toContain('.upgrade-instance-info { display: contents; }');
 }finally{w.close();}
});
t('a row click toggles the selection and Enter on the checkbox does too',async()=>{
 const {w,d}=await setupUpgrade();const T=w.__T;try{
 const r=row(d,'inst-1'),box=r.querySelector('input');
 r.querySelector('.upgrade-instance-id').click();
 expect([box.checked,r.classList.contains('selected'),T.selectedUpgradeInstances.has('inst-1')]).toEqual([true,true,true]);
 expect(d.getElementById('batch-flow-context').textContent).toBe('1 instance selected');
 box.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));
 expect([box.checked,r.classList.contains('selected'),T.selectedUpgradeInstances.has('inst-1')]).toEqual([false,false,false]);
 expect(box.disabled).toBe(false);expect(box.getAttribute('aria-label')).toContain('Cache 1');
 }finally{w.close();}
});
t('rotate rows drop the Select button, keep chips and checkbox, and toggle from the keyboard',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 T.setBatchStep(2,false);
 const r=row(d,'inst-a'),box=r.querySelector('input[type=checkbox]');
 expect(r.querySelector('.batch-select-state')).toBeNull();
 expect(r.textContent).not.toMatch(/\bSelected?\b/);
 expect(r.querySelector('.upgrade-instance-service').textContent).toBe('svc1');
 expect(r.querySelector('.upgrade-instance-plan').textContent).toBe('small');
 expect(r.querySelector('.rotate-chips .rotate-chip')).not.toBeNull();
 const named=r.querySelector('.upgrade-instance-name');
 expect(named.textContent).toBe('cache-a');
 expect(named.closest('.upgrade-instance-id')).toBeNull();
 expect(r.querySelector('.upgrade-instance-id').textContent).toBe('inst-a');
 expect(r.querySelector('.upgrade-instance-id').title).toBe('inst-a');
 r.querySelector('.upgrade-instance-id').click();
 // the row redraws from the selection, so read it again
 const now=row(d,'inst-a');
 expect([now.querySelector('input').checked,now.classList.contains('selected'),T.rotateSelection().has('inst-a')]).toEqual([true,true,true]);
 expect(d.getElementById('batch-flow-context').textContent).toBe('1 instance selected');
 const again=row(d,'inst-a').querySelector('input');
 again.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));
 expect(T.rotateSelection().has('inst-a')).toBe(false);
 expect(row(d,'inst-a').classList.contains('selected')).toBe(false);
 }finally{w.close();}
});
