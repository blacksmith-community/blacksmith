const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

async function review(requireText,typedSteps){
 const {w,d}=boot();
 try{
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
  await sleep(30);await w.__T.initUpgradeTab();
  const F=w.__T.batchFlow;F.step=3;
  w.__T.showBatchReview({title:'Review',requireText,confirmText:'Run'},F.revision);
  const ack=d.getElementById('batch-flow-ack');
  const label=d.querySelector('.batch-ack-label');
  const snap=()=>({disabled:d.getElementById('batch-flow-next').disabled,help:d.getElementById('batch-flow-help').textContent});
  const out={labelText:label.textContent,chip:label.querySelector('.confirm-word')?.textContent,placeholder:ack.placeholder,empty:snap(),steps:[]};
  for(const v of typedSteps||[]){ack.value=v;ack.dispatchEvent(new w.Event('input',{bubbles:true}));out.steps.push({v,...snap()});}
  return out;
 }finally{w.close();}
}

t('the label sets the phrase apart and never ends in confirm',async()=>{
 const r=await review('run as vcap');
 expect(r.labelText).toBe('To confirm, type "run as vcap" below.');
 expect(r.chip).toBe('"run as vcap"');
 expect(r.labelText.endsWith('confirm')).toBe(false);
 expect(/run as vcap\s*confirm/i.test(r.labelText)).toBe(false);
});
t('the placeholder equals the phrase',async()=>{
 expect((await review('run as root')).placeholder).toBe('run as root');
});
t('an empty field gets a plain instruction naming the phrase',async()=>{
 const r=await review('run as vcap');
 expect(r.empty.disabled).toBe(true);
 expect(r.empty.help).toContain('"run as vcap"');
 expect(r.labelText).toContain('"run as vcap"');
 expect(r.empty.help).not.toMatch(/doesn't match/);
});
t('typing the word confirm keeps the button off and says it does not match',async()=>{
 const r=await review('run as vcap',['confirm']);
 expect(r.steps[0].disabled).toBe(true);
 expect(r.steps[0].help).toBe('That doesn\'t match. Type "run as vcap" exactly, with the phrase shown in the label.');
});
t('typing the phrase one character at a time enables the button only at the end',async()=>{
 const phrase='run as vcap';
 const steps=Array.from({length:phrase.length},(_,i)=>phrase.slice(0,i+1));
 const r=await review(phrase,steps);
 r.steps.forEach((s,i)=>expect(s.disabled).toBe(i<phrase.length-1));
 expect((await review(phrase,['  RUN   as VCAP '])).steps[0].disabled).toBe(false);
});
t('a rotation patch confirmation gets the same label and placeholder',async()=>{
 const r=await review('patch in place',['x']);
 expect(r.labelText).toBe('To confirm, type "patch in place" below.');
 expect(r.chip).toBe('"patch in place"');
 expect(r.placeholder).toBe('patch in place');
 expect(r.steps[0].help).toContain('That doesn\'t match.');
});
t('the dialog uses the same label and placeholder',async()=>{
 const {w,d}=boot();
 try{
  w.__T.showConfirmDialog({title:'T',message:'m',requireText:'patch in place',confirmText:'Go',cancelText:'No'});
  await sleep(30);
  const ov=d.querySelector('#dialog-modal');
  expect(ov.querySelector('.dialog-modal-ack-label').textContent).toBe('To continue, type "patch in place" below.');
  expect(ov.querySelector('.dialog-modal-ack-label .confirm-word').textContent).toBe('"patch in place"');
  expect(ov.querySelector('.dialog-modal-ack-input').placeholder).toBe('patch in place');
 }finally{w.close();}
});

t('the phrase typed inside a pair of quotation marks still matches',async()=>{
 const r=await review('run as vcap',['"run as vcap"','“run as vcap”',"'run as vcap'",'‘run as vcap’','  "Run  As VCAP" ','confirm','"confirm"','"run as vcap','run as vcap"','“run as vcap"']);
 expect(r.steps.map(s=>s.disabled)).toEqual([false,false,false,false,false,true,true,true,true,true]);
});
