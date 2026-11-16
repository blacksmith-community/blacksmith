const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// The Settings page lists the errand timeout beside the reconciler interval.
// It shows the wait in effect and where it comes from, and Apply sends the
// value under the key errand_timeout.
const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); } finally { env.w.close(); } }
});

const screen=async(sent)=>{
 const env=boot({noDialogs:true,routes:[
  ['/b/blacksmith/changes',(u,o,j)=>j(200,{changes:[]})],
  ['/b/blacksmith/settings',(u,o,j)=>{ sent.push(JSON.parse(o.body)); return j(200,{success:true,errand_timeout:'45m0s'}); }]
 ]});
 envs.push(env);
 await sleep(60);
 env.errors.length=0;
 const host=env.d.createElement('div'); env.d.body.appendChild(host);
 host.innerHTML=env.w.__T.renderBlacksmithSettings({vault_max_versions:5,reconciler_interval:'1h',errand_timeout:'30m0s',errand_timeout_source:'default'},{});
 await env.w.__T.setupBlacksmithSettingsHandlers();
 await sleep(20);
 return {...env,host};
};

t('the errand timeout shows its value and where it comes from',async()=>{
 const s=await screen([]);
 expect(s.host.querySelector('#settings-errand-timeout').value).toBe('30m0s');
 expect(s.host.querySelector('label[for="settings-errand-timeout"]').textContent).toBe('Errand Timeout');
 expect(s.host.querySelector('#help-errand-timeout').textContent).toContain('Now set by: default.');
});

t('Apply sends errand_timeout and shows the saved wait',async()=>{
 const sent=[];
 const s=await screen(sent);
 s.host.querySelector('#settings-errand-timeout').value='45m';
 s.host.querySelector('#apply-errand-timeout').click();
 await sleep(40);
 expect(sent).toEqual([{errand_timeout:'45m'}]);
 expect(s.host.querySelector('#settings-errand-timeout').value).toBe('45m0s');
 expect(s.host.querySelector('#feedback-errand-timeout').textContent).toContain('Errand timeout applied: 45m0s.');
});

t('an empty errand timeout is refused before any request',async()=>{
 const sent=[];
 const s=await screen(sent);
 s.host.querySelector('#settings-errand-timeout').value='';
 s.host.querySelector('#apply-errand-timeout').click();
 await sleep(20);
 expect(sent).toEqual([]);
 expect(s.host.querySelector('#feedback-errand-timeout').textContent).toBe('Enter a duration, for example 30m or 1h.');
});
