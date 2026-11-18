const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// The Settings page lists the cleanup retry interval after the other broker
// settings. It shows the interval in effect and where it comes from, and
// Apply sends the value under the key cleanup_retry_interval.
const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); } finally { env.w.close(); } }
});

const screen=async(sent)=>{
 const env=boot({noDialogs:true,routes:[
  ['/b/blacksmith/changes',(u,o,j)=>j(200,{changes:[]})],
  ['/b/blacksmith/settings',(u,o,j)=>{ sent.push(JSON.parse(o.body)); return j(200,{success:true,cleanup_retry_interval:'20m0s'}); }]
 ]});
 envs.push(env);
 await sleep(60);
 env.errors.length=0;
 const host=env.d.createElement('div'); env.d.body.appendChild(host);
 host.innerHTML=env.w.__T.renderBlacksmithSettings({vault_max_versions:5,reconciler_interval:'1h',errand_timeout:'30m0s',cleanup_retry_interval:'15m0s',cleanup_retry_interval_source:'BLACKSMITH_CLEANUP_RETRY_INTERVAL'},{});
 await env.w.__T.setupBlacksmithSettingsHandlers();
 await sleep(20);
 return {...env,host};
};

t('the cleanup retry interval shows its value and where it comes from',async()=>{
 const s=await screen([]);
 expect(s.host.querySelector('#settings-cleanup-retry-interval').value).toBe('15m0s');
 expect(s.host.querySelector('label[for="settings-cleanup-retry-interval"]').textContent).toBe('Cleanup Retry Interval');
 expect(s.host.querySelector('#help-cleanup-retry-interval').textContent).toContain('Now set by: BLACKSMITH_CLEANUP_RETRY_INTERVAL.');
 expect(s.host.querySelector('#help-cleanup-retry-interval').textContent).toContain('from 1m to 6h');
});

t('Apply sends cleanup_retry_interval and shows the saved interval',async()=>{
 const sent=[];
 const s=await screen(sent);
 s.host.querySelector('#settings-cleanup-retry-interval').value='20m';
 s.host.querySelector('#apply-cleanup-retry-interval').click();
 await sleep(40);
 expect(sent).toEqual([{cleanup_retry_interval:'20m'}]);
 expect(s.host.querySelector('#settings-cleanup-retry-interval').value).toBe('20m0s');
 expect(s.host.querySelector('#feedback-cleanup-retry-interval').textContent).toContain('Cleanup retry interval applied: 20m0s.');
});

t('an empty cleanup retry interval is refused before any request',async()=>{
 const sent=[];
 const s=await screen(sent);
 s.host.querySelector('#settings-cleanup-retry-interval').value='';
 s.host.querySelector('#apply-cleanup-retry-interval').click();
 await sleep(20);
 expect(sent).toEqual([]);
 expect(s.host.querySelector('#feedback-cleanup-retry-interval').textContent).toBe('Enter an interval from 1m to 6h, for example 15m or 1h.');
});

t('every Settings control has its own keep key',async()=>{
 const s=await screen([]);
 expect(s.host.querySelector('#settings-cleanup-retry-interval').dataset.keepKey).toBe('settings:cleanup-retry-interval');
 expect(s.host.querySelector('#apply-cleanup-retry-interval').dataset.keepKey).toBe('settings:apply-cleanup-retry-interval');
 expect(s.host.querySelector('#settings-errand-timeout').dataset.keepKey).toBe('settings:errand-timeout');
 expect(s.host.querySelector('#apply-errand-timeout').dataset.keepKey).toBe('settings:apply-errand-timeout');
 const controls=[...s.host.querySelectorAll('input, select, button')];
 expect(controls.length).toBeGreaterThan(0);
 expect(controls.filter((node)=>!node.dataset.keepKey).map((node)=>node.id)).toEqual([]);
 const keys=controls.map((node)=>node.dataset.keepKey);
 expect(new Set(keys).size).toBe(keys.length);
});
