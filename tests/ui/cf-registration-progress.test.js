const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// Routes for the page's own load, so a console error means something broke.
const QUIET=[[/\/b\/status$/,(u,o,j)=>j(200,{env:'Local fixture',instances:{},plans:{}})],[/\/b\/instance$/,(u,o,j)=>j(200,{deployment:'blacksmith'})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],[/inventory\?summary_only=true/,(u,o,j)=>j(200,{summary:{certificates:0,needs_attention:0}})]];

const envs=[];
afterEach(()=>{while(envs.length){const env=envs.pop();expect(env.alerts).toEqual([]);expect(env.errors).toEqual([]);env.w.close();}});

// The progress view polls GET /b/cf/registrations/{id}, which answers
// {registration:{...}}. Each registration's answers come in turn, and the
// last repeats. An answer is a status for a good answer, or a function that
// makes the response, for the answers the view has to stop or retry on.
const open=async(statuses,{limit=2000,others={},delays={}}={})=>{
 const answers={'reg-1':statuses,...others};
 const answered={};
 const env=boot({noDialogs:true,oat:true,routes:[...QUIET,
  [/\/b\/cf\/registrations$/,(u,o,j)=>j(200,{registrations:[]})],
  [/\/b\/cf\/registrations\/([^/?]+)$/,(u,o,j)=>{
   const id=u.match(/registrations\/([^/?]+)$/)[1];
   const list=answers[id]; if(!list) return undefined;
   const n=answered[id]=(answered[id]||0)+1;
   const a=list[Math.min(n-1,list.length-1)];
   const make=()=>typeof a==='function'?a():j(200,{registration:{id,name:'lab-cf',...a}});
   const wait=(delays[id]||[])[n-1];
   return wait?new Promise(res=>setTimeout(()=>res(make()),wait)):make();
  }]]});
 envs.push(env);
 await sleep(40);
 const {w}=env;
 // There is no stream route, so the page must never open one.
 w.EventSource=class{ constructor(){ env.streams=(env.streams||0)+1; } close(){} };
 const toasts=[];
 const manager=w.__T.CFRegistrationManager;
 manager.showSuccess=(m)=>toasts.push('success:'+m);
 manager.showError=(m)=>toasts.push('error:'+m);
 manager.progressPollMs=20;
 manager.progressLimitMs=limit;
 manager.showProgressStream('reg-1');
 const polls=(id='reg-1')=>env.calls.filter(c=>c==='GET /b/cf/registrations/'+id).length;
 return {env,manager,toasts,polls};
};
// A response the harness's JSON helper can't make: any status, a body as
// text, and a Content-Type header.
const answer=(status,text,type='application/json')=>()=>Promise.resolve({ok:status>=200&&status<300,status,statusText:'',headers:{get:(h)=>/content-type/i.test(h)?type:null},json:()=>Promise.resolve(JSON.parse(text)),text:()=>Promise.resolve(text)});
const offline=()=>()=>Promise.reject(new TypeError('Failed to fetch'));
const PAGE='<!doctype html><html><head><title>Sign in</title></head><body><h1>Sign in</h1><form></form></body></html>';
const header=(env)=>env.d.querySelector('#registration-progress .progress-header h4').textContent;
const stepClasses=(env)=>[...env.d.querySelectorAll('#registration-progress .progress-step')].map(s=>['running','success','error'].filter(c=>s.classList.contains(c)).join(' '));

t('a registration that succeeds moves the steps and stops polling',async()=>{
 const {env,toasts,polls}=await open([{status:'registering'},{status:'registering'},{status:'active'}]);
 await sleep(30);
 expect(stepClasses(env).every(c=>c==='running'||c==='success')).toBe(true);
 await sleep(150);
 expect(header(env)).toBe('Registration COMPLETED');
 expect(stepClasses(env)).toEqual(['success','success','success','success','success']);
 expect(toasts).toEqual(['success:Registration completed successfully!']);
 const seen=polls();
 await sleep(120);
 expect(polls()).toBe(seen);
 expect(env.streams||0).toBe(0);
 expect(env.calls.filter(c=>/\/stream/.test(c))).toEqual([]);
});

t('a registration that fails shows its error and stops polling',async()=>{
 const {env,toasts,polls}=await open([{status:'registering'},{status:'failed',last_error:'the CF API refused the broker'}]);
 await sleep(200);
 expect(header(env)).toBe('Registration FAILED');
 expect(stepClasses(env)).toEqual(['error','error','error','error','error']);
 expect(toasts).toEqual(['error:Registration failed: the CF API refused the broker']);
 const seen=polls();
 await sleep(120);
 expect(polls()).toBe(seen);
});

t('a registration that never finishes times out once and stops polling',async()=>{
 const {env,toasts,polls}=await open([{status:'registering'}],{limit:150});
 await sleep(260);
 expect(header(env)).toBe('Registration Progress - TIMEOUT');
 expect(toasts).toEqual(['error:Registration progress monitoring timed out. Please check status manually.']);
 const seen=polls();
 expect(seen).toBeGreaterThan(1);
 await sleep(120);
 expect(polls()).toBe(seen);
});

t('closing the progress view clears the poll and its time limit',async()=>{
 const {env,manager,toasts,polls}=await open([{status:'registering'}],{limit:300});
 await sleep(70);
 expect(polls()).toBeGreaterThan(1);
 manager.hideRegistrationModal();
 const seen=polls();
 await sleep(400);
 expect(polls()).toBe(seen);
 expect(toasts).toEqual([]);
});

t('navigating away clears the poll and its time limit',async()=>{
 const {env,toasts,polls}=await open([{status:'registering'}],{limit:300});
 await sleep(70);
 env.w.dispatchEvent(new env.w.PopStateEvent('popstate',{state:null}));
 const seen=polls();
 await sleep(400);
 expect(polls()).toBe(seen);
 expect(toasts).toEqual([]);
});

t('the page has no registration stream left',()=>{
 const src=fs.readFileSync(path.join(__dirname,'../../ui/blacksmith.js'),'utf8');
 expect(src.match(/\/b\/cf\/registrations\/[^`'"]*\/stream/g)||[]).toEqual([]);
 expect(src.match(/new EventSource\([^)]*registrations/g)||[]).toEqual([]);
});

for(const [status,said] of [[401,'refused'],[403,'refused'],[404,'no longer has this registration']]){
 t(`a ${status} stops the poll at once with a readable message`,async()=>{
  const {env,toasts,polls}=await open([answer(status,JSON.stringify({error:'nope'}))]);
  await sleep(150);
  expect(polls()).toBe(1);
  expect(toasts.length).toBe(1);
  expect(toasts[0]).toContain(`HTTP ${status}`);
  expect(toasts[0]).toContain(said);
  expect(header(env)).toBe('Registration Progress - STOPPED');
 });
}

for(const [name,make] of [['a 200 web page',answer(200,PAGE,'text/html; charset=utf-8')],['a 401 web page',answer(401,PAGE,'text/html')],['a web page with no type',answer(200,PAGE,'')]]){
 t(`${name} stops the poll at once and never shows its markup`,async()=>{
  const {env,toasts,polls}=await open([make]);
  await sleep(150);
  expect(polls()).toBe(1);
  expect(toasts.length).toBe(1);
  expect(toasts[0]).toContain('web page');
  expect(toasts[0]).not.toContain('<');
  expect(toasts[0]).not.toContain('Sign in');
  expect(env.d.querySelector('#registration-progress').innerHTML).not.toContain('Sign in');
 });
}

for(const [name,make,said] of [['network errors',offline(),'network error'],['5xx answers',answer(503,PAGE,'text/html'),'HTTP 503']]){
 t(`${name} keep the poll going and stop it after three in a row`,async()=>{
  const {env,toasts,polls}=await open([make]);
  await sleep(250);
  expect(polls()).toBe(3);
  expect(toasts.length).toBe(1);
  expect(toasts[0]).toContain('3 checks in a row');
  expect(toasts[0]).toContain(said);
  expect(toasts[0]).not.toContain('<');
  expect(header(env)).toBe('Registration Progress - STOPPED');
 });
}

t('a good answer resets the count of failed checks',async()=>{
 const {env,toasts,polls}=await open([offline(),answer(500,'{"error":"vault"}'),{status:'registering'},offline(),answer(502,'bad gateway','text/plain'),{status:'registering'},offline(),offline(),{status:'active'}]);
 await sleep(400);
 expect(polls()).toBe(9);
 expect(header(env)).toBe('Registration COMPLETED');
 expect(toasts).toEqual(['success:Registration completed successfully!']);
});

t('opening progress twice leaves one poller and drops the first one\'s late answers',async()=>{
 const {env,manager,toasts,polls}=await open([{status:'failed',last_error:'stale'}],{delays:{'reg-1':[60]},others:{'reg-2':[{status:'registering'},{status:'active'}]}});
 await sleep(5);
 manager.showProgressStream('reg-2');
 await sleep(300);
 expect(polls('reg-1')).toBe(1);
 expect(polls('reg-2')).toBeGreaterThan(1);
 expect(header(env)).toBe('Registration COMPLETED');
 expect(toasts).toEqual(['success:Registration completed successfully!']);
 const seen=polls('reg-2');
 await sleep(100);
 expect(polls('reg-2')).toBe(seen);
});

t('a late failure from a replaced poll can\'t stop the new one',async()=>{
 const {env,manager,toasts,polls}=await open([offline()],{delays:{'reg-1':[0,0,80]},others:{'reg-2':[{status:'registering'},{status:'registering'},{status:'active'}]}});
 await sleep(50);
 expect(polls('reg-1')).toBe(3);
 manager.showProgressStream('reg-2');
 await sleep(300);
 expect(polls('reg-1')).toBe(3);
 expect(header(env)).toBe('Registration COMPLETED');
 expect(toasts).toEqual(['success:Registration completed successfully!']);
});
