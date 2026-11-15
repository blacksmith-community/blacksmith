const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// A failed read answers with a body the page must never show.
const SECRET='upstream-detail-do-not-show';
const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); } finally { env.w.close(); } } });

const okStatus=(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}});
const okCatalog=(u,o,j)=>j(200,{services:[]});
const fail=(status,text)=>(u,o,j)=>Promise.resolve({ok:false,status,statusText:text,json:()=>Promise.resolve({error:SECRET,url:u}),text:()=>Promise.resolve(SECRET)});

// The page reads the catalog first, so the first route decides how the load goes.
const start=async(catalog,extra=[])=>{
 const state={catalog,calls:0};
 const env=boot({noDialogs:true,routes:[
  ['/v2/catalog',(u,o,j)=>{ state.calls++; return state.catalog(u,o,j); }],
  ['/b/status',okStatus],
  ['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{enabled:true})],
  ...extra]});
 envs.push(env);
 await sleep(60);
 return {env,state};
};
const panelOf=(d,id)=>d.querySelector(`#${id} [data-load-error]`);

t('a failed first load shows one alert with a Retry button and no raw detail',async()=>{
 const {env}=await start(fail(503,'Service Unavailable'));
 const {d}=env;
 for(const id of ['blacksmith','services']){
  const panel=panelOf(d,id);
  expect(panel).not.toBeNull();
  expect(panel.getAttribute('role')).toBe('alert');
  expect(panel.textContent).toContain('503');
  expect(panel.textContent).toContain('Service Unavailable');
  expect(panel.querySelector('button').textContent.trim()).toBe('Retry');
 }
 const text=d.querySelector('#blacksmith').textContent+d.querySelector('#services').textContent;
 expect(text).not.toContain('Failed to load data');
 expect(text).not.toContain('Service unavailable');
 expect(text).not.toContain(SECRET);
 expect(text).not.toContain('/v2/catalog');
 expect(d.querySelector('#blacksmith').innerHTML).not.toContain('/v2/catalog');
});

t('the Retry buttons carry different keep keys',async()=>{
 const {env}=await start(fail(503,'Service Unavailable'));
 const keys=[...env.d.querySelectorAll('[data-load-retry]')].map(b=>b.getAttribute('data-keep-key'));
 expect(keys.length).toBe(2);
 expect(keys.every(Boolean)).toBe(true);
 expect(new Set(keys).size).toBe(2);
 for(const key of keys) expect(env.d.querySelectorAll(`[data-keep-key="${key}"]`).length).toBe(1);
});

t('a network failure says the broker could not be reached',async()=>{
 const {env}=await start(()=>Promise.reject(new TypeError('NetworkError when attempting to fetch resource.')));
 const panel=panelOf(env.d,'blacksmith');
 expect(panel.getAttribute('role')).toBe('alert');
 expect(panel.textContent).toContain('could not be reached');
 expect(panel.textContent).not.toContain('NetworkError');
});

t('Retry repeats the load and the page loads when it succeeds',async()=>{
 const {env,state}=await start(fail(503,'Service Unavailable'));
 const {d}=env;
 expect(state.calls).toBe(1);
 state.catalog=okCatalog;
 d.querySelector('#blacksmith [data-load-retry]').click();
 await sleep(80);
 expect(state.calls).toBe(2);
 expect(d.querySelector('[data-load-error]')).toBeNull();
 expect(d.querySelector('#blacksmith').textContent).not.toContain('Could not load');
});

t('Retry that fails again shows the panel again',async()=>{
 const {env,state}=await start(fail(503,'Service Unavailable'));
 const {d}=env;
 d.querySelector('#services [data-load-retry]').click();
 await sleep(80);
 expect(state.calls).toBe(2);
 expect(panelOf(d,'blacksmith')).not.toBeNull();
 expect(panelOf(d,'services')).not.toBeNull();
});

t('a 401 keeps its plain sign-in message',async()=>{
 const {env}=await start(fail(401,'Unauthorized'));
 const panel=panelOf(env.d,'blacksmith');
 expect(panel.textContent).toContain('Authentication required');
 expect(panel.textContent).not.toContain(SECRET);
});
