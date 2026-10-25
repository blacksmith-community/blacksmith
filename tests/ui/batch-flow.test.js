const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;
async function setup(){
 const env=boot(),{w,d}=env;
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[{operating_system:'ubuntu',version:'1'}])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
 await sleep(30);w.serviceInstances={cache:{service_id:'valkey',plan_id:'small'}};
 await w.__T.initUpgradeTab();return env;
}
t('focused steps preserve choices across breadcrumbs and Activity',async()=>{
 const {w,d}=await setup();try{
 const next=d.getElementById('batch-flow-next');
 expect(Boolean(next)).toBe(true);
 expect(d.getElementById('batch-new').textContent).toBe('New Operation');
 expect(w.__T.batchFlow.step).toBe(0);
 expect(d.querySelector('.upgrade-targets-panel').hidden).toBe(true);
 next.click();expect(w.__T.batchFlow.step).toBe(1);expect(next.disabled).toBe(true);
 const target=d.getElementById('upgrade-stemcell-filter');target.value='ubuntu/1';target.dispatchEvent(new w.Event('change',{bubbles:true}));await sleep(0);
 expect(next.disabled).toBe(false);next.click();expect(w.__T.batchFlow.step).toBe(2);
 d.getElementById('upgrade-select-all').click();
 d.querySelector('[data-batch-step="0"]').click();
 expect(w.__T.selectedUpgradeInstances.has('cache')).toBe(true);
 d.getElementById('batch-activity').click();d.getElementById('batch-new').click();
 expect(w.__T.batchFlow.step).toBe(0);expect(target.value).toBe('ubuntu/1');
 expect(d.getElementById('rotate-op-upgrade').closest('label').classList.contains('is-selected')).toBe(true);
 }finally{w.close();}
});
t('inline review requires acknowledgment and cancels when returning to edit',async()=>{
 const {w,d}=await setup();try{
 const F=w.__T.batchFlow;F.step=3;
 const body=d.createElement('p');body.textContent='Server preview';
 const pending=w.__T.showBatchReview({title:'Review rotation',body,requireText:'redeploy',confirmText:'Rotate and redeploy'},F.revision);
 expect(d.getElementById('batch-flow-next').disabled).toBe(true);
 const ack=d.getElementById('batch-flow-ack');ack.value='redeploy';ack.dispatchEvent(new w.Event('input',{bubbles:true}));
 expect(d.getElementById('batch-flow-next').disabled).toBe(false);
 w.__T.setBatchStep(2);expect(await pending).toBe(false);
 expect(await w.__T.showBatchReview({title:'Stale'},F.revision-1)).toBe(false);
 }finally{w.close();}
});

t('a refreshed server preview requires a new explicit confirmation',async()=>{
 const {w,d}=await setup();try{
 const F=w.__T.batchFlow;F.step=3;
 const first=w.__T.showBatchReview({title:'First preview',requireText:'redeploy',confirmText:'Rotate'},F.revision);
 const ack=d.getElementById('batch-flow-ack');ack.value='redeploy';ack.dispatchEvent(new w.Event('input'));
 d.getElementById('batch-flow-next').click();expect((await first).confirmed).toBe(true);expect(F.submitting).toBe(true);
 const refreshed=w.__T.showBatchReview({title:'Updated preview',requireText:'redeploy',confirmText:'Rotate'},F.revision);
 expect(F.submitting).toBe(false);expect(d.getElementById('batch-flow-ack').value).toBe('');expect(d.getElementById('batch-flow-next').disabled).toBe(true);
 w.__T.setBatchStep(2);expect(await refreshed).toBe(false);
 }finally{w.close();}
});

t('instance selection retains access to certificate removal controls',async()=>{
 const {w,d}=await setup();try{
 await w.__T.requestOperation('rotate');w.__T.setBatchStep(2);
 const edit=d.getElementById('rotate-review');
 expect(d.getElementById('batch-selection-review').contains(edit)).toBe(true);
 expect(edit.closest('.upgrade-footer')).toBeNull();
 }finally{w.close();}
});

t('a row click toggles once and the rows omit meaningless row numbers',async()=>{
 const {w,d}=await setup();try{
 const row=d.querySelector('.upgrade-instance-item');
 expect(row.querySelector('.upgrade-instance-row-number')).toBeNull();
 row.querySelector('.upgrade-instance-id').click();
 expect(row.querySelector('input').checked).toBe(true);
 expect(w.__T.selectedUpgradeInstances.has('cache')).toBe(true);
 row.querySelector('.upgrade-instance-id').click();
 expect(row.querySelector('input').checked).toBe(false);
 }finally{w.close();}
});
t('operation metadata explains purpose and gives examples',async()=>{
 const {w,d}=await setup();try{
 await w.__T.requestOperation('rotate');
 for(const id of ['rotate-name','rotate-reason']){
 const input=d.getElementById(id);
 expect(input.placeholder).not.toBe('Optional');
 expect(d.getElementById(input.getAttribute('aria-describedby')).textContent.length).toBeGreaterThan(30);
 }
 }finally{w.close();}
});

t('mode tabs expose the selected method and support keyboard navigation',async()=>{
 const {w,d}=await setup();try{
 await w.__T.requestOperation('rotate');
 const first=d.getElementById('rotate-mode-redeploy-tab');
 expect(first.getAttribute('role')).toBe('tab');
 expect(first.getAttribute('aria-selected')).toBe('true');
 const panel=d.getElementById(first.getAttribute('aria-controls'));
 expect(panel.getAttribute('aria-labelledby')).toBe(first.id);
 expect(panel.textContent).toContain('canaries and drains');
 first.dispatchEvent(new w.KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true}));
 expect(d.activeElement.getAttribute('role')).toBe('tab');
 }finally{w.close();}
});

t('review presents a small selection without redundant cards or pagination',async()=>{
 const {w,d}=await setup();try{
 w.serviceInstances.cache.instance_name='Production cache';
 const summary=w.__T.buildUpgradeFlowSummary(['cache'],['bpm/1.4.41']);
 expect(summary.querySelector('.batch-pagination').hidden).toBe(true);
 expect(summary.querySelector('.batch-review-fact')).toBeNull();
 expect(summary.querySelector('.batch-instance-name').textContent).toBe('Production cache');
 expect(summary.querySelector('.batch-instance-id').textContent).toBe('cache');
 const large=w.__T.buildUpgradeFlowSummary(Array.from({length:51},(_,i)=>String(i)),['bpm/1']);
 expect(large.querySelector('.batch-pagination').hidden).toBe(false);
 expect(large.querySelectorAll('.batch-instance-name').length).toBe(50);
 large.querySelector('.batch-pagination button:last-child').click();
 expect(large.querySelectorAll('.batch-instance-name').length).toBe(1);
 }finally{w.close();}
});

t('upgrade options use System and Releases rows and an applied release summary',async()=>{
 const {w,d}=await setup();try{
 expect([...d.querySelectorAll('.batch-upgrade-options-table tbody th')].map(el=>el.textContent)).toEqual(['System','Releases']);
 expect(d.querySelector('details.upgrade-advanced')).toBeNull();
 const source=d.querySelector('.upgrade-advanced-releases');
 source.innerHTML='<select id="upgrade-advrel-bpm" data-release-name="bpm"><option value="">Keep current</option><option value="1.4">1.4</option></select>';
 source.querySelector('select').value='1.4';w.__T.syncAdvancedReleaseSummary();
 expect(source.hidden).toBe(true);
 expect(d.getElementById('batch-release-summary').textContent).toContain('bpm');
 expect(d.getElementById('batch-release-summary').textContent).toContain('1.4');
 d.querySelector('#batch-release-summary button').click();
 expect(source.querySelector('select').value).toBe('');
 }finally{w.close();}
});

t('release picker keeps drafts isolated until Apply and rejects stale versions',async()=>{
 const {w,d}=await setup();try{
 await sleep(30);
 const source=d.querySelector('.upgrade-advanced-releases');
 source.innerHTML='<select id="upgrade-advrel-bpm" data-release-name="bpm"><option value="">Keep current</option><option value="1.4">1.4</option><option value="1.5">1.5</option></select>';
 const original=source.querySelector('select');original.value='1.4';w.__T.syncAdvancedReleaseSummary();
 let pending=w.__T.openAdvancedReleasePicker();
 let draft=d.querySelector('.batch-release-picker select');draft.value='1.5';draft.dispatchEvent(new w.Event('change'));
 expect(original.value).toBe('1.4');d.querySelector('[data-dialog-role="cancel"]').click();await pending;expect(original.value).toBe('1.4');
 pending=w.__T.openAdvancedReleasePicker();draft=d.querySelector('.batch-release-picker select');draft.value='1.5';draft.dispatchEvent(new w.Event('change'));
 d.querySelector('[data-dialog-role="confirm"]').click();await pending;expect(original.value).toBe('1.5');
 pending=w.__T.openAdvancedReleasePicker();draft=d.querySelector('.batch-release-picker select');draft.value='1.4';draft.dispatchEvent(new w.Event('change'));
 original.querySelector('[value="1.4"]').remove();
 d.querySelector('[data-dialog-role="confirm"]').click();await pending;expect(original.value).toBe('1.5');
 }finally{w.close();}
});

t('quick navigation selects each operation and opens Options',async()=>{
 const {w,d}=await setup();try{
 w.blacksmithNav.switchTab=async()=>true;
 for(const mode of ['upgrade','rotate','converge','upgrade']){
  d.getElementById('batch-activity').click();
  expect(await w.blacksmithBatch.openOperation(mode)).toBe(true);
  expect(w.__T.batchFlow.step).toBe(1);
  expect(d.getElementById('rotate-op-'+mode).checked).toBe(true);
  expect(d.getElementById('batch-new').getAttribute('aria-pressed')).toBe('true');
 }
 w.blacksmithNav.switchTab=async()=>false;
 expect(await w.blacksmithBatch.openOperation('rotate')).toBe(false);
 expect(d.getElementById('rotate-op-upgrade').checked).toBe(true);
 }finally{w.close();}
});

t('batch shortcuts navigate views and steps without submitting or intercepting typing',async()=>{
 const {w,d}=await setup();try{
 const panel=d.getElementById('upgrade');panel.classList.add('active');
 const key=(code,target=panel)=>target.dispatchEvent(new w.KeyboardEvent('keydown',{code,key:code.replace('Key',''),altKey:true,shiftKey:true,bubbles:true,cancelable:true}));
 key('KeyA');expect(d.getElementById('batch-activity').getAttribute('aria-pressed')).toBe('true');
 key('KeyN');expect(w.__T.batchFlow.step).toBe(0);
 const modal=d.createElement('dialog');modal.setAttribute('open','');d.body.append(modal);
 key('KeyA');expect(d.getElementById('batch-new').getAttribute('aria-pressed')).toBe('true');modal.remove();
 key('KeyR');await sleep(0);expect(d.getElementById('rotate-op-rotate').checked).toBe(true);expect(w.__T.batchFlow.step).toBe(1);
 key('KeyU');await sleep(0);key('KeyN');

 key('ArrowRight');expect(w.__T.batchFlow.step).toBe(1);
 key('ArrowRight');expect(w.__T.batchFlow.step).toBe(1); // no target selected
 key('ArrowLeft');expect(w.__T.batchFlow.step).toBe(0);
 const input=d.getElementById('batch-search');
 key('KeyA',input);expect(d.getElementById('batch-new').getAttribute('aria-pressed')).toBe('true');
 w.__T.batchFlow.step=3;let clicked=0;d.getElementById('batch-flow-next').addEventListener('click',()=>clicked++);
 key('ArrowRight');expect(clicked).toBe(0);
 panel.classList.remove('active');key('KeyA');expect(d.getElementById('batch-new').getAttribute('aria-pressed')).toBe('true');
 }finally{w.close();}
});
