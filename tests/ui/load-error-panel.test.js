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
 // The failure replaced the half-built services pane, so none of its controls linger.
 expect(d.querySelector('#services [data-load-error]')).not.toBeNull();
 expect(d.getElementById('copy-deployment-names')).toBeNull();
 expect(d.getElementById('refresh-services')).toBeNull();
 expect(d.getElementById('service-filter')).toBeNull();
 expect(d.querySelectorAll('#services .service-item').length).toBe(0);
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

// Modals and the screens inside them take the compact panel, and each Retry
// repeats that screen's own load.
const okJson=(body)=>(u,o,j)=>j(200,body);
const configBody={id:'c1',name:'cfg',type:'cloud',team:'t',created_at:'2026-01-01T00:00:00Z',content:'a: 1',metadata:{}};
const taskBody={task:{id:5,state:'done',description:'d',user:'u',started_at:'2026-01-01T00:00:00Z',ended_at:'2026-01-01T00:01:00Z',deployment:'dep'},events:[]};
const callsTo=(env,part)=>env.calls.filter((c)=>c.includes(part)).length;

t('config details that cannot load show a compact panel and Retry loads them again',async()=>{
 const state={answer:fail(503,'Service Unavailable')};
 const {env}=await detailStart([['/b/configs/c1',answers(state)]]);
 const {w,d}=env;
 await w.showConfigDetails('c1');
 const box=d.querySelector('#config-details-loading [data-load-error]');
 expect(box).not.toBeNull();
 expect(box.classList.contains('load-error--compact')).toBe(true);
 expect(box.getAttribute('role')).toBe('alert');
 expect(box.textContent).toContain('503');
 expect(d.getElementById('config-details-loading').textContent).not.toContain('Failed to load');
 expect(d.getElementById('config-details-loading').textContent).not.toContain(SECRET);
 const before=callsTo(env,'/b/configs/c1');
 state.answer=okJson(configBody);
 box.querySelector('[data-load-retry]').click();
 await sleep(80);
 expect(callsTo(env,'/b/configs/c1')).toBe(before+1);
 expect(d.querySelector('#config-details-loading [data-load-error]')).toBeNull();
 expect(d.getElementById('config-name').textContent).toBe('cfg');
});

t('config versions that cannot load show a compact panel and Retry loads them again',async()=>{
 const state={answer:fail(502,'Bad Gateway')};
 const {env}=await detailStart([['/versions',answers(state)]]);
 const {w,d}=env;
 await w.showConfigVersions('cloud','cfg','c1');
 const box=d.querySelector('#config-details-loading [data-load-error]');
 expect(box).not.toBeNull();
 expect(box.classList.contains('load-error--compact')).toBe(true);
 expect(box.textContent).toContain('502');
 expect(box.textContent).not.toContain(SECRET);
 const before=callsTo(env,'/versions');
 state.answer=okJson({configs:[]});
 box.querySelector('[data-load-retry]').click();
 await sleep(80);
 expect(callsTo(env,'/versions')).toBe(before+1);
 expect(d.querySelector('#config-details-loading [data-load-error]')).toBeNull();
});

t('task details that cannot load show a compact panel and Retry loads them again',async()=>{
 const state={answer:fail(503,'Service Unavailable')};
 const {env}=await detailStart([['/b/tasks/5/output',okJson({output:''})],['/b/tasks/5',answers(state)]]);
 const {w,d}=env;
 await w.showTaskDetails(5);
 await sleep(60);
 const box=d.querySelector('#task-details-loading [data-load-error]');
 expect(box).not.toBeNull();
 expect(box.classList.contains('load-error--compact')).toBe(true);
 expect(box.textContent).toContain('503');
 expect(box.textContent).not.toContain(SECRET);
 expect(d.getElementById('task-details-loading').textContent).not.toContain('Failed to load');
 const before=callsTo(env,'/b/tasks/5');
 state.answer=okJson(taskBody);
 box.querySelector('[data-load-retry]').click();
 await sleep(100);
 expect(callsTo(env,'/b/tasks/5')).toBeGreaterThan(before);
 expect(d.querySelector('#task-details-loading [data-load-error]')).toBeNull();
});

t('a task output tab that cannot load shows a compact panel and Retry loads it again',async()=>{
 const out={answer:fail(500,'Internal Server Error')};
 const {env}=await detailStart([['/b/tasks/5/output',answers(out)],['/b/tasks/5',okJson(taskBody)]]);
 const {w,d}=env;
 await w.showTaskDetails(5);
 await sleep(100);
 const pane=d.getElementById('task-task-content');
 const box=pane.querySelector('[data-load-error]');
 expect(box).not.toBeNull();
 expect(box.classList.contains('load-error--compact')).toBe(true);
 expect(box.textContent).toContain('500');
 expect(box.textContent).not.toContain(SECRET);
 const before=callsTo(env,'/b/tasks/5/output');
 out.answer=okJson({output:'line'});
 box.querySelector('[data-load-retry]').click();
 await sleep(80);
 expect(callsTo(env,'/b/tasks/5/output')).toBe(before+1);
 expect(pane.querySelector('[data-load-error]')).toBeNull();
});

t('a service list that cannot load shows the panel and Retry loads it again',async()=>{
 const state={answer:fail(502,'Bad Gateway')};
 const {env}=await detailStart([['/spaces/sp1/services',answers(state)]]);
 const {w,d}=env;
 const host=d.createElement('div');
 host.innerHTML='<select id="org-selector"><option value="o1" selected>o</option></select><select id="space-selector"><option value="sp1" selected>s</option></select><div id="services-results"></div>';
 d.body.appendChild(host);
 w.CFEndpointManager.selectedEndpoint='e1';
 await w.CFEndpointManager.loadServiceInstances('e1','o1','sp1');
 const box=d.querySelector('#services-results [data-load-error]');
 expect(box).not.toBeNull();
 expect(box.textContent).toContain('502');
 expect(box.textContent).not.toContain(SECRET);
 const before=callsTo(env,'/spaces/sp1/services');
 state.answer=okJson({services:[]});
 box.querySelector('[data-load-retry]').click();
 await sleep(80);
 expect(callsTo(env,'/spaces/sp1/services')).toBe(before+1);
 expect(d.querySelector('#services-results [data-load-error]')).toBeNull();
});

t('older logs that cannot load say what happened without the broker body',async()=>{
 const {env}=await detailStart([['/b/blacksmith/logs',fail(503,'Service Unavailable')]]);
 const {w,d}=env;
 const ws=d.querySelector('#blacksmith .entity-workspace')||(()=>{ const x=d.createElement('div'); x.className='entity-workspace'; d.getElementById('blacksmith').appendChild(x); return x; })();
 ws.dataset.section='blacksmith-logs';
 d.body.insertAdjacentHTML('beforeend','<div id="blacksmith-logs-display"></div><button id="logs-load-more"></button>');
 w.blacksmithLogsPagination={hasMore:true,isLoading:false,currentFile:'f',currentOffset:0,totalLines:10};
 await w.loadMoreBlacksmithLogs();
 const more=d.getElementById('logs-load-more');
 expect(more.textContent).toContain('503 Service Unavailable');
 expect(more.textContent).toContain('Click to retry');
 expect(more.textContent).not.toContain(SECRET);
 expect(more.textContent).not.toContain('HTTP 503:');
});

t('every compact panel Retry keeps its own keep key',async()=>{
 const {env}=await detailStart([['/b/configs/c1',fail(503,'Service Unavailable')],['/b/tasks/5',fail(503,'Service Unavailable')]]);
 const {w,d}=env;
 await w.showConfigDetails('c1');
 await w.showTaskDetails(5);
 await sleep(60);
 const keys=[...d.querySelectorAll('[data-load-retry]')].map((b)=>b.getAttribute('data-keep-key'));
 expect(keys.length).toBe(2);
 expect(new Set(keys).size).toBe(2);
});

// Round 3. "Could not be reached" is only for a request that never got an answer.
t('a render error that is a TypeError does not say the broker could not be reached',async()=>{
 const {env}=await detailStart([['/b/inst1/manifest',()=>Promise.resolve({ok:true,status:200,statusText:'OK',json:()=>Promise.reject(new TypeError('x is not a function'))})]]);
 const {w,d}=env;
 const host=d.createElement('div'); host.className='detail-content'; d.getElementById('services').appendChild(host);
 w.currentInstanceInfo={id:'inst1'};
 await w.loadInstanceSection('inst1','manifest');
 await sleep(30);
 const panel=host.querySelector('[data-load-error]');
 expect(panel).not.toBeNull();
 expect(panel.textContent).not.toContain('could not be reached');
 expect(panel.textContent).toContain('could not read');
});

t('a request that never got an answer says the broker could not be reached',async()=>{
 const {env}=await detailStart([['/b/inst1/vms',()=>Promise.reject(new TypeError('Failed to fetch'))]]);
 const {w,d}=env;
 const host=d.createElement('div'); host.className='detail-content'; d.getElementById('services').appendChild(host);
 w.currentInstanceInfo={id:'inst1'};
 await w.loadInstanceSection('inst1','vms');
 await sleep(30);
 expect(host.querySelector('[data-load-error]').textContent).toContain('could not be reached');
});

// Retry presses that race, or that come after the operator has moved on.
t('a double-clicked first-load Retry starts one load',async()=>{
 const {env,state}=await start(fail(503,'Service Unavailable'));
 const {w,d}=env;
 const button=d.querySelector('#blacksmith [data-load-retry]');
 const other=d.querySelector('#services [data-load-retry]');
 expect(state.calls).toBe(1);
 state.catalog=okCatalog;
 for(const target of [button,button,other]) target.dispatchEvent(new w.MouseEvent('click',{bubbles:true,cancelable:true}));
 await sleep(120);
 expect(state.calls).toBe(2);
});

t('a double-clicked detail Retry reloads the view once',async()=>{
 const state={answer:fail(503,'Service Unavailable')};
 const {env}=await detailStart([['/b/configs',answers(state)]]);
 const {w,d}=env;
 await w.loadBlacksmithSection('configs');
 await sleep(30);
 const button=d.querySelector('#blacksmith .detail-content [data-load-retry]');
 const before=callsTo(env,'/b/configs');
 state.answer=(u,o,j)=>j(200,[]);
 for(let i=0;i<2;i++) button.dispatchEvent(new w.MouseEvent('click',{bubbles:true,cancelable:true}));
 await sleep(100);
 expect(callsTo(env,'/b/configs')).toBe(before+1);
});

t('Retry after the operator left the instance does nothing',async()=>{
 const state={answer:fail(502,'Bad Gateway')};
 const {env}=await detailStart([['/b/inst1/vms',answers(state)]]);
 const {w,d}=env;
 const host=d.createElement('div'); host.className='detail-content'; d.getElementById('services').appendChild(host);
 w.currentInstanceInfo={id:'inst1'};
 await w.loadInstanceSection('inst1','vms');
 await sleep(30);
 const button=host.querySelector('[data-load-retry]');
 const before=callsTo(env,'/b/inst1/vms');
 w.currentInstanceInfo={id:'inst2'};
 state.answer=okJson([]);
 button.click();
 await sleep(60);
 expect(callsTo(env,'/b/inst1/vms')).toBe(before);
 expect(host.querySelector('[data-load-error]')).not.toBeNull();
});

t('Retry after the operator opened another broker section does nothing',async()=>{
 const state={answer:fail(503,'Service Unavailable')};
 const {env}=await detailStart([['/b/configs',answers(state)]]);
 const {w,d}=env;
 await w.loadBlacksmithSection('configs');
 await sleep(30);
 const workspace=d.querySelector('#blacksmith .entity-workspace');
 expect(workspace).not.toBeNull();
 const button=d.querySelector('#blacksmith .detail-content [data-load-retry]');
 const before=callsTo(env,'/b/configs');
 workspace.dataset.section='tasks';
 state.answer=(u,o,j)=>j(200,[]);
 button.click();
 await sleep(60);
 expect(callsTo(env,'/b/configs')).toBe(before);
});

t('Retry after the operator picked another endpoint does nothing',async()=>{
 const state={answer:fail(502,'Bad Gateway')};
 const {env}=await detailStart([['/marketplace',answers(state)]]);
 const {w,d}=env;
 const panel=d.createElement('div'); panel.setAttribute('data-panel','marketplace'); d.body.appendChild(panel);
 w.CFEndpointManager.selectedEndpoint='e1';
 await w.CFEndpointManager.loadTabContent('marketplace','e1');
 const button=panel.querySelector('[data-load-retry]');
 const before=callsTo(env,'/marketplace');
 w.CFEndpointManager.selectedEndpoint='e2';
 state.answer=okJson({services:[]});
 button.click();
 await sleep(60);
 expect(callsTo(env,'/marketplace')).toBe(before);
});

t('a handler is dropped when its panel is gone and kept while the panel is on the page',async()=>{
 const state={answer:fail(503,'Service Unavailable')};
 const {env}=await detailStart([['/b/configs',answers(state)]]);
 const {w,d}=env;
 await w.loadBlacksmithSection('configs');
 await sleep(30);
 const saved=d.querySelector('#blacksmith .detail-content [data-load-error]').outerHTML;
 // An unrelated change to the page keeps the handler of a panel that is still shown.
 d.body.appendChild(d.createElement('div'));
 await sleep(30);
 const before=callsTo(env,'/b/configs');
 state.answer=(u,o,j)=>j(200,[]);
 d.querySelector('#blacksmith .detail-content [data-load-retry]').click();
 await sleep(80);
 expect(callsTo(env,'/b/configs')).toBe(before+1);
 // The panel was replaced, so a copy of it that comes back has nothing to run.
 await sleep(30);
 const holder=d.createElement('div'); holder.innerHTML=saved; d.body.appendChild(holder);
 holder.querySelector('[data-load-retry]').click();
 await sleep(60);
 expect(callsTo(env,'/b/configs')).toBe(before+1);
});
