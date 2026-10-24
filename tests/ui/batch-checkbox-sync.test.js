const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;
async function setup(){
 const env=boot(),{w,d}=env;
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
 await sleep(30);
 w.serviceInstances=Object.fromEntries(Array.from({length:60},(_,i)=>[`inst-${String(i).padStart(3,'0')}`,{service_id:'valkey',plan_id:'cluster',name:`Cache ${i}`}]));
 d.getElementById('upgrade').classList.add('active');
 await w.__T.initUpgradeTab();await sleep(30);
 w.__T.setBatchStep(2,false);
 return env;
}
// Every row's highlight, checkbox property, and the selection must agree.
function agree(w,d){
 const rows=[...d.querySelectorAll('#upgrade .upgrade-instance-item')];
 expect(rows.length).toBeGreaterThan(0);
 rows.forEach(row=>{
  const on=w.__T.selectedUpgradeInstances.has(row.dataset.instanceId);
  expect([row.dataset.instanceId,row.classList.contains('selected')]).toEqual([row.dataset.instanceId,on]);
  expect([row.dataset.instanceId,row.querySelector('input[type="checkbox"]').checked]).toEqual([row.dataset.instanceId,on]);
 });
 return rows;
}
const row=(d,id)=>d.querySelector(`#upgrade .upgrade-instance-item[data-instance-id="${id}"]`);
t('a click on the row body checks the box',async()=>{
 const {w,d}=await setup();try{
 row(d,'inst-003').querySelector('.upgrade-instance-info').click();
 agree(w,d);expect(row(d,'inst-003').querySelector('input').checked).toBe(true);
 row(d,'inst-003').querySelector('.upgrade-instance-info').click();
 agree(w,d);expect(row(d,'inst-003').querySelector('input').checked).toBe(false);
 }finally{w.close();}
});
t('a click on the checkbox or its mark selects once',async()=>{
 const {w,d}=await setup();try{
 row(d,'inst-004').querySelector('input').click();
 agree(w,d);expect(w.__T.selectedUpgradeInstances.has('inst-004')).toBe(true);
 row(d,'inst-004').querySelector('.checkmark').click();
 agree(w,d);expect(w.__T.selectedUpgradeInstances.has('inst-004')).toBe(false);
 row(d,'inst-005').querySelector('.checkmark').click();
 agree(w,d);expect(w.__T.selectedUpgradeInstances.has('inst-005')).toBe(true);
 expect(d.getElementById('batch-flow-context').textContent).toBe('1 instance selected');
 }finally{w.close();}
});
t('Select this page, Select all matching, and Clear selection keep the boxes in step',async()=>{
 const {w,d}=await setup();try{
 d.getElementById('upgrade-select-all').click();
 expect(agree(w,d).every(r=>r.querySelector('input').checked)).toBe(true);
 d.getElementById('batch-select-matching').click();
 expect(w.__T.selectedUpgradeInstances.size).toBe(60);agree(w,d);
 d.getElementById('upgrade-deselect-all').click();
 expect(agree(w,d).some(r=>r.querySelector('input').checked)).toBe(false);
 }finally{w.close();}
});
t('paging and filtering redraw each box from the selection',async()=>{
 const {w,d}=await setup();try{
 row(d,'inst-001').querySelector('input').click();
 d.getElementById('batch-next').click();
 agree(w,d);row(d,'inst-055').querySelector('.upgrade-instance-info').click();
 agree(w,d);expect(row(d,'inst-055').querySelector('input').checked).toBe(true);
 d.getElementById('batch-previous').click();
 agree(w,d);expect(row(d,'inst-001').querySelector('input').checked).toBe(true);
 const search=d.getElementById('batch-search');search.value='inst-05';search.dispatchEvent(new w.Event('input'));
 agree(w,d);expect(row(d,'inst-055').querySelector('input').checked).toBe(true);
 search.value='';search.dispatchEvent(new w.Event('input'));
 agree(w,d);
 }finally{w.close();}
});
t('moving between steps keeps the boxes in step',async()=>{
 const {w,d}=await setup();try{
 row(d,'inst-002').querySelector('.upgrade-instance-info').click();
 w.__T.setBatchStep(1,false);w.__T.setBatchStep(2,false);
 agree(w,d);expect(row(d,'inst-002').querySelector('input').checked).toBe(true);
 w.__T.setBatchStep(0,false);d.querySelector('[data-batch-step="2"]').disabled=false;w.__T.setBatchStep(2,false);
 agree(w,d);
 }finally{w.close();}
});
t('a row another operation holds cannot be checked',async()=>{
 const {w,d}=await setup();try{
 w.__T.setUpgradeHolders?.(new Map());
 const held=row(d,'inst-007');held.dataset.held='true';held.querySelector('input').disabled=true;
 held.querySelector('.upgrade-instance-info').click();
 expect(w.__T.selectedUpgradeInstances.has('inst-007')).toBe(false);expect(held.querySelector('input').checked).toBe(false);
 }finally{w.close();}
});
