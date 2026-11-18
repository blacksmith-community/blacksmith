const {test,expect,afterEach}=require('bun:test');
const {main:{boot,idle,TEST_LIMIT}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?(n,f,l)=>test(n,f,l===undefined?TEST_LIMIT:l):test.skip;

const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); } finally { env.w.close(); } } });

const okStatus=(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}});
const okCatalog=(u,o,j)=>j(200,{services:[]});

// The SSH status check. A page that cannot read the status leaves the buttons alone.
const sshStart=async(answer)=>{
 const ticks=[];
 const state={answer};
 const env=boot({noDialogs:true,
  setup:(w)=>{
   w.setInterval=(fn)=>{ ticks.push(fn); return ticks.length; };
   w.document.body.insertAdjacentHTML('beforeend','<table><tbody><tr><td class="vm-instance">web/0</td><td><button class="ssh-btn" title="SSH to web/0" onclick="void 0">ssh</button></td></tr></tbody></table>');
  },
  routes:[['/b/config/ssh/ui-terminal-status',(u,o,j)=>state.answer(u,o,j)],['/v2/catalog',okCatalog],['/b/status',okStatus]]});
 envs.push(env);
 await idle(env.w);
 const button=env.d.querySelector('.ssh-btn');
 const poll=async()=>{ for(const fn of ticks) fn(); await idle(env.w); };
 return {env,state,button,poll};
};
const answer503=(u,o,j)=>j(503,{enabled:false,message:'Unavailable right now'});
const answerOff=(u,o,j)=>j(200,{enabled:false,message:'Switched off by the operator'});
const answerOn=(u,o,j)=>j(200,{enabled:true});

t('a 503 from the SSH status check leaves the buttons enabled with no misleading title',async()=>{
 const {button}=await sshStart(answer503);
 expect(button.disabled).toBe(false);
 expect(button.title).toBe('SSH to web/0');
 expect(button.classList.contains('is-busy')).toBe(false);
});

t('a network failure of the SSH status check leaves the buttons enabled',async()=>{
 const {button}=await sshStart(()=>Promise.reject(new TypeError('offline')));
 expect(button.disabled).toBe(false);
 expect(button.title).toBe('SSH to web/0');
});

t('a body that does not say enabled or not is status unknown',async()=>{
 const {button}=await sshStart((u,o,j)=>j(200,{message:'maintenance'}));
 expect(button.disabled).toBe(false);
 expect(button.title).toBe('SSH to web/0');
});

t('a real disabled answer still disables the buttons',async()=>{
 const {button}=await sshStart(answerOff);
 expect(button.disabled).toBe(true);
 expect(button.title).toBe('Switched off by the operator');
});

t('a later failed check re-enables buttons and the next good check is read',async()=>{
 const {state,button,poll}=await sshStart(answerOff);
 expect(button.disabled).toBe(true);
 state.answer=answer503;
 await poll();
 expect(button.disabled).toBe(false);
 expect(button.title).toBe('SSH to web/0');
 state.answer=answerOff;
 await poll();
 expect(button.disabled).toBe(true);
 state.answer=answerOn;
 await poll();
 expect(button.disabled).toBe(false);
});
