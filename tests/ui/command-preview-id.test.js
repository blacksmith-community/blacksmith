const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {expectNoAlerts}=require('./certificates/no-alerts.js');
const t=JSDOM?test:test.skip;

const SECRET='echo SECRET-COMMAND-TEXT-5521';
const KEY='key-SECRET-CREDENTIAL-8841';
const PREVIEW_URL='/b/upgrade/commands/preview';
const CREATE_URL='/b/upgrade/tasks';
const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); expectNoAlerts(env); expect(env.errors).toEqual([]);
 expect(env.logs.some(l=>String(l).includes(SECRET))).toBe(false); } });
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
 env.errors.length=0; // boot-time loads of unrouted endpoints
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
const seq=(...specs)=>{let n=0;return (o,j)=>{const spec=specs[Math.min(n,specs.length-1)];n++;return typeof spec==='function'?spec(o,j):spec;};};
const reject=()=>({raw:Promise.reject(new TypeError('Failed to fetch'))});
const unreadable=()=>({raw:Promise.resolve({ok:true,status:200,json:()=>Promise.reject(new Error('bad body')),text:()=>Promise.resolve('')})});
const HTML502=[502,'<html>Bad Gateway</html>'];
const needKey=(o)=>((o.headers||{})['X-Blacksmith-Command-Key']?[200,preview()]:[401,{success:false,error:'A command key is required.',code:'command_credential_required'}]);
const confirmRun=async(env)=>{ack(env,'run as vcap');next(env).click();await sleep(30);};

const DISCARD_URL='/b/upgrade/commands/discard';
const EXPIRED=[410,{success:false,error:'expired',code:'preview_expired'}];
const BOUND_TEXT='belongs to another request';
const UNKNOWN_TEXT='could not confirm whether this request started a run';

// Every create is an unknown outcome so the review stays pending. The discard
// answers with the given spec.
function pendingRoutes(env,discard){
 const {w}=env;env.discards=[];
 w.__routes.unshift([DISCARD_URL,(u,o,j)=>{
  env.discards.push(JSON.parse(o.body));
  const r=typeof discard==='function'?discard():discard;
  return r.raw?r.raw:j(r[0],r[1]);
 }]);
 w.__routes.unshift([CREATE_URL,(u,o,j)=>{
  if((o.method||'GET')!=='POST') return j(200,[]);
  env.requests.push({url:CREATE_URL,headers:o.headers||{},body:JSON.parse(o.body)});
  return Promise.reject(new TypeError('Failed to fetch'));
 }]);
}
const discardBtn=(env)=>env.d.getElementById('command-discard');
const anywayBtn=(env)=>env.d.getElementById('command-discard-anyway');
const unconfirmedText=(env)=>{const el=env.d.querySelector('.command-discard-unconfirmed');return el?el.textContent:'';};

t('the preview, the create, and the discard send one request ID',async()=>{
 const env=await setup();const {w}=env;try{
 pendingRoutes(env,[500,{success:false,error:'internal',code:'internal'}]);
 await review(env);await confirmRun(env);
 discardBtn(env).click();await sleep(40);
 const id=previews(env)[0].body.client_request_id;
 expect(typeof id).toBe('string');
 expect(id.length>0).toBe(true);
 expect(creates(env)[0].body.client_request_id).toBe(id);
 expect(env.discards[0].client_request_id).toBe(id);
 }finally{w.close();}
});

t('a Retry resends the same ID on the same preview',async()=>{
 const env=await setup({create:seq(reject,[201,{id:'task-9'}])});const {w}=env;try{
 await review(env);await confirmRun(env);
 next(env).click();await sleep(30);
 const id=previews(env)[0].body.client_request_id;
 expect(creates(env).length).toBe(2);
 expect(creates(env).every(c=>c.body.client_request_id===id)).toBe(true);
 expect(previews(env).length).toBe(1);
 }finally{w.close();}
});

// The first create answers 409 preview_mismatch, which ends that review and
// lets the operator start a second one through Next.
t('a new review sends a new ID on its preview',async()=>{
 const env=await setup({create:seq([409,{success:false,error:'The settings changed.',code:'preview_mismatch'}],[201,{id:'task-9'}])});const {w}=env;try{
 await review(env);await confirmRun(env);
 next(env).click();await sleep(30);await confirmRun(env);
 expect(previews(env).length).toBe(2);
 const first=previews(env)[0].body.client_request_id;
 const second=previews(env)[1].body.client_request_id;
 expect(typeof second).toBe('string');
 expect(second!==first).toBe(true);
 expect(creates(env)[1].body.client_request_id).toBe(second);
 }finally{w.close();}
});

t('an expired-preview refresh sends a preview whose ID matches the create after it',async()=>{
 let n=0;
 const env=await setup({preview:()=>[200,preview({preview_id:'prev-'+(++n)})],create:seq(EXPIRED,[201,{id:'task-9'}])});const {w}=env;try{
 await review(env);await confirmRun(env);
 ack(env,'run as vcap');next(env).click();await sleep(30);
 expect(previews(env).length).toBe(2);
 expect(creates(env).length).toBe(2);
 expect(previews(env)[1].body.client_request_id).toBe(creates(env)[1].body.client_request_id);
 expect(creates(env)[1].body.preview_id).toBe('prev-2');
 expect(previews(env)[0].body.client_request_id).toBe(creates(env)[0].body.client_request_id);
 }finally{w.close();}
});

t('a preview_bound discard says nothing was discarded and still offers Discard anyway',async()=>{
 const env=await setup();const {w}=env;try{
 pendingRoutes(env,[409,{success:false,error:'<b>bound</b>',code:'preview_bound'}]);
 await review(env);await confirmRun(env);
 discardBtn(env).click();await sleep(40);
 expect(unconfirmedText(env).includes(BOUND_TEXT)).toBe(true);
 expect(unconfirmedText(env).includes('Activity')).toBe(true);
 expect(unconfirmedText(env).includes(UNKNOWN_TEXT)).toBe(false);
 expect(anywayBtn(env)!==null).toBe(true);
 expect(anywayBtn(env).disabled).toBe(false);
 expect(w.__T.batchFlow.step).toBe(3);
 // The server text carries markup. None of it may reach the page.
 expect(env.d.body.innerHTML.includes('<b>bound</b>')).toBe(false);
 expect(env.d.body.textContent.includes('bound</b>')).toBe(false);
 expect(env.d.querySelector('.command-discard-unconfirmed b')).toBe(null);
 }finally{w.close();}
});

t('a 409 with a non-JSON body keeps the unknown-outcome message and Discard anyway',async()=>{
 const env=await setup();const {w}=env;try{
 pendingRoutes(env,()=>({raw:Promise.resolve({ok:false,status:409,headers:{get:()=>'text/html'},json:()=>Promise.reject(new Error('not json')),text:()=>Promise.resolve('<html><b>Conflict</b></html>')})}));
 await review(env);await confirmRun(env);
 discardBtn(env).click();await sleep(40);
 expect(unconfirmedText(env).includes(UNKNOWN_TEXT)).toBe(true);
 expect(unconfirmedText(env).includes(BOUND_TEXT)).toBe(false);
 expect(anywayBtn(env)!==null).toBe(true);
 expect(anywayBtn(env).disabled).toBe(false);
 expect(w.__T.batchFlow.step).toBe(3);
 expect(env.d.body.innerHTML.includes('<b>Conflict</b>')).toBe(false);
 expect(env.d.body.textContent.includes('Conflict')).toBe(false);
 }finally{w.close();}
});

for(const [name,spec] of [['a 500',[500,{success:false,error:'internal',code:'internal'}]],['a rejected fetch',reject],['a 409 with another code',[409,{success:false,error:'x',code:'other'}]]]){
 t(`${name} keeps the unknown-outcome message`,async()=>{
  const env=await setup();const {w}=env;try{
  pendingRoutes(env,spec);
  await review(env);await confirmRun(env);
  discardBtn(env).click();await sleep(40);
  expect(unconfirmedText(env).includes(UNKNOWN_TEXT)).toBe(true);
  expect(unconfirmedText(env).includes(BOUND_TEXT)).toBe(false);
  expect(anywayBtn(env)!==null).toBe(true);
  }finally{w.close();}
 });
}
