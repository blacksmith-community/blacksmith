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
afterEach(()=>{ while(envs.length){ const env=envs.pop(); expect(env.alerts).toEqual([]);
 // Neither the command nor the key reaches any console method.
 expect(env.logs.some(l=>String(l).includes(SECRET)||String(l).includes(KEY))).toBe(false); } });

const vm=(index,over={})=>({group:'node',index,id:'vm-'+index,cid:'cid-'+index,job_state:'running',targeted:true,...over});
const instance=(id,over={})=>({instance_id:id,instance_name:id+'-name',deployment:'dep-'+id,service:'valkey',plan:'small',blocked:null,vms:[vm(0),vm(1)],...over});
const preview=(over={})=>({preview_id:'prev-1',expires_at:'2030-01-01T00:00:00Z',command_digest:'d',command_length:SECRET.length,run_as:'vcap',shell:'/bin/bash',timeout_seconds:600,parallel:false,max_in_flight:0,parallel_available:true,worker_count:4,max_in_flight_limit:0,services:['valkey'],warnings:[],instances:[instance('val-a')],...over});

// routes.preview and routes.create answer as [status, body] or a function of
// the request that returns one. requests records each call with its headers.
async function setup(routes={}){
 const env=boot({noDialogs:true}),{w}=env;envs.push(env);
 const requests=[];
 const answer=(spec,o,j)=>{const r=typeof spec==='function'?spec(o,j):spec;if(r&&r.raw) return r.raw;return j(r[0],r[1]);};
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
async function openInstances(env,ids=['val-a'],text=SECRET){
 const {w,d}=env;
 await w.__T.requestOperation('command');
 w.__T.setBatchStep(1,false);
 type(w,d.getElementById('command-text'),text);
 w.__T.setBatchStep(2,false);
 await sleep(5);
 ids.forEach(id=>pick(d,id));
}
// Clicks Next from the instance step and waits for the review to settle.
async function review(env,ids,text){
 await openInstances(env,ids,text);
 env.d.getElementById('batch-flow-next').click();
 await sleep(20);
}
const ack=(env,value)=>type(env.w,env.d.getElementById('batch-flow-ack'),value);
const next=(env)=>env.d.getElementById('batch-flow-next');
const same=(a,b)=>JSON.stringify(a)===JSON.stringify(b);
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
 expect(same(previews(env)[0].body,{instance_ids:['val-a','val-b'],command:SECRET,run_as:'vcap',timeout_seconds:600,parallel:false,max_in_flight:0})).toBe(true);
 expect(w.__T.batchFlow.step).toBe(3);
 const host=d.getElementById('batch-flow-review');
 const pre=host.querySelector('pre');
 expect((pre)!==null).toBe(true);expect(pre.textContent===SECRET).toBe(true);
 const text=host.textContent.replace(SECRET,'');
 expect(String(text).includes('vcap')).toBe(true);expect(String(text).includes('/bin/bash')).toBe(true);expect(String(text).includes('600')).toBe(true);
 expect(host.querySelectorAll('table').length).toBe(2);
 expect(String(host.textContent).includes('dep-one')).toBe(true);expect(String(host.textContent).includes('dep-two')).toBe(true);
 expect(String(host.textContent).includes('failing')).toBe(true);expect(String(host.textContent).includes('no VM')).toBe(true);
 // The command text is in the pre and nowhere else on the page.
 const holders=[...d.querySelectorAll('*')].filter(e=>e.children.length===0&&hasSecret(e.textContent));
 expect(holders.every(e=>e.tagName==='PRE')).toBe(true);
 expect(env.logs.some(hasSecret)).toBe(false);
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
 expect(String(d.getElementById('batch-flow-review').textContent).includes('2 at a time')).toBe(true);
 }finally{w.close();}
});

t('a mixed_services warning shows a danger block that names the services',async()=>{
 const env=await setup({preview:[200,preview({warnings:['mixed_services'],services:['valkey','redis'],instances:[instance('val-a'),instance('val-b',{service:'redis'})]})]});const {w,d}=env;try{
 await review(env,['val-a','val-b']);
 const block=d.querySelector('#batch-flow-review .batch-review-warning');
 expect((block)!==null).toBe(true);expect(block.dataset.variant).toBe('danger');
 expect(String(block.textContent).includes('valkey')).toBe(true);expect(String(block.textContent).includes('redis')).toBe(true);
 }finally{w.close();}
});

t('no warning block shows without mixed_services',async()=>{
 const env=await setup();const {w,d}=env;try{
 await review(env);
 expect((d.querySelector('#batch-flow-review .batch-review-warning'))===null).toBe(true);
 }finally{w.close();}
});

t('a limit below the entered value sends the operator back to the options step',async()=>{
 const env=await setup({preview:[200,preview({max_in_flight_limit:3,parallel:true,max_in_flight:8})]});const {w,d}=env;try{
 await openInstances(env);
 d.getElementById('command-parallel').click();
 type(w,d.getElementById('command-max-in-flight'),'8');
 next(env).click();await sleep(20);
 expect(w.__T.batchFlow.step).toBe(1);
 expect((d.getElementById('batch-flow-review').querySelector('pre'))===null).toBe(true);
 expect(String(d.getElementById('batch-flow-help').textContent).includes('at most 3')).toBe(true);
 expect(creates(env).length).toBe(0);
 }finally{w.close();}
});

t('the confirmation needs the typed phrase and creates with the previewed values',async()=>{
 const env=await setup();const {w,d}=env;try{
 await review(env);
 const confirm=next(env);
 expect(confirm.textContent).toBe('Run on 2 nodes');
 expect(confirm.disabled).toBe(true);
 expect(String(d.getElementById('batch-flow-review').textContent).includes('run as vcap')).toBe(true);
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
 ['command','run_as','timeout_seconds','parallel','max_in_flight'].forEach(k=>expect(same(sent[k],asked[k])).toBe(true));
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
 expect(String(host.textContent).includes('Another operation holds this instance.')).toBe(true);
 ack(env,'run as vcap');
 expect(next(env).disabled).toBe(true);
 expect(String(d.getElementById('batch-flow-help').textContent.toLowerCase()).includes('deselect')).toBe(true);
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
  expect((w.__T.batchFlow.review)!==null).toBe(true);
  edit(w,d);
  await sleep(5);
  expect((w.__T.batchFlow.review)===null).toBe(true);
  expect(w.__T.batchFlow.step).not.toBe(3);
  expect((d.getElementById('batch-flow-review').querySelector('pre'))===null).toBe(true);
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
 expect((input)!==null).toBe(true);
 expect(next(env).disabled).toBe(true);
 expect(creates(env).length).toBe(0);
 type(w,input,KEY);
 expect(next(env).disabled).toBe(false);
 next(env).click();await sleep(20);
 expect(previews(env).length).toBe(2);
 expect(headerOf(previews(env)[0],'X-Blacksmith-Command-Key')==='').toBe(true);
 expect(headerOf(previews(env)[1],'X-Blacksmith-Command-Key')===KEY).toBe(true);
 // The review shows now, and the key is not in the page.
 expect((d.querySelector('#batch-flow-review input[type="password"]'))===null).toBe(true);
 expect((d.querySelector('#batch-flow-review pre'))!==null).toBe(true);
 ack(env,'run as vcap');next(env).click();await sleep(20);
 expect(creates(env).length).toBe(1);
 expect(headerOf(creates(env)[0],'X-Blacksmith-Command-Key')===KEY).toBe(true);
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
 expect(headerOf(previews(env)[2],'X-Blacksmith-Command-Key')==='').toBe(true);
 expect((d.querySelector('#batch-flow-review input[type="password"]'))!==null).toBe(true);
 }finally{w.close();}
});

t('the credential never reaches the draft or the page while it is held',async()=>{
 const env=await setup({preview:(o)=>((o.headers||{})['X-Blacksmith-Command-Key']?[200,preview()]:[401,{success:false,error:'A command key is required.',code:'command_credential_required'}])});const {w,d}=env;try{
 await review(env);
 type(w,d.querySelector('#batch-flow-review input[type="password"]'),KEY);
 next(env).click();await sleep(20);
 expect((w.__T.batchFlow.review)!==null).toBe(true);
 expect(hasKey(JSON.stringify(w.__T.batchFlow.drafts))).toBe(false);
 expect(hasKey(d.body.textContent)).toBe(false);
 expect(hasKey(JSON.stringify(Object.entries(w.localStorage)))).toBe(false);
 expect(hasKey(JSON.stringify(Object.entries(w.sessionStorage)))).toBe(false);
 // Dropping the review clears it, so the next preview asks again.
 w.__T.setBatchStep(2,false);
 next(env).click();await sleep(20);
 expect(headerOf(previews(env)[previews(env).length-1],'X-Blacksmith-Command-Key')==='').toBe(true);
 expect((d.querySelector('#batch-flow-review input[type="password"]'))!==null).toBe(true);
 expect(env.errors.some(hasKey)).toBe(false);
 }finally{w.close();}
});

t('a refused credential asks again',async()=>{
 const env=await setup({preview:[401,{success:false,error:'A command key is required.',code:'command_credential_required'}]});const {w,d}=env;try{
 await review(env);
 type(w,d.querySelector('#batch-flow-review input[type="password"]'),KEY);
 next(env).click();await sleep(20);
 const input=d.querySelector('#batch-flow-review input[type="password"]');
 expect((input)!==null).toBe(true);expect(input.value==='').toBe(true);
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
 expect((w.__T.batchFlow.review)!==null).toBe(true);
 expect(d.getElementById('batch-flow-ack').value).toBe('');
 expect(next(env).disabled).toBe(true);
 expect(String(d.getElementById('batch-flow-review').textContent).includes("fresh one")).toBe(true);
 ack(env,'run as vcap');next(env).click();await sleep(30);
 expect(creates(env).map(c=>c.body.preview_id)).toEqual(['prev-1','prev-2']);
 expect(creates(env)[1].body.client_request_id.length>0).toBe(true);
 }finally{w.close();}
});

t('a refused preview leaves the review step with the reason',async()=>{
 const env=await setup({preview:[400,{success:false,error:'The command is empty.',code:'invalid_request'}]});const {w,d}=env;try{
 await review(env);
 expect(w.__T.batchFlow.step).toBe(2);
 expect((w.__T.batchFlow.review)===null).toBe(true);
 expect(creates(env).length).toBe(0);
 expect(env.logs.some(hasSecret)).toBe(false);
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

// ---- Unknown create outcomes, retries, and the key ----

// seq answers each call with the next spec and repeats the last.
const seq=(...specs)=>{let n=0;return (o,j)=>{const spec=specs[Math.min(n,specs.length-1)];n++;return typeof spec==='function'?spec(o,j):spec;};};
const reject=()=>({raw:Promise.reject(new TypeError('Failed to fetch'))});
const unreadable=()=>({raw:Promise.resolve({ok:true,status:200,json:()=>Promise.reject(new Error('bad body')),text:()=>Promise.resolve('')})});
const HTML502=[502,'<html>Bad Gateway</html>'];
const needKey=(o)=>((o.headers||{})['X-Blacksmith-Command-Key']?[200,preview()]:[401,{success:false,error:'A command key is required.',code:'command_credential_required'}]);
const confirmRun=async(env)=>{ack(env,'run as vcap');next(env).click();await sleep(30);};
const quiet=(env)=>{
 const all=env.d.documentElement.outerHTML+env.logs.join('\n')+env.errors.join('\n')+env.calls.join('\n')+JSON.stringify(Object.entries(env.w.localStorage))+JSON.stringify(Object.entries(env.w.sessionStorage))+JSON.stringify(env.w.__T.batchFlow.drafts)+env.w.location.href;
 return !hasKey(all)&&![...env.d.querySelectorAll('input')].some(i=>hasKey(i.value));
};

for(const [name,spec] of [['a rejected fetch',reject],['a 502 from a proxy',HTML502],['a 500',[500,{success:false,error:'internal',code:'internal'}]],['an unreadable 2xx',unreadable]]){
 t(`${name} leaves the outcome unknown and Retry resends the same request`,async()=>{
  const env=await setup({create:seq(spec,[201,{id:'task-9'}])});const {w,d}=env;try{
  await review(env);await confirmRun(env);
  expect(w.__T.batchFlow.step).toBe(3);
  expect(w.__T.batchFlow.review!==null).toBe(true);
  const host=d.getElementById('batch-flow-review');
  expect(host.textContent.includes('may have started')).toBe(true);
  expect(host.textContent.includes('not started')).toBe(false);
  expect(host.textContent.includes('<html>')).toBe(false);
  expect(next(env).textContent).toBe('Retry');expect(next(env).disabled).toBe(false);
  expect(d.getElementById('batch-flow-back').disabled).toBe(true);
  expect([...d.querySelectorAll('[data-batch-step]')].every(b=>b.disabled)).toBe(true);
  expect(d.getElementById('command-text').disabled).toBe(true);
  expect([...d.querySelectorAll('#upgrade .upgrade-instances input')].every(i=>i.disabled)).toBe(true);
  expect(previews(env).length).toBe(1);
  next(env).click();await sleep(30);
  expect(creates(env).length).toBe(2);
  expect(same(creates(env)[0].body,creates(env)[1].body)).toBe(true);
  expect(previews(env).length).toBe(1);
  expect(d.getElementById('command-text').disabled).toBe(false);
  expect([...d.querySelectorAll('#upgrade .upgrade-instances input')].some(i=>i.disabled)).toBe(false);
  }finally{w.close();}
 });
}

t('a second unknown outcome keeps asking with the same request',async()=>{
 const env=await setup({create:seq(reject,HTML502,[201,{id:'task-9'}])});const {w}=env;try{
 await review(env);await confirmRun(env);
 next(env).click();await sleep(30);
 expect(w.__T.batchFlow.review!==null).toBe(true);expect(next(env).textContent).toBe('Retry');
 next(env).click();await sleep(30);
 expect(creates(env).length).toBe(3);
 expect(creates(env).every(c=>same(c.body,creates(env)[0].body))).toBe(true);
 expect(previews(env).length).toBe(1);
 }finally{w.close();}
});

t('a retry that finds the preview expired keeps the request ID on the fresh preview',async()=>{
 let n=0;
 const env=await setup({preview:()=>[200,preview({preview_id:'prev-'+(++n)})],create:seq(reject,[410,{success:false,error:'expired',code:'preview_expired'}],[201,{id:'task-9'}])});const {w}=env;try{
 await review(env);await confirmRun(env);
 next(env).click();await sleep(30);
 expect(previews(env).length).toBe(2);
 ack(env,'run as vcap');next(env).click();await sleep(30);
 const sent=creates(env);
 expect(sent.length).toBe(3);
 expect(sent.every(c=>c.body.client_request_id===sent[0].body.client_request_id)).toBe(true);
 expect(sent[2].body.preview_id).toBe('prev-2');
 }finally{w.close();}
});

t('a definite refusal lets the next review mint a new request ID',async()=>{
 const env=await setup({create:seq([409,{success:false,error:'The settings changed.',code:'preview_mismatch'}],[201,{id:'task-9'}])});const {w,d}=env;try{
 await review(env);await confirmRun(env);
 expect(w.__T.batchFlow.step).toBe(2);
 expect(d.getElementById('command-text').disabled).toBe(false);
 next(env).click();await sleep(30);await confirmRun(env);
 expect(creates(env).length).toBe(2);
 expect(creates(env)[0].body.client_request_id!==creates(env)[1].body.client_request_id).toBe(true);
 }finally{w.close();}
});

t('the preview bodies are equal across a 401 retry',async()=>{
 const env=await setup({preview:needKey});const {w,d}=env;try{
 await review(env);
 type(w,d.querySelector('#batch-flow-review input[type="password"]'),KEY);next(env).click();await sleep(30);
 expect(previews(env).length).toBe(2);
 expect(same(previews(env)[0].body,previews(env)[1].body)).toBe(true);
 }finally{w.close();}
});

t('the key stays through a Retry in the same review and leaves with the flow',async()=>{
 const env=await setup({preview:needKey,create:seq(reject,reject,[201,{id:'task-9'}])});const {w,d}=env;try{
 await review(env);
 const input=d.querySelector('#batch-flow-review input[type="password"]');
 type(w,input,KEY);next(env).click();await sleep(30);
 expect(input.value==='').toBe(true);
 await confirmRun(env);
 next(env).click();await sleep(30);
 expect(creates(env).length).toBe(2);
 expect(creates(env).every(c=>headerOf(c,'X-Blacksmith-Command-Key')===KEY)).toBe(true);
 expect(quiet(env)).toBe(true);
 // Leaving drops the key, and the retry state comes back without it.
 d.getElementById('batch-activity').click();await sleep(5);
 expect(w.__T.batchFlow.review===null).toBe(true);
 expect(quiet(env)).toBe(true);
 expect(d.getElementById('command-text').disabled).toBe(true);
 d.getElementById('batch-new').click();
 next(env).click();await sleep(30);
 expect(next(env).textContent).toBe('Retry');
 expect(d.getElementById('batch-flow-review').textContent.includes('may have started')).toBe(true);
 next(env).click();await sleep(30);
 expect(creates(env).length).toBe(3);
 expect(headerOf(creates(env)[2],'X-Blacksmith-Command-Key')==='').toBe(true);
 expect(same(creates(env)[2].body,creates(env)[0].body)).toBe(true);
 expect(quiet(env)).toBe(true);
 }finally{w.close();}
});

const exits={
 activity:(env)=>env.d.getElementById('batch-activity').click(),
 operation:(env)=>env.w.__T.requestOperation('upgrade'),
 step:(env)=>env.w.__T.setBatchStep(2,false),
 back:(env)=>env.d.getElementById('batch-flow-back').click(),
 tab:(env)=>env.w.blacksmithNav.switchTab('services')
};
for(const [name,leave] of Object.entries(exits)){
 t(`leaving by ${name} clears the key while the key prompt is open`,async()=>{
  const env=await setup({preview:needKey});const {w,d}=env;try{
  await review(env);
  const input=d.querySelector('#batch-flow-review input[type="password"]');
  type(w,input,KEY);
  await leave(env);await sleep(30);
  expect(input.value==='').toBe(true);
  expect(w.__T.batchFlow.review===null).toBe(true);
  expect(quiet(env)).toBe(true);
  expect(previews(env).length).toBe(1);
  }finally{w.close();}
 });
 t(`leaving by ${name} clears the key while the review is open`,async()=>{
  const env=await setup({preview:needKey});const {w,d}=env;try{
  await review(env);
  const input=d.querySelector('#batch-flow-review input[type="password"]');
  type(w,input,KEY);next(env).click();await sleep(30);
  expect(w.__T.batchFlow.review!==null).toBe(true);
  await leave(env);await sleep(30);
  expect(input.value==='').toBe(true);
  expect(w.__T.batchFlow.review===null).toBe(true);
  expect(d.querySelector('#batch-flow-review pre')===null).toBe(true);
  expect(quiet(env)).toBe(true);
  // The next review asks for the key again.
  if(name!=='operation'){
   w.__T.setBatchStep(2,false);await sleep(5);
   next(env).click();await sleep(30);
   expect(headerOf(previews(env)[previews(env).length-1],'X-Blacksmith-Command-Key')==='').toBe(true);
  }
  }finally{w.close();}
 });
}

t('a finished create clears the key and the unknown state',async()=>{
 const env=await setup({preview:needKey});const {w,d}=env;try{
 await review(env);
 type(w,d.querySelector('#batch-flow-review input[type="password"]'),KEY);next(env).click();await sleep(30);
 await confirmRun(env);
 expect(creates(env).length).toBe(1);
 expect(quiet(env)).toBe(true);
 expect(d.getElementById('command-text').disabled).toBe(false);
 }finally{w.close();}
});

for(const [code,message] of [['max_in_flight_too_high','Most nodes at once is above the limit of 3.'],['timeout_out_of_range','The timeout is out of range.'],['command_too_long','The command is too long.'],['invalid_run_user','Commands run as root or vcap.']]){
 t(`a ${code} refusal puts its issue on the options step`,async()=>{
  const env=await setup({preview:[400,{success:false,error:message,code}]});const {w,d}=env;try{
  await review(env);
  expect(w.__T.batchFlow.step).toBe(1);
  expect(d.getElementById('batch-flow-help').textContent.includes(message)).toBe(true);
  expect(d.querySelector('#batch-flow-review pre')===null).toBe(true);
  expect(next(env).disabled).toBe(true);
  expect(creates(env).length).toBe(0);
  // Changing an option clears the issue.
  type(w,d.getElementById('command-timeout'),'601');
  expect(w.__T.batchOptionIssue()).toBe('');
  expect(hasSecret(d.getElementById('batch-flow-help').textContent)).toBe(false);
  }finally{w.close();}
 });
}

t('the review table has the AZ and state columns',async()=>{
 const env=await setup({preview:[200,preview({instances:[instance('val-a',{vms:[vm(0,{az:'z1',state:'running',job_state:'failing'}),vm(1,{az:'z2',state:'unresponsive agent',targeted:false,reason:'no VM'})]})]})]});const {w,d}=env;try{
 await review(env);
 const table=d.querySelector('#batch-flow-review table');
 expect(same([...table.querySelectorAll('th')].map(h=>h.textContent),['Node','AZ','State','Job state','Targeted','Reason'])).toBe(true);
 const rows=[...table.querySelectorAll('tr')].slice(1).map(r=>[...r.children].map(c=>c.textContent));
 expect(rows[0].slice(1).join('|')).toBe('z1|running|failing|Yes|');
 expect(rows[1].slice(1).join('|')).toBe('z2|unresponsive agent|running|No|no VM');
 }finally{w.close();}
});

t('a selection with no node to run stops the confirmation',async()=>{
 const env=await setup({preview:[200,preview({instances:[instance('val-a',{vms:[vm(0,{targeted:false,reason:'no VM'})]})]})]});const {w,d}=env;try{
 await review(env);
 ack(env,'run as vcap');
 expect(next(env).disabled).toBe(true);
 expect(d.getElementById('batch-flow-help').textContent.includes('No node')).toBe(true);
 next(env).click();await sleep(10);
 expect(creates(env).length).toBe(0);
 }finally{w.close();}
});

t('a preview error with an HTML body never shows markup',async()=>{
 const env=await setup({preview:[502,'<html>Bad Gateway</html>']});const {w,d}=env;try{
 await review(env);
 expect(d.documentElement.outerHTML.includes('&lt;html&gt;')||d.body.textContent.includes('<html>')).toBe(false);
 expect(d.body.textContent.includes('HTTP 502')).toBe(true);
 }finally{w.close();}
});

// ---- Definite answers, a create 401, and the lock ----

const CONFLICT=[409,{success:false,error:'This client_request_id was already used for a different request, so no task was created.',code:'request_id_conflict'}];
const NEED_KEY=[401,{success:false,error:'A command key is required.',code:'command_credential_required'}];
const keyed=(o)=>Boolean((o.headers||{})['X-Blacksmith-Command-Key']);
const sameCreates=(env)=>creates(env).every(c=>same(c.body,creates(env)[0].body));
const unlocked=(d)=>!d.getElementById('command-text').disabled;

t('a 401 on the first create asks for the key and resends the same create without a new preview',async()=>{
 const env=await setup({create:(o)=>keyed(o)?[201,{id:'task-9'}]:NEED_KEY});const {w,d}=env;try{
 await review(env);await confirmRun(env);
 const prompt=d.getElementById('batch-flow-review');
 expect(prompt.querySelector('input[type="password"]')!==null).toBe(true);
 expect(prompt.textContent.includes('asks for a command key')).toBe(true);
 expect(prompt.textContent.includes('did not accept')).toBe(false);
 expect(d.getElementById('command-text').disabled).toBe(true);
 type(w,prompt.querySelector('input[type="password"]'),KEY);next(env).click();await sleep(30);
 expect(previews(env).length).toBe(1);
 expect(creates(env).length).toBe(2);
 expect(sameCreates(env)).toBe(true);
 expect(headerOf(creates(env)[0],'X-Blacksmith-Command-Key')==='').toBe(true);
 expect(headerOf(creates(env)[1],'X-Blacksmith-Command-Key')===KEY).toBe(true);
 expect(unlocked(d)).toBe(true);
 expect(quiet(env)).toBe(true);
 }finally{w.close();}
});

t('a key the broker refuses on create is asked for again with the refusal named',async()=>{
 const env=await setup({create:(o)=>(o.headers||{})['X-Blacksmith-Command-Key']==='good-'+KEY?[201,{id:'task-9'}]:NEED_KEY});const {w,d}=env;try{
 await review(env);await confirmRun(env);
 type(w,d.querySelector('#batch-flow-review input[type="password"]'),KEY);next(env).click();await sleep(30);
 const prompt=d.getElementById('batch-flow-review');
 expect(prompt.textContent.includes('did not accept')).toBe(true);
 expect(previews(env).length).toBe(1);
 type(w,prompt.querySelector('input[type="password"]'),'good-'+KEY);next(env).click();await sleep(30);
 expect(creates(env).length).toBe(3);
 expect(sameCreates(env)).toBe(true);
 }finally{w.close();}
});

// P1: unknown, leave for Activity, come back, Retry without the key gets a 401.
t('a Retry after leaving gets a 401, asks for the key, and resends the original create',async()=>{
 const env=await setup({create:seq(reject,(o)=>NEED_KEY,(o)=>NEED_KEY)});const {w,d}=env;try{
 let n=0;
 env.w.__routes.unshift([CREATE_URL,(u,o,j)=>{
  if((o.method||'GET')!=='POST') return undefined;
  const body=JSON.parse(o.body);env.requests.push({url:CREATE_URL,headers:o.headers||{},body});n++;
  if(n===1) return Promise.reject(new TypeError('Failed to fetch'));
  return keyed(o)?j(201,{id:'task-9'}):j(401,NEED_KEY[1]);
 }]);
 await review(env);await confirmRun(env);
 d.getElementById('batch-activity').click();await sleep(5);
 d.getElementById('batch-new').click();
 next(env).click();await sleep(30);
 expect(next(env).textContent).toBe('Retry');
 next(env).click();await sleep(30);
 const prompt=d.getElementById('batch-flow-review');
 expect(prompt.querySelector('input[type="password"]')!==null).toBe(true);
 expect(prompt.textContent.includes('did not accept')).toBe(false);
 expect(previews(env).length).toBe(1);
 expect(unlocked(d)).toBe(false);
 expect(d.getElementById('batch-flow-back').disabled).toBe(true);
 type(w,prompt.querySelector('input[type="password"]'),KEY);next(env).click();await sleep(30);
 expect(previews(env).length).toBe(1);
 expect(creates(env).length).toBe(3);
 expect(sameCreates(env)).toBe(true);
 expect(headerOf(creates(env)[2],'X-Blacksmith-Command-Key')===KEY).toBe(true);
 expect(unlocked(d)).toBe(true);
 }finally{w.close();}
});

// P2: a Retry answered 409 request_id_conflict means the run already exists.
t('a conflict on Retry says the run already started and opens Activity',async()=>{
 const env=await setup({create:seq(reject,CONFLICT)});const {w,d}=env;try{
 await review(env);await confirmRun(env);
 next(env).click();await sleep(30);
 const text=d.body.textContent;
 expect(text.includes('already started')).toBe(true);
 expect(text.includes('was not started')).toBe(false);
 expect(d.getElementById('upgrade').querySelector('.upgrade-main').hidden).toBe(true);
 expect(w.__T.batchFlow.review===null).toBe(true);
 expect(unlocked(d)).toBe(true);
 expect(previews(env).length).toBe(1);
 expect(creates(env).length).toBe(2);
 expect(sameCreates(env)).toBe(true);
 }finally{w.close();}
});

// P3: a 403 on Retry leaves the create pending.
t('a 403 on Retry keeps the create pending and offers Retry again',async()=>{
 const env=await setup({create:seq(reject,[403,{success:false,error:'Commands are switched off.',code:'commands_disabled'}],[201,{id:'task-9'}])});const {w,d}=env;try{
 await review(env);await confirmRun(env);
 next(env).click();await sleep(30);
 const host=d.getElementById('batch-flow-review');
 expect(next(env).textContent).toBe('Retry');
 expect(host.textContent.includes('refused the request for now')).toBe(true);
 expect(host.textContent.includes('not started')).toBe(false);
 expect(unlocked(d)).toBe(false);
 expect(w.__T.batchFlow.step).toBe(3);
 next(env).click();await sleep(30);
 expect(creates(env).length).toBe(3);
 expect(sameCreates(env)).toBe(true);
 expect(previews(env).length).toBe(1);
 expect(unlocked(d)).toBe(true);
 }finally{w.close();}
});

for(const [name,answer] of [['a 400',[400,{success:false,error:'The command is empty.',code:'invalid_request'}]],['a preview_mismatch',[409,{success:false,error:'The settings changed.',code:'preview_mismatch'}]],['an instance_busy',[409,{success:false,error:'Another operation holds it.',code:'instance_busy'}]]]){
 t(`${name} on Retry is definite and frees the review`,async()=>{
  const env=await setup({create:seq(reject,answer)});const {w,d}=env;try{
  await review(env);await confirmRun(env);
  next(env).click();await sleep(30);
  expect(w.__T.batchFlow.step).toBe(2);
  expect(w.__T.batchFlow.review===null).toBe(true);
  expect(unlocked(d)).toBe(true);
  }finally{w.close();}
 });
}

t('a 503 on Retry stays pending',async()=>{
 const env=await setup({create:seq(reject,[503,{success:false,error:'Tasks are still loading.',code:'tasks_loading'}])});const {w,d}=env;try{
 await review(env);await confirmRun(env);
 next(env).click();await sleep(30);
 expect(next(env).textContent).toBe('Retry');expect(unlocked(d)).toBe(false);
 }finally{w.close();}
});

t('the command text in the console fails the suite check',async()=>{
 const env=await setup();const {w}=env;try{
 w.console.error(SECRET);
 expect(env.logs.some(hasSecret)).toBe(true);
 env.logs.length=0;
 }finally{w.close();}
});

t('the instance list redrawn under the lock stays disabled',async()=>{
 const env=await setup({create:seq(reject)});const {w,d}=env;try{
 await review(env);await confirmRun(env);
 w.__T.refreshUpgradeInstanceList();
 const boxes=[...d.querySelectorAll('#upgrade .upgrade-instances input')];
 expect(boxes.length>0).toBe(true);
 expect(boxes.every(b=>b.disabled)).toBe(true);
 }finally{w.close();}
});

t('a pending create ignores a change to the controls',async()=>{
 const env=await setup({create:seq(reject)});const {w,d}=env;try{
 await review(env);await confirmRun(env);
 w.__T.commandState.text=SECRET+' more';
 d.getElementById('upgrade').dispatchEvent(new w.Event('change',{bubbles:true}));await sleep(10);
 expect(w.__T.batchFlow.step).toBe(3);
 expect(w.__T.batchFlow.review!==null).toBe(true);
 expect(next(env).textContent).toBe('Retry');
 }finally{w.close();}
});

t('the review signature comes from the frozen values, not the live controls',async()=>{
 let release;
 const env=await setup({preview:(o,j)=>({raw:new Promise(r=>{release=()=>r(j(200,preview()));})})});const {w,d}=env;try{
 await openInstances(env);
 next(env).click();await sleep(10);
 // The operator edits while the preview is in flight.
 type(w,d.getElementById('command-text'),SECRET+' --changed');
 release();await sleep(30);
 expect(w.__T.batchFlow.review===null).toBe(true);
 expect(w.__T.batchFlow.step).not.toBe(3);
 }finally{w.close();}
});

// ---- Key prompt labels, a bare 401, and the ID through a 410 ----

t('the key prompt from a create 401 carries the run label and danger styling',async()=>{
 const env=await setup({create:(o)=>keyed(o)?[201,{id:'task-9'}]:NEED_KEY});const {w,d}=env;try{
 await review(env);await confirmRun(env);
 expect(next(env).textContent.startsWith('Run on ')).toBe(true);
 expect(next(env).textContent.includes('Preview')).toBe(false);
 expect(next(env).dataset.variant).toBe('danger');
 }finally{w.close();}
});

t('the key prompt from a preview 401 keeps the preview label',async()=>{
 const env=await setup({preview:needKey});const {w,d}=env;try{
 await openInstances(env);
 next(env).click();await sleep(30);
 expect(next(env).textContent).toBe('Preview command run');
 expect(next(env).dataset.variant).toBe('primary');
 }finally{w.close();}
});

t('a bare 401 on a pending create keeps it pending with the same request',async()=>{
 const env=await setup({create:seq(reject,[401,{error:'Unauthorized'}],[201,{id:'task-9'}])});const {w,d}=env;try{
 await review(env);await confirmRun(env);
 next(env).click();await sleep(30);
 expect(next(env).textContent).toBe('Retry');
 expect(unlocked(d)).toBe(false);
 expect(w.__T.batchFlow.step).toBe(3);
 next(env).click();await sleep(30);
 expect(creates(env).length).toBe(3);
 expect(sameCreates(env)).toBe(true);
 expect(previews(env).length).toBe(1);
 }finally{w.close();}
});

t('a review resumed after leaving keeps its request ID through a 410',async()=>{
 let n=0;
 const env=await setup({preview:()=>[200,preview({preview_id:'prev-'+(++n)})],create:seq(reject,[410,{success:false,error:'expired',code:'preview_expired'}],[201,{id:'task-9'}])});const {w,d}=env;try{
 await review(env);await confirmRun(env);
 d.getElementById('batch-activity').click();await sleep(5);
 d.getElementById('batch-new').click();
 next(env).click();await sleep(30);
 next(env).click();await sleep(30);
 expect(previews(env).length).toBe(2);
 ack(env,'run as vcap');next(env).click();await sleep(30);
 const sent=creates(env);
 expect(sent.length).toBe(3);
 expect(sent.every(c=>c.body.client_request_id===sent[0].body.client_request_id)).toBe(true);
 expect(sent[2].body.preview_id).toBe('prev-2');
 }finally{w.close();}
});

t('the console capture sees command text nested in an object',async()=>{
 const env=await setup();const {w}=env;try{
 w.console.log('create',{command:SECRET});
 expect(env.logs.some(hasSecret)).toBe(true);
 env.logs.length=0;
 w.console.error({message:'create failed',body:{command:SECRET}});
 expect(env.logs.some(hasSecret)).toBe(true);
 env.logs.length=0;
 }finally{w.close();}
});

t('a command with hidden characters is reviewed as escapes but previewed and created as typed',async()=>{
 const raw=SECRET+'\u0007\u001b\u202e\u200f\u2066\u0085';
 const env=await setup();const {w,d}=env;try{
 await review(env,['val-a'],raw);
 const pre=d.querySelector('#batch-flow-review pre.command-review-text');
 expect(pre!==null).toBe(true);
 expect(/[\u0007\u001b\u202e\u200f\u2066\u0085]/.test(pre.textContent)).toBe(false);
 expect(pre.textContent.includes('\\u0007')&&pre.textContent.includes('\\u202E')&&pre.textContent.includes('\\u0085')).toBe(true);
 ack(env,'run as vcap');
 next(env).click();await sleep(20);
 expect(previews(env)[0].body.command===raw).toBe(true);
 expect(creates(env).length).toBe(1);
 expect(creates(env)[0].body.command===raw).toBe(true);
 }finally{w.close();}
});

// ---- Discard a pending create ----

const DISCARD_URL='/b/upgrade/commands/discard';
const okBody=(body)=>({raw:Promise.resolve({ok:true,status:200,json:()=>Promise.resolve(body),text:()=>Promise.resolve(JSON.stringify(body))})});

// Answers every create with an unknown outcome, so a review stays pending
// until Discard settles it. The discard answers the next spec of discards,
// each a [status, body], reject, or a function of the request body. The task
// list answers whatever list returns for the pending request ID.
function pendingRoutes(env,discards,list){
 const {w}=env;let n=0;env.listReads=0;env.discards=[];
 w.__routes.unshift([DISCARD_URL,(u,o,j)=>{
  const body=JSON.parse(o.body);
  env.discards.push({url:DISCARD_URL,method:o.method,headers:o.headers||{},raw:o.body,body});
  const spec=discards[Math.min(n,discards.length-1)];n++;
  const r=typeof spec==='function'?spec(body):spec;
  if(r&&r.raw) return r.raw;
  return j(r[0],r[1]);
 }]);
 w.__routes.unshift([CREATE_URL,(u,o,j)=>{
  if((o.method||'GET')!=='POST'){
   env.listReads++;
   return j(200,list?list(creates(env)[0]&&creates(env)[0].body.client_request_id):[]);
  }
  env.requests.push({url:CREATE_URL,headers:o.headers||{},body:JSON.parse(o.body)});
  return Promise.reject(new TypeError('Failed to fetch'));
 }]);
}
const discardBtn=(env)=>env.d.getElementById('command-discard');
const anywayBtn=(env)=>env.d.getElementById('command-discard-anyway');
const summaryOf=(id)=>({id:'task-77',kind:'command',status:'running',client_request_id:id,target:'Run command',total_count:2});
const pendingReview=async(env)=>{await review(env);await confirmRun(env);};
const DISCARDED=[200,{discarded:true}];
const gated=(body)=>{let release;const gate=new Promise((r)=>{release=r;});return {release,spec:()=>({raw:gate.then(()=>({ok:true,status:200,json:()=>Promise.resolve(body),text:()=>Promise.resolve(JSON.stringify(body))}))})};};

t('the command review headings render',async()=>{
 const env=await setup({preview:(o)=>{
  const key=(o.headers||{})['X-Blacksmith-Command-Key'];
  return key?[200,preview()]:[401,{success:false,error:'A command key is required.',code:'command_credential_required'}];
 }});const {w,d}=env;try{
 const heading=()=>{const h=d.querySelector('#batch-flow-review .batch-review-title');return h?h.textContent:'';};
 await review(env);
 expect(heading()==='Command key').toBe(true);
 type(w,d.querySelector('#batch-flow-review input[type="password"]'),KEY);
 next(env).click();await sleep(20);
 expect(heading()==='Run this command?').toBe(true);
 pendingRoutes(env,[DISCARDED]);
 await confirmRun(env);
 expect(heading()==='Outcome unknown').toBe(true);
 }finally{w.close();}
});

t('a review with no pending create shows no Discard',async()=>{
 const env=await setup();const {w}=env;try{
 await review(env);
 expect(discardBtn(env)===null).toBe(true);
 }finally{w.close();}
});

t('a pending create shows Discard beside Retry',async()=>{
 const env=await setup();const {w}=env;try{
 pendingRoutes(env,[DISCARDED]);
 await pendingReview(env);
 expect(discardBtn(env)!==null).toBe(true);
 expect(next(env).textContent).toBe('Retry');
 expect(discardBtn(env).disabled).toBe(false);
 }finally{w.close();}
});

t('Discard disables Retry and Discard while the broker answers',async()=>{
 const env=await setup();const {w}=env;try{
 const hold=gated({discarded:true});
 pendingRoutes(env,[hold.spec]);
 await pendingReview(env);
 discardBtn(env).click();await sleep(10);
 expect(next(env).disabled).toBe(true);
 expect(discardBtn(env).disabled).toBe(true);
 hold.release();await sleep(30);
 expect(w.__T.batchFlow.step).toBe(1);
 }finally{w.close();}
});

t('a discarded answer clears the create, keeps the settings, and says the broker will refuse a late request',async()=>{
 const env=await setup();const {w,d}=env;try{
 pendingRoutes(env,[DISCARDED]);
 await pendingReview(env);
 const firstId=creates(env)[0].body.client_request_id;
 discardBtn(env).click();await sleep(40);
 expect(w.__T.batchFlow.step).toBe(1);
 expect(w.__T.batchFlow.review===null).toBe(true);
 expect(unlocked(d)).toBe(true);
 expect(d.getElementById('command-text').value===SECRET).toBe(true);
 expect([...d.querySelectorAll('#upgrade .upgrade-instances input')].some(i=>i.disabled)).toBe(false);
 const note=d.getElementById('command-discard-note');
 expect(note!==null).toBe(true);
 expect(note.textContent.includes('discarded')).toBe(true);
 expect(note.textContent.includes('Blacksmith will refuse it if it arrives late')).toBe(true);
 expect(note.textContent.includes('appears later in Activity')).toBe(false);
 expect(d.getElementById('batch-flow-review').textContent.includes('Outcome unknown')).toBe(false);
 expect(d.getElementById('command-discard')===null).toBe(true);
 expect(next(env).textContent==='Retry').toBe(false);
 expect(hasSecret(d.getElementById('batch-flow-review').textContent)).toBe(false);
 // Discard reads no task list and sends no create.
 expect(env.listReads).toBe(0);
 expect(creates(env).length).toBe(1);
 // The next review sends a new ID, and never the discarded one.
 w.__T.setBatchStep(2,false);await sleep(5);
 next(env).click();await sleep(30);
 await confirmRun(env);
 expect(creates(env).length).toBe(2);
 expect(creates(env)[1].body.client_request_id!==firstId).toBe(true);
 expect(d.getElementById('command-discard-note')===null).toBe(true);
 expect(quiet(env)).toBe(true);
 }finally{w.close();}
});

t('the discard request carries only the request ID and the preview ID, without the key or the command',async()=>{
 const env=await setup({preview:needKey});const {w,d}=env;try{
 pendingRoutes(env,[DISCARDED]);
 await review(env);
 const input=d.querySelector('#batch-flow-review input[type="password"]');
 type(w,input,KEY);next(env).click();await sleep(30);
 await confirmRun(env);
 // The create did carry the key, as it must.
 expect(creates(env).length).toBe(1);
 expect(headerOf(creates(env)[0],'X-Blacksmith-Command-Key')).toBe(KEY);
 discardBtn(env).click();await sleep(40);
 expect(env.discards.length).toBe(1);
 const post=env.discards[0];
 expect(post.method).toBe('POST');
 expect(Object.keys(post.body).sort()).toEqual(['client_request_id','preview_id']);
 expect(post.body.client_request_id).toBe(creates(env)[0].body.client_request_id);
 expect(post.body.preview_id).toBe(creates(env)[0].body.preview_id);
 expect(Object.keys(post.headers).some(k=>k.toLowerCase()==='x-blacksmith-command-key')).toBe(false);
 expect(post.headers['Content-Type']).toBe('application/json');
 expect(hasSecret(post.raw)).toBe(false);
 expect(hasKey(post.raw)).toBe(false);
 expect(creates(env).length).toBe(1);
 expect(w.__T.batchFlow.step).toBe(1);
 expect(quiet(env)).toBe(true);
 expect(hasSecret(d.getElementById('batch-flow-review').textContent)).toBe(false);
 }finally{w.close();}
});

t('a discard that finds the run settles it like a conflict and opens it in Activity',async()=>{
 const env=await setup();const {w,d}=env;try{
 pendingRoutes(env,[(body)=>[200,{discarded:false,task_id:'task-77'}]],(id)=>[summaryOf(id)]);
 await pendingReview(env);
 discardBtn(env).click();await sleep(40);
 const text=d.body.textContent;
 expect(text.includes('already started')).toBe(true);
 expect(d.getElementById('command-discard-note')===null).toBe(true);
 expect(d.getElementById('upgrade').querySelector('.upgrade-main').hidden).toBe(true);
 expect(w.__T.batchFlow.review===null).toBe(true);
 expect(unlocked(d)).toBe(true);
 expect(creates(env).length).toBe(1);
 expect(env.discards.length).toBe(1);
 expect(env.calls.some((c)=>String(c).includes('/b/upgrade/tasks/task-77'))).toBe(true);
 }finally{w.close();}
});

t('a discard that names a task opens it although the task list does not show it yet',async()=>{
 const env=await setup();const {w,d}=env;try{
 pendingRoutes(env,[[200,{discarded:false,task_id:'task-77'}]],()=>[]);
 await pendingReview(env);
 discardBtn(env).click();await sleep(40);
 expect(d.body.textContent.includes('already started')).toBe(true);
 expect(env.calls.some((c)=>String(c).includes('/b/upgrade/tasks/task-77'))).toBe(true);
 }finally{w.close();}
});

const MALFORMED=[
 ['a 200 with an empty body',[200,{}]],
 ['a 200 whose discarded is not a boolean',[200,{discarded:'yes'}]],
 ['a 200 that says not discarded and names no task',[200,{discarded:false}]],
 ['a 200 that is not JSON',unreadable],
 ['a 202 with a discarded body',[202,{discarded:true}]],
 ['a 404 with a discarded body',[404,{discarded:true}]]
];
for(const [name,spec] of [['a rejected fetch',reject],['a 401',[401,{success:false,error:'sign in',code:'unauthorized'}]],['a 403',[403,{success:false,error:'no',code:'commands_disabled'}]],['a 503',[503,{success:false,error:'loading',code:'tasks_loading'}]],['a 500',[500,{success:false,error:'x',code:'internal'}]],...MALFORMED]){
 t(`Discard after ${name} offers Discard anyway and never uses a dialog`,async()=>{
  const env=await setup();const {w,d}=env;try{
  pendingRoutes(env,[spec]);
  await pendingReview(env);
  expect(anywayBtn(env).parentElement.hidden).toBe(true);
  discardBtn(env).click();await sleep(40);
  expect(w.__T.batchFlow.step).toBe(3);
  expect(w.__T.batchFlow.review!==null).toBe(true);
  expect(anywayBtn(env).parentElement.hidden).toBe(false);
  const text=anywayBtn(env).parentElement.textContent;
  expect(text.includes('could not confirm whether this request started a run')).toBe(true);
  expect(text.includes('Check Activity before you run the command again')).toBe(true);
  expect(anywayBtn(env).className.includes('danger')).toBe(true);
  expect(next(env).disabled).toBe(false);
  expect(unlocked(d)).toBe(false);
  expect(creates(env).length).toBe(1);
  }finally{w.close();}
 });
}

t('Discard anyway clears the create locally, posts nothing more, and the discarded ID is never sent again',async()=>{
 const env=await setup();const {w,d}=env;try{
 pendingRoutes(env,[reject]);
 await pendingReview(env);
 const firstId=creates(env)[0].body.client_request_id;
 discardBtn(env).click();await sleep(30);
 expect(env.discards.length).toBe(1);
 anywayBtn(env).click();await sleep(30);
 expect(env.discards.length).toBe(1);
 expect(env.listReads).toBe(0);
 expect(w.__T.batchFlow.step).toBe(1);
 expect(unlocked(d)).toBe(true);
 const note=d.getElementById('command-discard-note');
 expect(note!==null).toBe(true);
 expect(note.textContent.includes('A run that appears later in Activity would have come from it')).toBe(true);
 expect(creates(env).length).toBe(1);
 w.__T.setBatchStep(2,false);await sleep(5);
 next(env).click();await sleep(30);
 await confirmRun(env);
 expect(creates(env).length).toBe(2);
 expect(creates(env)[1].body.client_request_id!==firstId).toBe(true);
 }finally{w.close();}
});

t('Discard clears the held command key and the key never shows after',async()=>{
 const env=await setup({preview:needKey});const {w,d}=env;try{
 pendingRoutes(env,[DISCARDED]);
 await review(env);
 const input=d.querySelector('#batch-flow-review input[type="password"]');
 expect(input!==null).toBe(true);
 type(w,input,KEY);next(env).click();await sleep(30);
 await confirmRun(env);
 expect(discardBtn(env)!==null).toBe(true);
 discardBtn(env).click();await sleep(40);
 expect(w.__T.batchFlow.step).toBe(1);
 expect(quiet(env)).toBe(true);
 expect(hasSecret(d.getElementById('batch-flow-review').textContent)).toBe(false);
 }finally{w.close();}
});

t('leaving for Activity while Discard waits drops the answer',async()=>{
 const env=await setup();const {w,d}=env;try{
 const hold=gated({discarded:true});
 pendingRoutes(env,[hold.spec]);
 await pendingReview(env);
 discardBtn(env).click();await sleep(10);
 d.getElementById('batch-activity').click();await sleep(5);
 hold.release();await sleep(40);
 expect(w.__T.batchFlow.step).toBe(3);
 expect(d.getElementById('command-discard-note')===null).toBe(true);
 d.getElementById('batch-new').click();
 next(env).click();await sleep(30);
 expect(next(env).textContent).toBe('Retry');
 expect(next(env).disabled).toBe(false);
 }finally{w.close();}
});

t('a stale Discard answer does not leave the next review stuck',async()=>{
 const env=await setup();const {w,d}=env;try{
 const hold=gated({discarded:true});
 pendingRoutes(env,[hold.spec]);
 await pendingReview(env);
 discardBtn(env).click();await sleep(10);
 d.getElementById('batch-activity').click();await sleep(5);
 d.getElementById('batch-new').click();
 next(env).click();await sleep(30);
 expect(next(env).textContent).toBe('Retry');
 expect(next(env).disabled).toBe(false);
 expect(discardBtn(env).disabled).toBe(false);
 hold.release();await sleep(30);
 expect(w.__T.batchFlow.step).toBe(3);
 expect(next(env).disabled).toBe(false);
 }finally{w.close();}
});

t('Discard and Discard anyway wait while a Retry is in flight',async()=>{
 const env=await setup();const {w}=env;try{
 let release;const gate=new Promise((r)=>{release=r;});
 pendingRoutes(env,[reject]);
 await pendingReview(env);
 discardBtn(env).click();await sleep(30);
 expect(anywayBtn(env).parentElement.hidden).toBe(false);
 // Hold the next create open.
 w.__routes.unshift([CREATE_URL,(u,o,j)=>{
  if((o.method||'GET')!=='POST') return undefined;
  env.requests.push({url:CREATE_URL,headers:o.headers||{},body:JSON.parse(o.body)});
  return gate.then(()=>j(201,{id:'task-9'}));
 }]);
 next(env).click();await sleep(10);
 expect(discardBtn(env).disabled).toBe(true);
 expect(anywayBtn(env).disabled).toBe(true);
 release();await sleep(40);
 }finally{w.close();}
});

t('a discarded answer keeps the Activity output key',async()=>{
 const env=await setup();const {w,d}=env;try{
 pendingRoutes(env,[DISCARDED]);
 await pendingReview(env);
 const held=d.createElement('input');held.type='password';held.className='command-output-key';held.value=KEY;d.querySelector('#upgrade').appendChild(held);
 discardBtn(env).click();await sleep(40);
 expect(w.__T.batchFlow.step).toBe(1);
 expect(held.value===KEY).toBe(true);
 }finally{w.close();}
});

t('the Discard button uses the outline style that Back uses',async()=>{
 const env=await setup();const {w}=env;try{
 pendingRoutes(env,[DISCARDED]);
 await pendingReview(env);
 expect(discardBtn(env).classList.contains('outline')).toBe(true);
 }finally{w.close();}
});
