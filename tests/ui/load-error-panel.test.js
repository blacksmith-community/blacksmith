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

// The detail views of the broker and of an instance use the same panel.
const detailStart=async(extra)=>{
 const out=await start(okCatalog,extra);
 const {env}=out;
 out.container=(id)=>{
  const host=env.d.querySelector(`#${id} .detail-content`)||(()=>{ const div=env.d.createElement('div'); div.className='detail-content'; env.d.getElementById(id).appendChild(div); return div; })();
  return host;
 };
 return out;
};
const answers=(state)=>(u,o,j)=>state.answer(u,o,j);

t('a broker tab that cannot load shows the panel and Retry reloads it',async()=>{
 const state={answer:fail(503,'Service Unavailable')};
 const {env}=await detailStart([['/b/configs',answers(state)]]);
 const {w,d}=env;
 expect(typeof w.loadBlacksmithSection).toBe('function');
 await w.loadBlacksmithSection('configs');
 await sleep(30);
 const panel=d.querySelector('#blacksmith .detail-content [data-load-error]');
 expect(panel).not.toBeNull();
 expect(panel.getAttribute('role')).toBe('alert');
 expect(panel.textContent).toContain('503');
 expect(panel.textContent).not.toContain(SECRET);
 expect(panel.textContent).not.toContain('/b/configs');
 expect(d.querySelector('#blacksmith .detail-content').textContent).not.toContain('Failed to load');
 state.answer=(u,o,j)=>j(200,[]);
 panel.querySelector('[data-load-retry]').click();
 await sleep(80);
 expect(d.querySelector('#blacksmith .detail-content [data-load-error]')).toBeNull();
});

t('a manifest that cannot load shows the status and never the broker body',async()=>{
 const state={answer:fail(500,'Internal Server Error')};
 const {env}=await detailStart([['/manifest',answers(state)]]);
 const {w,d}=env;
 await w.loadBlacksmithSection('manifest');
 await sleep(30);
 const panel=d.querySelector('#blacksmith .detail-content [data-load-error]');
 expect(panel).not.toBeNull();
 expect(panel.textContent).toContain('500');
 expect(panel.textContent).not.toContain(SECRET);
});

t('an instance tab that cannot load shows the panel and Retry reloads it',async()=>{
 const state={answer:fail(502,'Bad Gateway')};
 const {env}=await detailStart([['/b/inst1/vms',answers(state)]]);
 const {w,d}=env;
 const host=d.createElement('div'); host.className='detail-content'; d.getElementById('services').appendChild(host);
 w.currentInstanceInfo={id:'inst1'};
 expect(typeof w.loadInstanceSection).toBe('function');
 await w.loadInstanceSection('inst1','vms');
 await sleep(30);
 const panel=host.querySelector('[data-load-error]');
 expect(panel).not.toBeNull();
 expect(panel.textContent).toContain('502');
 expect(panel.textContent).toContain('Bad Gateway');
 expect(panel.textContent).not.toContain(SECRET);
 expect(panel.textContent).not.toContain('/b/inst1');
 const calls=env.calls.filter((c)=>c.includes('/b/inst1/vms')).length;
 state.answer=(u,o,j)=>j(200,[]);
 panel.querySelector('[data-load-retry]').click();
 await sleep(80);
 expect(env.calls.filter((c)=>c.includes('/b/inst1/vms')).length).toBe(calls+1);
 expect(host.querySelector('[data-load-error]')).toBeNull();
});

t('every Retry button on a page has its own keep key',async()=>{
 const {env}=await detailStart([['/b/configs',fail(503,'Service Unavailable')]]);
 const {w,d}=env;
 await w.loadBlacksmithSection('configs');
 await sleep(30);
 const keys=[...d.querySelectorAll('[data-load-retry]')].map((b)=>b.getAttribute('data-keep-key'));
 expect(keys.length).toBe(1);
 expect(keys[0]).toMatch(/^load-retry:/);
 expect(d.querySelectorAll(`[data-keep-key="${keys[0]}"]`).length).toBe(1);
 // No other control in the page uses the same key shape twice.
 const all=[...d.querySelectorAll('[data-keep-key]')].map((e)=>e.getAttribute('data-keep-key'));
 expect(new Set(all).size).toBe(all.length);
});

t('a marketplace that cannot load shows the panel without the broker message',async()=>{
 const state={answer:fail(502,'Bad Gateway')};
 const {env}=await detailStart([['/marketplace',answers(state)]]);
 const {w,d}=env;
 const panel=d.createElement('div'); panel.setAttribute('data-panel','marketplace'); d.body.appendChild(panel);
 const manager=w.CFEndpointManager;
 manager.selectedEndpoint='e1';
 await manager.loadTabContent('marketplace','e1');
 const box=panel.querySelector('[data-load-error]');
 expect(box).not.toBeNull();
 expect(box.textContent).toContain('502');
 expect(box.textContent).not.toContain(SECRET);
 const before=env.calls.filter((c)=>c.includes('/marketplace')).length;
 state.answer=(u,o,j)=>j(200,{services:[]});
 box.querySelector('[data-load-retry]').click();
 await sleep(60);
 expect(env.calls.filter((c)=>c.includes('/marketplace')).length).toBe(before+1);
 expect(panel.querySelector('[data-load-error]')).toBeNull();
});

// A load that fails after its handlers are registered, then retried, must
// leave one handler per control, not two.
const lateFailure=async()=>{
 const state={failing:true};
 const status=(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',plans:{},instances:{
  'i1':{plan_id:'p1',service_id:'s1',deployment_name:'s1-p1-i1',created_at:'2026-01-01T00:00:00Z'}}});
 const catalog=(u,o,j)=>j(200,{services:[{id:'s1',name:'svc',plans:[{id:'p1',name:'small'}]}]});
 const env=boot({noDialogs:true,
  setup:(w)=>{
   // The blacksmith panel render, which comes after the services handlers
   // are registered, fails the first time it is written.
   const panel=w.document.getElementById('blacksmith');
   const proto=Object.getOwnPropertyDescriptor(w.Element.prototype,'innerHTML');
   Object.defineProperty(panel,'innerHTML',{configurable:true,get(){ return proto.get.call(this); },set(v){
    if(state.failing&&String(v).length>300&&!String(v).includes('data-load-error')){ state.failing=false; throw new Error('HTTP 500: late failure'); }
    proto.set.call(this,v);
   }});
   w.__copies=0;
   Object.defineProperty(w.navigator,'clipboard',{configurable:true,value:{writeText:()=>{ w.__copies++; return Promise.resolve(); }}});
  },
  routes:[['/v2/catalog',catalog],['/b/status',status],['/b/instance',(u,o,j)=>j(200,{deployment:'blacksmith',id:'b'})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{enabled:true})]]});
 envs.push(env);
 await sleep(120);
 return {env,state};
};

t('a load that fails late and is retried registers each handler once',async()=>{
 const {env}=await lateFailure();
 const {w,d}=env;
 const retry=d.querySelector('#blacksmith [data-load-retry]');
 expect(retry).not.toBeNull();
 retry.click();
 await sleep(150);
 expect(d.querySelector('[data-load-error]')).toBeNull();
 const copy=d.getElementById('copy-deployment-names');
 expect(copy).not.toBeNull();
 copy.click();
 await sleep(30);
 expect(w.__copies).toBe(1);
 const before=env.calls.filter((c)=>c.includes('/v2/catalog')).length;
 d.getElementById('refresh-services').click();
 await sleep(60);
 expect(env.calls.filter((c)=>c.includes('/v2/catalog')).length).toBe(before+1);
 // One change event on the service filter reaches the route notifier once.
 let notified=0;
 const notify=w.blacksmithViews.notify;
 w.blacksmithViews.notify=(...a)=>{ notified++; return notify.apply(w.blacksmithViews,a); };
 const filter=d.getElementById('service-filter');
 filter.value='s1';
 filter.dispatchEvent(new w.Event('change'));
 expect(notified).toBe(1);
 // The page announced itself once, however many loads it took.
 expect(env.errors.filter((e)=>!e.includes('late failure')&&!e.includes('Error loading data'))).toEqual([]);
});

