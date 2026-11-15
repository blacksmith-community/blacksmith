const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const OAT=fs.readFileSync(path.resolve(__dirname,'..','..','ui','vendor','oat','oat.min.js'),'utf8');

// The Settings and Endpoints pages each show the latest changes the broker
// recorded for them. The broker hides a secret's values before it stores a
// record, and the page checks the key by name again, so a raw value that sits
// under a secret key never reaches the markup, an attribute, or the console.
const RAW_OLD='old-raw-secret-value-1';
const RAW_NEW='new-raw-secret-value-2';
const at=(minutes)=>new Date(Date.UTC(2026,10,14,12,minutes,0)).toISOString();

const settingsChanges=[
 {id:'c3',at:at(3),scope:'settings',actor:'operator',from:'10.0.0.9',target:'broker',change:'update',key:'reconciler_interval',old:'1h',new:'30m'},
 {id:'c2',at:at(2),scope:'settings',actor:'operator',target:'certificates',change:'update',key:'cf.password',old:RAW_OLD,new:RAW_NEW},
 {id:'c1',at:at(1),scope:'settings',target:'batch',change:'update',key:'max_batch_jobs',old:'',new:'4'}
];
const endpointChanges=[
 {id:'e2',at:at(5),scope:'endpoints',actor:'operator',target:'prod-cf',change:'register',key:'status',old:'created',new:'registering'},
 {id:'e1',at:at(4),scope:'endpoints',actor:'operator',target:'prod-cf',change:'create',key:'api_url',new:'https://api.example.test'}
];

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); } finally { env.w.close(); } }
});

// Every place a value could leak: the markup, each attribute, the console, and the errors.
const leaks=(env,host,values)=>{
 const attrs=[...host.querySelectorAll('*')].flatMap(n=>[...n.attributes].map(a=>a.value)).join('\n');
 const places=[host.innerHTML,attrs,env.logs.join('\n'),env.errors.join('\n')];
 return values.filter(v=>places.some(p=>p.includes(v)));
};

const settingsScreen=async(state)=>{
 const env=boot({noDialogs:true,routes:[
  ['/b/blacksmith/changes',(u,o,j)=>{ state.requests.push(u); return state.fail?j(503,{error:'vault unavailable',code:'vault_unavailable'}):j(200,{changes:state.changes}); }],
  ['/b/blacksmith/settings',(u,o,j)=>j(200,{success:true,reconciler_interval:'30m'})]
 ]});
 envs.push(env);
 await sleep(60);
 // The page's own boot requests are not stubbed here, so their noise is cleared first.
 env.errors.length=0;
 const host=env.d.createElement('div'); env.d.body.appendChild(host);
 host.innerHTML=env.w.__T.renderBlacksmithSettings({vault_max_versions:5,reconciler_interval:'1h'},{});
 await env.w.__T.setupBlacksmithSettingsHandlers();
 await sleep(20);
 return {...env,host,section:()=>host.querySelector('[data-recent-changes="settings"]')};
};

const rows=(section)=>[...section.querySelectorAll('.recent-changes-table tbody tr')].map(r=>[...r.cells].map(c=>c.textContent.trim().replace(/\s+/g,' ')));

t('settings page lists the latest settings changes newest first',async()=>{
 const state={changes:settingsChanges,requests:[]};
 const s=await settingsScreen(state);
 const section=s.section();
 expect(section).toBeTruthy();
 expect(section.querySelector('h3').textContent).toBe('Recent changes');
 expect(state.requests).toEqual(['/b/blacksmith/changes?scope=settings&limit=10']);
 const table=rows(section);
 expect(table.length).toBe(3);
 expect(table[0].slice(1)).toEqual(['operator 10.0.0.9','Broker reconciler_interval','Changed','1h','30m']);
 expect(table[2][1]).toBe('—');
 expect(table[2][2]).toBe('Batch operations max_batch_jobs');
 expect(table[2][4]).toBe('—');
 expect(section.querySelector('table').getAttribute('data-sortable')).toBe('false');
 // The runtime and configuration headings keep their own class.
 expect([...s.host.querySelectorAll('.settings-section-header')].map(h=>h.textContent)).toEqual(['Runtime settings','Current configuration']);
});

t('a value under a secret key never reaches the page or the console',async()=>{
 const state={changes:settingsChanges,requests:[]};
 const s=await settingsScreen(state);
 const table=rows(s.section());
 expect(table[1].slice(2)).toEqual(['Certificates cf.password','Changed','(changed)','(changed)']);
 expect(leaks(s,s.host,[RAW_OLD,RAW_NEW])).toEqual([]);
});

t('a failed load shows the failure panel and Retry loads the list',async()=>{
 const state={changes:settingsChanges,requests:[],fail:true};
 const s=await settingsScreen(state);
 const section=s.section();
 const panel=section.querySelector('[data-load-error]');
 expect(panel).toBeTruthy();
 expect(panel.textContent).toContain('Could not load recent changes.');
 const retry=panel.querySelector('button[data-load-retry]');
 expect(retry.textContent).toBe('Retry');
 expect(retry.getAttribute('data-keep-key')).toBe('load-retry:recent-changes:settings');
 expect(s.errors).toEqual([]);
 state.fail=false;
 retry.click();
 await sleep(20);
 expect(section.querySelector('[data-load-error]')).toBeNull();
 expect(rows(section).length).toBe(3);
});

t('an empty list says no changes are recorded',async()=>{
 const state={changes:[],requests:[]};
 const s=await settingsScreen(state);
 expect(s.section().querySelector('.recent-changes-body').textContent.trim()).toBe('No settings changes recorded yet.');
});

t('a successful Apply loads the list again',async()=>{
 const state={changes:settingsChanges.slice(1),requests:[]};
 const s=await settingsScreen(state);
 expect(rows(s.section()).length).toBe(2);
 state.changes=settingsChanges;
 const input=s.host.querySelector('#settings-reconciler-interval');
 input.value='30m';
 s.host.querySelector('#apply-reconciler-interval').click();
 await sleep(40);
 expect(state.requests.length).toBe(2);
 expect(rows(s.section()).length).toBe(3);
});

const brokerScreen=async(state)=>{
 const env=boot({noDialogs:true,routes:[
  ['/b/blacksmith/changes',(u,o,j)=>{ state.requests.push(u); return j(200,{changes:state.changes}); }],
  ['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],
  ['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],
  ['/v2/catalog',(u,o,j)=>j(200,{services:[]})],
  ['/b/cf/endpoints',(u,o,j)=>j(200,{})],
  ['/b/cf/registrations/',(u,o,j)=>j(200,{success:true})],
  ['/b/cf/registrations',(u,o,j)=>j(200,{registrations:[]})]
 ]});
 envs.push(env);
 env.w.eval(OAT);
 await sleep(60);
 const host=env.d.createElement('div'); env.d.body.appendChild(host);
 host.innerHTML=env.w.__T.renderBrokerTab();
 await env.w.__T.initBrokerTab();
 await sleep(20);
 return {...env,host,section:()=>host.querySelector('[data-recent-changes="endpoints"]')};
};

t('endpoints page lists the latest endpoint changes',async()=>{
 const state={changes:endpointChanges,requests:[]};
 const s=await brokerScreen(state);
 const section=s.section();
 expect(section).toBeTruthy();
 expect(state.requests).toEqual(['/b/blacksmith/changes?scope=endpoints&limit=10']);
 const table=rows(section);
 expect(table.map(r=>r.slice(1))).toEqual([
  ['operator','prod-cf status','Registration started','created','registering'],
  ['operator','prod-cf api_url','Created','—','https://api.example.test']
 ]);
 expect(s.errors).toEqual([]);
});

t('deleting a registration loads the endpoint changes again',async()=>{
 const state={changes:endpointChanges,requests:[]};
 const s=await brokerScreen(state);
 const done=s.w.__T.CFRegistrationManager.deleteRegistration('r1');
 await sleep(20);
 s.d.querySelector('[data-dialog-role="confirm"]').click();
 await done;
 await sleep(20);
 expect(state.requests.length).toBe(2);
});

// A record from an older broker can still hold a URL with its password.
// The page replaces the password and keeps the rest of the URL.
t('a password inside a URL never reaches the page',async()=>{
 const RAW_URL_PW='url-raw-pw-5c3a';
 const state={changes:[{id:'u1',at:at(6),scope:'endpoints',actor:'operator',target:'prod-cf',change:'update',key:'api_url',old:'https://api.old.example.test',new:`https://admin:${RAW_URL_PW}@api.example.test/v3`}],requests:[]};
 const s=await brokerScreen(state);
 const table=rows(s.section());
 expect(table[0].slice(4)).toEqual(['https://api.old.example.test','https://admin:(changed)@api.example.test/v3']);
 expect(leaks(s,s.host,[RAW_URL_PW])).toEqual([]);
});
