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
// {registration:{...}}. statuses are answered in turn, and the last repeats.
const open=async(statuses,{limit=2000}={})=>{
 let answered=0;
 const env=boot({noDialogs:true,oat:true,routes:[...QUIET,
  [/\/b\/cf\/registrations$/,(u,o,j)=>j(200,{registrations:[]})],
  [/\/b\/cf\/registrations\/reg-1$/,(u,o,j)=>{ const s=statuses[Math.min(answered++,statuses.length-1)]; return j(200,{registration:{id:'reg-1',name:'lab-cf',...s}}); }]]});
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
 return {env,manager,toasts,polls:()=>env.calls.filter(c=>/^GET \/b\/cf\/registrations\/reg-1$/.test(c)).length};
};
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
