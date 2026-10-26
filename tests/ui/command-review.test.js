const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const SECRET='echo SECRET-COMMAND-TEXT-5520';
const KEY='key-SECRET-CREDENTIAL-8841';
const PREVIEW_URL='/b/upgrade/commands/preview';
const CREATE_URL='/b/upgrade/tasks';

// Every env a test boots, checked once the test ends so a dialog error that
// the UI code swallows still fails it.
const envs=[];
afterEach(()=>{ while(envs.length) expect(envs.pop().alerts).toEqual([]); });

const vm=(index,over={})=>({group:'node',index,id:'vm-'+index,cid:'cid-'+index,job_state:'running',targeted:true,...over});
const instance=(id,over={})=>({instance_id:id,instance_name:id+'-name',deployment:'dep-'+id,service:'valkey',plan:'small',blocked:null,vms:[vm(0),vm(1)],...over});
const preview=(over={})=>({preview_id:'prev-1',expires_at:'2030-01-01T00:00:00Z',command_digest:'d',command_length:SECRET.length,run_as:'vcap',shell:'/bin/bash',timeout_seconds:600,parallel:false,max_in_flight:0,parallel_available:true,worker_count:4,max_in_flight_limit:0,services:['valkey'],warnings:[],instances:[instance('val-a')],...over});

// routes.preview and routes.create answer as [status, body] or a function of
// the request that returns one. requests records each call with its headers.
async function setup(routes={}){
 const env=boot({noDialogs:true}),{w}=env;envs.push(env);
 const requests=[];
 const answer=(spec,o,j)=>{const [status,body]=typeof spec==='function'?spec(o):spec;return j(status,body);};
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],
  [PREVIEW_URL,(u,o,j)=>{
   const body=JSON.parse(o.body);
   if(!body.instance_ids.length) return j(400,{success:false,error:'Select at least one instance to preview.',code:'no_selections'});
   requests.push({url:PREVIEW_URL,headers:o.headers||{},body});
   return answer(routes.preview||[200,preview()],o,j);
  }],
  [CREATE_URL,(u,o,j)=>{
   if((o.method||'GET')!=='POST') return j(200,[]);
   const body=JSON.parse(o.body);
   requests.push({url:CREATE_URL,headers:o.headers||{},body});
   return answer(routes.create||[201,{id:'task-1'}],o,j);
  }]);
 w.plansData={services:[{id:'valkey',name:'valkey'}]};
 await sleep(30);
 w.serviceInstances={
  'val-a':{service_id:'valkey',plan_id:'small',plan:{id:'small',name:'small'}},
  'val-b':{service_id:'valkey',plan_id:'small',plan:{id:'small',name:'small'}}
 };
 await w.__T.initUpgradeTab();await sleep(10);
 return {...env,requests};
}
const type=(w,el,value)=>{el.value=value;el.dispatchEvent(new w.Event('input',{bubbles:true}));};
const pick=(d,id)=>d.querySelector(`#upgrade .upgrade-instance-item[data-instance-id="${id}"] input[type="checkbox"]`).click();
async function openInstances(env,ids=['val-a']){
 const {w,d}=env;
 await w.__T.requestOperation('command');
 w.__T.setBatchStep(1,false);
 type(w,d.getElementById('command-text'),SECRET);
 w.__T.setBatchStep(2,false);
 await sleep(5);
 ids.forEach(id=>pick(d,id));
}
// Clicks Next from the instance step and waits for the review to settle.
async function review(env,ids){
 await openInstances(env,ids);
 env.d.getElementById('batch-flow-next').click();
 await sleep(20);
}
const ack=(env,value)=>type(env.w,env.d.getElementById('batch-flow-ack'),value);
const next=(env)=>env.d.getElementById('batch-flow-next');
const hasSecret=(s)=>String(s).includes(SECRET);
const hasKey=(s)=>String(s).includes(KEY);
const previews=(env)=>env.requests.filter(r=>r.url===PREVIEW_URL);
const creates=(env)=>env.requests.filter(r=>r.url===CREATE_URL);
const headerOf=(request,name)=>request.headers[name]||'';

t('commandConfirmWord names the user',async()=>{
 const env=await setup();const {w}=env;try{
 expect(w.__T.commandConfirmWord('vcap')).toBe('run as vcap');
 expect(w.__T.commandConfirmWord('root')).toBe('run as root');
 }finally{w.close();}
});

t('the preview posts the entered values and the review shows them',async()=>{
 const env=await setup({preview:[200,preview({instances:[
  instance('val-a',{deployment:'dep-one',vms:[vm(0,{job_state:'running'}),vm(1,{job_state:'failing',targeted:false,reason:'no VM'})]}),
  instance('val-b',{deployment:'dep-two',vms:[vm(0)]})]})]});
 const {w,d}=env;try{
 await review(env,['val-a','val-b']);
 expect(previews(env).length).toBe(1);
 expect(previews(env)[0].body).toEqual({instance_ids:['val-a','val-b'],command:SECRET,run_as:'vcap',timeout_seconds:600,parallel:false,max_in_flight:0});
 expect(w.__T.batchFlow.step).toBe(3);
 const host=d.getElementById('batch-flow-review');
 const pre=host.querySelector('pre');
 expect(pre).not.toBeNull();expect(pre.textContent).toBe(SECRET);
 const text=host.textContent.replace(SECRET,'');
 expect(text).toContain('vcap');expect(text).toContain('/bin/bash');expect(text).toContain('600');
 expect(host.querySelectorAll('table').length).toBe(2);
 expect(host.textContent).toContain('dep-one');expect(host.textContent).toContain('dep-two');
 expect(host.textContent).toContain('failing');expect(host.textContent).toContain('no VM');
 // The command text is in the pre and nowhere else on the page.
 const holders=[...d.querySelectorAll('*')].filter(e=>e.children.length===0&&hasSecret(e.textContent));
 expect(holders.every(e=>e.tagName==='PRE')).toBe(true);
 expect(env.errors.some(hasSecret)).toBe(false);
 expect(hasSecret(JSON.stringify(w.__T.batchFlow.drafts))).toBe(false);
 }finally{w.close();}
});

t('the review shows the parallel settings',async()=>{
 const env=await setup({preview:[200,preview({parallel:true,max_in_flight:2,run_as:'vcap'})]});const {w,d}=env;try{
 await openInstances(env);
 d.getElementById('command-parallel').click();
 type(w,d.getElementById('command-max-in-flight'),'2');
 next(env).click();await sleep(20);
 expect(previews(env)[0].body.parallel).toBe(true);expect(previews(env)[0].body.max_in_flight).toBe(2);
 expect(d.getElementById('batch-flow-review').textContent).toContain('2 at a time');
 }finally{w.close();}
});

t('a mixed_services warning shows a danger block that names the services',async()=>{
 const env=await setup({preview:[200,preview({warnings:['mixed_services'],services:['valkey','redis'],instances:[instance('val-a'),instance('val-b',{service:'redis'})]})]});const {w,d}=env;try{
 await review(env,['val-a','val-b']);
 const block=d.querySelector('#batch-flow-review .batch-review-warning');
 expect(block).not.toBeNull();expect(block.dataset.variant).toBe('danger');
 expect(block.textContent).toContain('valkey');expect(block.textContent).toContain('redis');
 }finally{w.close();}
});

t('no warning block shows without mixed_services',async()=>{
 const env=await setup();const {w,d}=env;try{
 await review(env);
 expect(d.querySelector('#batch-flow-review .batch-review-warning')).toBeNull();
 }finally{w.close();}
});

t('a limit below the entered value sends the operator back to the options step',async()=>{
 const env=await setup({preview:[200,preview({max_in_flight_limit:3,parallel:true,max_in_flight:8})]});const {w,d}=env;try{
 await openInstances(env);
 d.getElementById('command-parallel').click();
 type(w,d.getElementById('command-max-in-flight'),'8');
 next(env).click();await sleep(20);
 expect(w.__T.batchFlow.step).toBe(1);
 expect(d.getElementById('batch-flow-review').querySelector('pre')).toBeNull();
 expect(d.getElementById('batch-flow-help').textContent).toContain('at most 3');
 expect(creates(env).length).toBe(0);
 }finally{w.close();}
});

t('the confirmation needs the typed phrase and creates with the previewed values',async()=>{
 const env=await setup();const {w,d}=env;try{
 await review(env);
 const confirm=next(env);
 expect(confirm.textContent).toBe('Run on 2 nodes');
 expect(confirm.disabled).toBe(true);
 expect(d.getElementById('batch-flow-review').textContent).toContain('run as vcap');
 ack(env,'run as root');expect(confirm.disabled).toBe(true);
 ack(env,'  Run   as VCAP ');expect(confirm.disabled).toBe(false);
 type(w,d.getElementById('rotate-name'),'');
 confirm.click();await sleep(20);
 expect(creates(env).length).toBe(1);
 const sent=creates(env)[0].body;
 const asked=previews(env)[0].body;
 expect(sent.kind).toBe('command');
 expect(sent.preview_id).toBe('prev-1');
 expect(typeof sent.client_request_id).toBe('string');expect(sent.client_request_id.length>0).toBe(true);
 ['command','run_as','timeout_seconds','parallel','max_in_flight'].forEach(k=>expect(sent[k]).toEqual(asked[k]));
 expect(Object.keys(sent).sort()).toEqual(['client_request_id','command','kind','max_in_flight','name','parallel','preview_id','reason','run_as','timeout_seconds']);
 expect(env.calls.some(c=>c.includes('/rotation/preview'))).toBe(false);
 }finally{w.close();}
});

t('a root run asks for run as root',async()=>{
 const env=await setup({preview:[200,preview({run_as:'root'})]});const {w,d}=env;try{
 await openInstances(env);
 d.getElementById('command-user-root').click();
 next(env).click();await sleep(20);
 expect(previews(env)[0].body.run_as).toBe('root');
 ack(env,'run as vcap');expect(next(env).disabled).toBe(true);
 ack(env,'run as root');expect(next(env).disabled).toBe(false);
 }finally{w.close();}
});

t('a blocked instance shows its reason and stops the confirmation',async()=>{
 const env=await setup({preview:[200,preview({instances:[instance('val-a'),instance('val-b',{blocked:{code:'instance_busy',message:'Another operation holds this instance.'},vms:[]})]})]});const {w,d}=env;try{
 await review(env,['val-a','val-b']);
 const host=d.getElementById('batch-flow-review');
 expect(host.textContent).toContain('Another operation holds this instance.');
 ack(env,'run as vcap');
 expect(next(env).disabled).toBe(true);
 expect(d.getElementById('batch-flow-help').textContent.toLowerCase()).toContain('deselect');
 next(env).click();await sleep(10);
 expect(creates(env).length).toBe(0);
 }finally{w.close();}
});

const edits={
 text:(w,d)=>type(w,d.getElementById('command-text'),SECRET+' --more'),
 user:(w,d)=>d.getElementById('command-user-root').click(),
 timeout:(w,d)=>type(w,d.getElementById('command-timeout'),'900'),
 parallel:(w,d)=>d.getElementById('command-parallel').click(),
 selection:(w,d)=>pick(d,'val-b')
};
for(const [name,edit] of Object.entries(edits)){
 t(`editing the ${name} after a preview drops the review and needs a fresh one`,async()=>{
  const env=await setup();const {w,d}=env;try{
  await review(env);
  expect(w.__T.batchFlow.review).not.toBeNull();
  edit(w,d);
  await sleep(5);
  expect(w.__T.batchFlow.review).toBeNull();
  expect(w.__T.batchFlow.step).not.toBe(3);
  expect(d.getElementById('batch-flow-review').querySelector('pre')).toBeNull();
  w.__T.setBatchStep(2,false);
  if(name==='parallel') type(w,d.getElementById('command-max-in-flight'),'2');
  next(env).click();await sleep(20);
  expect(previews(env).length).toBe(2);
  expect(creates(env).length).toBe(0);
  }finally{w.close();}
 });
}

t('a credential answer shows a password input and the key rides on the preview and create',async()=>{
 const env=await setup({preview:(o)=>{
  const key=(o.headers||{})['X-Blacksmith-Command-Key'];
  return key?[200,preview()]:[401,{success:false,error:'A command key is required.',code:'command_credential_required'}];
 }});const {w,d}=env;try{
 await review(env);
 const input=d.querySelector('#batch-flow-review input[type="password"]');
 expect(input).not.toBeNull();
 expect(next(env).disabled).toBe(true);
 expect(creates(env).length).toBe(0);
 type(w,input,KEY);
 expect(next(env).disabled).toBe(false);
 next(env).click();await sleep(20);
 expect(previews(env).length).toBe(2);
 expect(headerOf(previews(env)[0],'X-Blacksmith-Command-Key')).toBe('');
 expect(headerOf(previews(env)[1],'X-Blacksmith-Command-Key')).toBe(KEY);
 // The review shows now, and the key is not in the page.
 expect(d.querySelector('#batch-flow-review input[type="password"]')).toBeNull();
 expect(d.querySelector('#batch-flow-review pre')).not.toBeNull();
 ack(env,'run as vcap');next(env).click();await sleep(20);
 expect(creates(env).length).toBe(1);
 expect(headerOf(creates(env)[0],'X-Blacksmith-Command-Key')).toBe(KEY);
 expect(creates(env)[0].body.preview_id).toBe('prev-1');
 // The key is held nowhere once the create ends.
 expect(hasKey(d.documentElement.outerHTML)).toBe(false);
 expect([...d.querySelectorAll('input')].some(i=>hasKey(i.value))).toBe(false);
 expect(hasKey(JSON.stringify(w.__T.batchFlow.drafts))).toBe(false);
 expect(hasKey(JSON.stringify(Object.entries(w.localStorage)))).toBe(false);
 expect(hasKey(JSON.stringify(Object.entries(w.sessionStorage)))).toBe(false);
 expect(hasKey(w.location.href)).toBe(false);
 expect(env.errors.some(hasKey)).toBe(false);
 expect(env.calls.some(hasKey)).toBe(false);
 // A new run asks for the key again.
 w.__T.setBatchStep(2,false);
 pick(d,'val-a');
 await sleep(5);
 next(env).click();await sleep(20);
 expect(headerOf(previews(env)[2],'X-Blacksmith-Command-Key')).toBe('');
 expect(d.querySelector('#batch-flow-review input[type="password"]')).not.toBeNull();
 }finally{w.close();}
});

t('the credential never reaches the draft or the page while it is held',async()=>{
 const env=await setup({preview:(o)=>((o.headers||{})['X-Blacksmith-Command-Key']?[200,preview()]:[401,{success:false,error:'A command key is required.',code:'command_credential_required'}])});const {w,d}=env;try{
 await review(env);
 type(w,d.querySelector('#batch-flow-review input[type="password"]'),KEY);
 next(env).click();await sleep(20);
 expect(w.__T.batchFlow.review).not.toBeNull();
 expect(hasKey(JSON.stringify(w.__T.batchFlow.drafts))).toBe(false);
 expect(hasKey(d.body.textContent)).toBe(false);
 expect(hasKey(JSON.stringify(Object.entries(w.localStorage)))).toBe(false);
 expect(hasKey(JSON.stringify(Object.entries(w.sessionStorage)))).toBe(false);
 // Dropping the review clears it, so the next preview asks again.
 w.__T.setBatchStep(2,false);
 next(env).click();await sleep(20);
 expect(headerOf(previews(env)[previews(env).length-1],'X-Blacksmith-Command-Key')).toBe('');
 expect(d.querySelector('#batch-flow-review input[type="password"]')).not.toBeNull();
 expect(env.errors.some(hasKey)).toBe(false);
 }finally{w.close();}
});

t('a refused credential asks again',async()=>{
 const env=await setup({preview:[401,{success:false,error:'A command key is required.',code:'command_credential_required'}]});const {w,d}=env;try{
 await review(env);
 type(w,d.querySelector('#batch-flow-review input[type="password"]'),KEY);
 next(env).click();await sleep(20);
 const input=d.querySelector('#batch-flow-review input[type="password"]');
 expect(input).not.toBeNull();expect(input.value).toBe('');
 expect(next(env).disabled).toBe(true);
 expect(creates(env).length).toBe(0);
 }finally{w.close();}
});

t('an expired preview opens a fresh review and a fresh confirmation',async()=>{
 let n=0;
 const env=await setup({
  preview:()=>[200,preview({preview_id:'prev-'+(++n)})],
  create:(o)=>JSON.parse(o.body).preview_id==='prev-1'?[410,{success:false,error:'That preview expired.',code:'preview_expired'}]:[201,{id:'task-2'}]
 });const {w,d}=env;try{
 await review(env);
 ack(env,'run as vcap');next(env).click();await sleep(30);
 expect(previews(env).length).toBe(2);
 expect(w.__T.batchFlow.review).not.toBeNull();
 expect(d.getElementById('batch-flow-ack').value).toBe('');
 expect(next(env).disabled).toBe(true);
 expect(d.getElementById('batch-flow-review').textContent).toContain("fresh one");
 ack(env,'run as vcap');next(env).click();await sleep(30);
 expect(creates(env).map(c=>c.body.preview_id)).toEqual(['prev-1','prev-2']);
 expect(creates(env)[1].body.client_request_id.length>0).toBe(true);
 }finally{w.close();}
});

t('a refused preview leaves the review step with the reason',async()=>{
 const env=await setup({preview:[400,{success:false,error:'The command is empty.',code:'invalid_request'}]});const {w,d}=env;try{
 await review(env);
 expect(w.__T.batchFlow.step).toBe(2);
 expect(w.__T.batchFlow.review).toBeNull();
 expect(creates(env).length).toBe(0);
 expect(env.errors.some(hasSecret)).toBe(false);
 }finally{w.close();}
});

t('a command run never touches a certificate endpoint',async()=>{
 const env=await setup();const {w}=env;try{
 await openInstances(env);
 const before=env.calls.length;
 next(env).click();await sleep(20);
 ack(env,'run as vcap');next(env).click();await sleep(20);
 const sent=env.calls.slice(before);
 expect(sent.length>0).toBe(true);
 expect(sent.some(c=>c.includes('/b/certificates'))).toBe(false);
 expect(sent.some(c=>c.includes('/b/upgrade/preview'))).toBe(false);
 }finally{w.close();}
});
