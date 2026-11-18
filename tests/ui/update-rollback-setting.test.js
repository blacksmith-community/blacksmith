const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// The Settings page lists the rollback after a failed update below the
// errand timeout. It shows whether the rollback is on and where that comes
// from, and Apply sends a boolean under the key update_rollback.
const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); } finally { env.w.close(); } }
});

const screen=async(sent,settings={})=>{
 const env=boot({noDialogs:true,routes:[
  ['/b/blacksmith/changes',(u,o,j)=>j(200,{changes:[]})],
  ['/b/blacksmith/settings',(u,o,j)=>{ const body=JSON.parse(o.body); sent.push(body); return j(200,{success:true,...body}); }]
 ]});
 envs.push(env);
 await sleep(60);
 env.errors.length=0;
 const host=env.d.createElement('div'); env.d.body.appendChild(host);
 host.innerHTML=env.w.__T.renderBlacksmithSettings({vault_max_versions:5,reconciler_interval:'1h',errand_timeout:'30m0s',errand_timeout_source:'default',update_rollback:false,update_rollback_source:'default',...settings},{});
 await env.w.__T.setupBlacksmithSettingsHandlers();
 await sleep(20);
 return {...env,host};
};

t('the update rollback shows off and where that comes from',async()=>{
 const s=await screen([]);
 const select=s.host.querySelector('#settings-update-rollback');
 expect(select.value).toBe('false');
 expect(select.selectedOptions[0].textContent).toBe('Off');
 expect(s.host.querySelector('label[for="settings-update-rollback"]').textContent).toBe('Update Rollback');
 expect(s.host.querySelector('#help-update-rollback').textContent).toContain('Now set by: default.');
});

t('a saved rollback shows on',async()=>{
 const s=await screen([],{update_rollback:true,update_rollback_source:'Vault settings'});
 expect(s.host.querySelector('#settings-update-rollback').value).toBe('true');
 expect(s.host.querySelector('#help-update-rollback').textContent).toContain('Now set by: Vault settings.');
});

t('Apply sends update_rollback as a boolean and shows the saved value',async()=>{
 const sent=[];
 const s=await screen(sent);
 s.host.querySelector('#settings-update-rollback').value='true';
 s.host.querySelector('#apply-update-rollback').click();
 await sleep(40);
 expect(sent).toEqual([{update_rollback:true}]);
 expect(s.host.querySelector('#settings-update-rollback').value).toBe('true');
 expect(s.host.querySelector('#feedback-update-rollback').textContent).toContain('Update rollback applied: on.');
});

t('every rollback control has its own keep key',async()=>{
 const s=await screen([]);
 const keys=['#settings-update-rollback','#apply-update-rollback'].map((id)=>s.host.querySelector(id).dataset.keepKey);
 expect(keys.every(Boolean)).toBe(true);
 expect(new Set(keys).size).toBe(keys.length);
 const all=[...s.host.querySelectorAll('[data-keep-key]')].map((node)=>node.dataset.keepKey);
 expect(new Set(all).size).toBe(all.length);
});
