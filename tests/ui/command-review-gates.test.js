const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {expectNoAlerts}=require('./certificates/no-alerts.js');
const t=JSDOM?test:test.skip;

const SECRET='echo SECRET-COMMAND-TEXT-7731';
const KEY='key-SECRET-CREDENTIAL-8841';
const PREVIEW_URL='/b/upgrade/commands/preview';
const CREATE_URL='/b/upgrade/tasks';

// Every env a test boots, checked once the test ends so a dialog error that
// the UI code swallows still fails it.
const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); expectNoAlerts(env);
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


const blockedInstance=(id)=>instance(id,{blocked:'held by another job'});
const help=(env)=>env.d.getElementById('batch-flow-help').textContent;
const refresh=(env)=>env.d.getElementById('batch-flow-ack').dispatchEvent(new env.w.Event('input',{bubbles:true}));

t('typing the exact word the prompt shows enables the run button',async()=>{
 const env=await setup();const {d}=env;try{
 await review(env,['val-a']);
 const word=d.querySelector('.confirm-word').textContent;
 expect(help(env)).toContain(word);
 expect(next(env).disabled).toBe(true);
 ack(env,word);
 expect(next(env).disabled).toBe(false);
 ack(env,'  '+word.toUpperCase()+'  ');
 expect(next(env).disabled).toBe(false);
 ack(env,word.slice(0,-1));
 expect(next(env).disabled).toBe(true);
 }finally{env.w.close();}
});

t('a held instance keeps the button off and the help says which one',async()=>{
 const env=await setup({preview:[200,preview({instances:[blockedInstance('val-a'),instance('val-b')]})]});const {d}=env;try{
 await review(env,['val-a','val-b']);
 ack(env,d.querySelector('.confirm-word').textContent);
 expect(next(env).disabled).toBe(true);
 expect(help(env)).toBe('Another operation holds val-a-name. Deselect it to continue.');
 }finally{env.w.close();}
});

t('a selection with no node to run keeps the button off and the help says why',async()=>{
 const env=await setup({preview:[200,preview({instances:[instance('val-a',{vms:[vm(0,{targeted:false})]})]})]});const {d}=env;try{
 await review(env,['val-a']);
 ack(env,d.querySelector('.confirm-word').textContent);
 expect(next(env).disabled).toBe(true);
 expect(help(env)).toBe('No node in this selection can run the command. Change the selection to continue.');
 }finally{env.w.close();}
});

t('while the review is loading, the help says so even with the word typed',async()=>{
 const env=await setup();const {w,d}=env;try{
 await review(env,['val-a']);
 ack(env,d.querySelector('.confirm-word').textContent);
 expect(next(env).disabled).toBe(false);
 w.__T.batchFlow.loading=true;refresh(env);
 expect(next(env).disabled).toBe(true);
 expect(help(env)).toBe('Preparing the review. The button turns on when it is ready.');
 w.__T.batchFlow.loading=false;refresh(env);
 expect(next(env).disabled).toBe(false);
 expect(help(env)).not.toContain('Preparing');
 }finally{env.w.close();}
});

t('while the run is starting, the help says so even with the word typed',async()=>{
 const env=await setup();const {w,d}=env;try{
 await review(env,['val-a']);
 ack(env,d.querySelector('.confirm-word').textContent);
 w.__T.batchFlow.submitting=true;refresh(env);
 expect(next(env).disabled).toBe(true);
 expect(help(env)).toBe('The operation is starting. Please wait.');
 w.__T.batchFlow.submitting=false;refresh(env);
 expect(next(env).disabled).toBe(false);
 }finally{env.w.close();}
});
