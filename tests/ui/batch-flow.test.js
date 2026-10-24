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
 const pending=w.__T.showBatchReview({title:'Review rotation',body,requireText:'rotate',confirmText:'Rotate and redeploy'},F.revision);
 expect(d.getElementById('batch-flow-next').disabled).toBe(true);
 const ack=d.getElementById('batch-flow-ack');ack.value='rotate';ack.dispatchEvent(new w.Event('input',{bubbles:true}));
 expect(d.getElementById('batch-flow-next').disabled).toBe(false);
 w.__T.setBatchStep(2);expect(await pending).toBe(false);
 expect(await w.__T.showBatchReview({title:'Stale'},F.revision-1)).toBe(false);
 }finally{w.close();}
});

t('a refreshed server preview requires a new explicit confirmation',async()=>{
 const {w,d}=await setup();try{
 const F=w.__T.batchFlow;F.step=3;
 const first=w.__T.showBatchReview({title:'First preview',requireText:'rotate',confirmText:'Rotate'},F.revision);
 const ack=d.getElementById('batch-flow-ack');ack.value='rotate';ack.dispatchEvent(new w.Event('input'));
 d.getElementById('batch-flow-next').click();expect((await first).confirmed).toBe(true);expect(F.submitting).toBe(true);
 const refreshed=w.__T.showBatchReview({title:'Updated preview',requireText:'rotate',confirmText:'Rotate'},F.revision);
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
