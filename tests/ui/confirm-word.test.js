const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;
const K='/secret/dir/valkey_crt';
const pvInst=(id)=>({instance_id:id,instance_name:id+'-name',service:'valkey',plan:'small',vms:3,mode:'redeploy',gate:'g',manifest_digest:'d',selected:[{name:K,days_left:10}],additional:[],consumers:[],blocked:null,impact:'x'});

async function rotationWord(mode,count){
 const {w}=boot();
 try{
  const instances=Array.from({length:count},(_,i)=>pvInst('i'+i));
  w.__routes.push(['/b/certificates/rotation/preview',(u,o,j)=>j(200,{preview_id:'pv',instances,warnings:[]})]);
  let seen=null;
  await w.__T.previewAndCreate({mode,selections:instances.map(i=>({instance_id:i.instance_id,variables:[K]})),confirmReview:async(options)=>{seen=options;return false;}});
  return seen.requireText;
 }finally{w.close();}
}

t('a patch asks for patch in place at any size',async()=>{
 expect(await rotationWord('patch',1)).toBe('patch in place');
});
t('a redeploy asks for redeploy only above ten instances',async()=>{
 expect(await rotationWord('redeploy',11)).toBe('redeploy');
 expect(await rotationWord('redeploy',10)).toBeUndefined();
});
t('a converge above ten instances asks for converge',async()=>{
 expect(await rotationWord('converge',11)).toBe('converge');
 expect(await rotationWord('converge',10)).toBeUndefined();
});

async function upgradeReview(count,diverged){
 const env=boot(),{w,d}=env;
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[{operating_system:'ubuntu',version:'1'}])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])],
  ['/b/upgrade/preview',(u,o,j)=>j(200,{id:'up-1',diverged:diverged?[{instance_id:'i0',instance_name:'i0-name',changes:[]}]:[]})]);
 await sleep(30);
 await w.__T.initUpgradeTab();
 const sel=d.getElementById('upgrade-stemcell-filter');sel.value='ubuntu/1';
 for(let i=0;i<count;i++) w.__T.selectedUpgradeInstances.add('i'+i);
 const F=w.__T.batchFlow;F.step=3;
 const run=w.__T.startUpgrade({flowReview:true});
 await sleep(80);
 return {w,d,run};
}
t('an upgrade above ten instances asks for upgrade',async()=>{
 const {w,d}=await upgradeReview(11,false);
 try{
  const label=d.querySelector('.batch-ack-label');
  expect(label.textContent).toBe('Type upgrade to confirm');
  expect(label.querySelector('strong').textContent).toBe('upgrade');
 }finally{w.close();}
});
t('an upgrade with diverged instances asks for upgrade at any size',async()=>{
 const {w,d}=await upgradeReview(1,true);
 try{ expect(d.querySelector('.batch-ack-label').textContent).toBe('Type upgrade to confirm'); }finally{w.close();}
});
t('a small clean upgrade asks for nothing',async()=>{
 const {w,d}=await upgradeReview(2,false);
 try{ expect(d.getElementById('batch-flow-ack')).toBeNull(); }finally{w.close();}
});

async function typedReview(requireText,typed){
 const {w,d}=boot();
 try{
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
  await sleep(30);await w.__T.initUpgradeTab();
  const F=w.__T.batchFlow;F.step=3;
  w.__T.showBatchReview({title:'Review',requireText,confirmText:'Go'},F.revision);
  const ack=d.getElementById('batch-flow-ack');
  const label=d.querySelector('.batch-ack-label').textContent;
  ack.value=typed;ack.dispatchEvent(new w.Event('input',{bubbles:true}));
  return {label,disabled:d.getElementById('batch-flow-next').disabled,help:d.getElementById('batch-flow-help').textContent};
 }finally{w.close();}
}
t('the batch review shows the lowercase word and accepts forgiving input',async()=>{
 const a=await typedReview('patch in place','Patch In Place');
 expect(a.label).toBe('Type patch in place to confirm');
 expect(a.disabled).toBe(false);
 expect((await typedReview('patch in place','  patch   in place ')).disabled).toBe(false);
 const wrong=await typedReview('patch in place','rotate');
 expect(wrong.disabled).toBe(true);
 expect(wrong.help).toBe('Type patch in place in the confirmation field. The typed word keeps a stray click or keypress from starting this operation.');
});

t('the modal accepts the same forgiving input and names the word',async()=>{
 const {w,d}=boot();
 try{
  const p=w.__T.showConfirmDialog({title:'T',message:'m',requireText:'patch in place',confirmText:'Go',cancelText:'No'});
  await sleep(30);
  const ov=d.querySelector('#dialog-modal');
  expect(ov.querySelector('.dialog-modal-ack-label').textContent).toBe('Type patch in place to continue');
  const input=ov.querySelector('.dialog-modal-ack-input');
  input.value='rotate';ov.querySelector('[data-dialog-role="confirm"]').click();await sleep(30);
  expect(ov.textContent).toContain('Type "patch in place" in the field to continue.');
  input.value=' Patch  In Place ';ov.querySelector('[data-dialog-role="confirm"]').click();
  const answer=await p;
  expect(answer===true||answer.confirmed).toBe(true);
 }finally{w.close();}
});
