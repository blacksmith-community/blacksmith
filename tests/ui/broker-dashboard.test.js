const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const fs=require('fs'),path=require('path');
const t=JSDOM?test:test.skip;

// Every env a test boots, checked once the test ends so a dialog call that the
// UI code swallows still fails it.
const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); expect(env.alerts).toEqual([]); } });
const start=(opts)=>{ const env=boot({noDialogs:true,...opts}); envs.push(env); return env; };

const plan=(service,id,extra={})=>({id,name:id,description:`${id} plan`,limit:0,type:'service',service:{id:service,name:service},...extra});
const inst=(service,planId,extra={})=>({service_id:service,plan_id:planId,plan:{id:planId,name:planId},...extra});
const fixture=()=>({
 deployment:'blacksmith',env:'lab',
 plans:{
  'valkey/solo':plan('valkey','solo'),
  'redis/small':plan('redis','small',{description:'One small node',limit:5}),
  'valkey/cluster':plan('valkey','cluster',{description:'Three node cluster'}),
  'redis/large':plan('redis','large'),
 },
 instances:{
  r1:inst('redis','small',{vm_status:'running'}),
  r2:inst('redis','small',{vm_status:'failing'}),
  r3:inst('redis','small',{deleted:true,vm_status:'failing'}),
  r4:inst('redis','large'),
  r5:inst('redis','gone'),
  v1:inst('valkey','cluster',{vm_status:'unresponsive agent'}),
  g1:inst('legacy','old',{vm_status:'running'}),
 }
});
const render=(w,d,data)=>{ const host=d.createElement('div'); d.body.appendChild(host); host.innerHTML=w.__T.renderBlacksmithOverview(data); return host; };
const rowText=(row)=>[...row.children].map(c=>c.textContent.replace(/\s+/g,' ').trim());
const rows=(host)=>[...host.querySelectorAll('.entity-services tbody tr')];

t('the panel lists every type and plan, sorted, with active and alert counts that add up',()=>{
 const {w,d}=start();try{
  const host=render(w,d,fixture());
  const tiles=[...host.querySelectorAll('.entity-metric strong')].map(x=>x.textContent);
  expect(tiles).toEqual(['6','4','2']);
  const names=rows(host).map(r=>[r.className.includes('entity-plan-row')?'plan':'type',r.querySelector('th').textContent.replace(/\s+/g,' ').trim()]);
  expect(names).toEqual([
   ['type','legacy Not in the catalog'],['plan','old Not in the catalog'],
   ['type','redis'],['plan','gone Not in the catalog'],['plan','large'],['plan','small'],
   ['type','valkey'],['plan','cluster'],['plan','solo']]);
  const counts=Object.fromEntries(rows(host).map(r=>[r.className.includes('entity-plan-row')?r.dataset.service+'/'+r.dataset.plan:r.dataset.service,[r.querySelector('[data-count="instances"]').textContent.trim(),r.querySelector('[data-count="alerts"]').textContent.trim()]]));
  expect(counts).toEqual({
   legacy:['1 instance','None'],'legacy/old':['1 instance','None'],
   redis:['4 instances','1 with VM alerts'],'redis/gone':['1 instance','None'],'redis/large':['1 instance','None'],'redis/small':['2 instances','1 with VM alerts'],
   valkey:['1 instance','1 with VM alerts'],'valkey/cluster':['1 instance','1 with VM alerts'],'valkey/solo':['0 instances','None']});
  const typeTotal=rows(host).filter(r=>!r.className.includes('entity-plan-row')).reduce((n,r)=>n+Number(r.querySelector('[data-count="instances"]').textContent.trim().split(' ')[0]),0);
  expect(typeTotal).toBe(6);
  const planTotal=rows(host).filter(r=>r.className.includes('entity-plan-row')).reduce((n,r)=>n+Number(r.querySelector('[data-count="instances"]').textContent.trim().split(' ')[0]),0);
  expect(planTotal).toBe(6);
  const alertTotal=rows(host).filter(r=>r.className.includes('entity-plan-row')).reduce((n,r)=>n+(/^(\d+)/.exec(r.querySelector('[data-count="alerts"]').textContent.trim())||[0,0])[1]*1,0);
  expect(alertTotal).toBe(2);
 }finally{w.close();}
});

t('plan rows show the description and any limit',()=>{
 const {w,d}=start();try{
  const host=render(w,d,fixture());
  const small=rows(host).find(r=>r.dataset.plan==='small');
  expect(small.textContent).toContain('One small node');
  expect(small.textContent).toContain('Limit 5');
  const large=rows(host).find(r=>r.dataset.plan==='large');
  expect(large.textContent).not.toContain('Limit');
  const old=rows(host).find(r=>r.dataset.plan==='old');
  expect(old.textContent).toContain('Not in the catalog');
 }finally{w.close();}
});

t('the panel has a reachable heading and a captioned table with column headers',()=>{
 const {w,d}=start();try{
  const host=render(w,d,fixture());
  const panel=host.querySelector('.entity-services');
  const heading=panel.querySelector('h3');
  expect(heading.textContent).toBe('Services and plans');
  expect(panel.getAttribute('aria-labelledby')).toBe(heading.id);
  expect(panel.querySelector('table caption').textContent.length).toBeGreaterThan(0);
  expect([...panel.querySelectorAll('thead th')].every(th=>th.scope==='col')).toBe(true);
  expect([...panel.querySelectorAll('tbody th')].every(th=>th.scope==='row')).toBe(true);
  expect(host.textContent).toContain('VM exceptions reflect reported status');
  expect(!!host.querySelector('.entity-context')).toBe(true);
  expect(!!host.querySelector('details.entity-technical')).toBe(true);
 }finally{w.close();}
});

t('each entry is a button whose name says where it goes',()=>{
 const {w,d}=start();try{
  const host=render(w,d,fixture());
  const label=(sel)=>host.querySelector(sel).getAttribute('aria-label');
  expect(label('button[data-service="redis"]:not([data-plan])')).toBe('Show the 4 redis instances');
  expect(label('button[data-service="redis"][data-plan="small"]')).toBe('Show the 2 redis instances on plan small');
  expect(label('button[data-service="redis"][data-plan="large"]')).toBe('Show the 1 redis instance on plan large');
  expect(label('button[data-service="valkey"][data-plan="solo"]')).toBe('Show the 0 valkey instances on plan solo');
  for(const b of host.querySelectorAll('.entity-services button')){ expect(b.type).toBe('button'); expect(b.hasAttribute('data-services-filter')).toBe(true); }
  // A type missing from the catalog is a choice in the Services filter, so it links too.
  expect(host.querySelector('button[data-service="legacy"]:not([data-plan])').getAttribute('aria-label')).toBe('Show the 1 legacy instance');
  expect(host.querySelector('button[data-service="legacy"][data-plan="old"]').getAttribute('aria-label')).toBe('Show the 1 legacy instance on plan old');
  // The catalog type with a plan that has left the catalog can still be filtered.
  expect(!!host.querySelector('button[data-service="redis"][data-plan="gone"]')).toBe(true);
 }finally{w.close();}
});

t('clicking a type or a plan opens the Services view with that filter through the router',async()=>{
 const {w,d}=start();try{
  const calls=[];
  w.blacksmithRouter={navigate:async(r)=>{calls.push(r);return true;}};
  const host=render(w,d,fixture());
  host.querySelector('button[data-service="redis"]:not([data-plan])').click();await sleep(5);
  host.querySelector('button[data-service="redis"][data-plan="small"]').click();await sleep(5);
  expect(calls).toEqual([{screen:'services',service:'redis'},{screen:'services',service:'redis',plan:'small'}]);
  // The Service instances tile keeps its unfiltered jump.
  host.querySelector('.entity-metric[data-entity-section="instances"]').click();await sleep(5);
  expect(calls[2]).toEqual({screen:'services'});
 }finally{w.close();}
});

t('the panel says so when the broker has no plans or instances',()=>{
 const {w,d}=start();try{
  const host=render(w,d,{deployment:'blacksmith',env:'lab',plans:{},instances:{}});
  const panel=host.querySelector('.entity-services');
  expect(panel.querySelector('h3').textContent).toBe('Services and plans');
  expect(panel.textContent).toContain('The broker has no service plans or instances yet.');
  expect(!!panel.querySelector('table')).toBe(false);
  expect(host.querySelectorAll('.entity-metric')).toHaveLength(3);
  const noInstances=render(w,d,{plans:{'redis/small':plan('redis','small')},instances:{}});
  expect(rows(noInstances)).toHaveLength(2);
  expect(noInstances.querySelector('.entity-services').textContent).not.toContain('no service plans');
 }finally{w.close();}
});

t('catalog text is escaped',()=>{
 const {w,d}=start();try{
  const evil='<img src=x onerror=alert(1)>';
  const host=render(w,d,{plans:{'a/b':plan('a','b',{description:evil,service:{id:'a',name:evil}})},instances:{i:inst('a','b')}});
  expect(host.querySelector('img')).toBeNull();
  expect(host.textContent).toContain(evil);
 }finally{w.close();}
});

t('routes carry the Services filter and reject a plan without a service',()=>{
 const w=watchUncaught(new JSDOM('<body></body>',{url:'http://localhost/',runScripts:'outside-only'}).window);
 w.eval(fs.readFileSync(path.join(__dirname,'../../ui/routes.js'),'utf8'));
 const api=w.BlacksmithRoutes;
 expect(api.format({screen:'services'})).toBe('#/instances');
 expect(api.format({screen:'services',service:'redis'})).toBe('#/instances?service=redis');
 const route={screen:'services',service:'a b/c',plan:'small & big'};
 const hash=api.format(route);
 expect(hash).toBe('#/instances?service=a%20b%2Fc&plan=small%20%26%20big');
 expect(api.parse(hash)).toEqual(route);
 expect(api.parse('#/instances?service=redis')).toEqual({screen:'services',service:'redis'});
 expect(api.parse('#/instances?plan=small')).toEqual({screen:'services'});
 expect(api.parse('#/instances/abc/logs?service=redis')).toEqual({screen:'services',instance:'abc',section:'instance-logs'});
 w.close();
});

const catalog={services:[{id:'redis',name:'redis',plans:[{id:'small',name:'small'},{id:'large',name:'large'}]},{id:'valkey',name:'valkey',plans:[{id:'cluster',name:'cluster'}]}]};
const status=()=>{ const f=fixture(); return {env:'lab',deployment:'blacksmith',instances:f.instances,plans:f.plans}; };
const loaded=async()=>{
 const env=start({routes:[['/v2/catalog',(u,o,j)=>j(200,JSON.parse(JSON.stringify(catalog)))],['/b/status',(u,o,j)=>j(200,status())]]});
 await sleep(80);return env;
};
const visible=(d)=>[...d.querySelectorAll('#services .service-item')].map(x=>x.dataset.instanceId).sort();

t('activating a Services route applies the type and plan filter and clearing the route clears it',async()=>{
 const env=await loaded(),{w,d}=env;try{
  expect(typeof w.blacksmithViews?.activate).toBe('function');
  // Deleted r3 is hidden by default, so a plan route shows the active ones only.
  await w.blacksmithViews.activate({screen:'services',service:'redis',plan:'small'},{});
  expect(d.getElementById('service-filter').value).toBe('redis');
  expect(d.getElementById('plan-filter').value).toBe('small');
  expect(d.getElementById('plan-filter').disabled).toBe(false);
  expect(visible(d)).toEqual(['r1','r2']);
  expect(w.blacksmithViews.read('services')).toEqual({screen:'services',service:'redis',plan:'small'});
  await w.blacksmithViews.activate({screen:'services',service:'redis'},{});
  expect(d.getElementById('plan-filter').value).toBe('');
  expect(visible(d)).toEqual(['r1','r2','r4','r5']);
  await w.blacksmithViews.activate({screen:'services'},{});
  expect(d.getElementById('service-filter').value).toBe('');
  expect(visible(d)).toEqual(['g1','r1','r2','r4','r5','v1']);
 }finally{w.close();}
});

t('a Services route for an unknown type or plan is refused rather than showing everything',async()=>{
 const env=await loaded(),{w}=env;try{
  await expect(w.blacksmithViews.activate({screen:'services',service:'nope'},{})).rejects.toThrow('This service type is no longer available.');
  await expect(w.blacksmithViews.activate({screen:'services',service:'redis',plan:'nope'},{})).rejects.toThrow('This service plan is no longer available.');
 }finally{w.close();}
});

t('the overview in the loaded page links through the real router to the filtered list',async()=>{
 const env=await loaded(),{w,d}=env;try{
  w.eval(fs.readFileSync(path.join(__dirname,'../../ui/routes.js'),'utf8'));
  await sleep(60);
  expect(w.blacksmithRouter.current.screen).toBe('blacksmith');
  const button=d.querySelector('#blacksmith button[data-service="redis"][data-plan="small"]');
  expect(!!button).toBe(true);
  button.click();await sleep(60);
  expect(w.location.hash).toBe('#/instances?service=redis&plan=small');
  expect(visible(d)).toEqual(['r1','r2']);
 }finally{w.close();}
});

t('a plan with no instances and a plan gone from the catalog both open an empty or filled list',async()=>{
 const env=await loaded(),{w,d}=env;try{
  await w.blacksmithViews.activate({screen:'services',service:'valkey',plan:'cluster'},{});
  expect(visible(d)).toEqual(['v1']);
  await w.blacksmithViews.activate({screen:'services',service:'redis',plan:'gone'},{});
  expect(visible(d)).toEqual(['r5']);
  expect(d.getElementById('service-filter').value).toBe('redis');
 }finally{w.close();}
});

const withRouter=async(env)=>{
 const {w}=env;
 w.eval(fs.readFileSync(path.join(__dirname,'../../ui/routes.js'),'utf8'));
 await sleep(60);
 return w.blacksmithRouter;
};
const choose=(w,select,value)=>{ select.value=value; select.dispatchEvent(new w.Event('change',{bubbles:true})); };

t('choosing a filter by hand updates the address without adding history entries',async()=>{
 const env=await loaded(),{w,d}=env;try{
  const router=await withRouter(env);
  await router.navigate({screen:'services'});
  const length=w.history.length;
  choose(w,d.getElementById('service-filter'),'redis');await sleep(10);
  expect(w.location.hash).toBe('#/instances?service=redis');
  choose(w,d.getElementById('plan-filter'),'small');await sleep(10);
  expect(w.location.hash).toBe('#/instances?service=redis&plan=small');
  choose(w,d.getElementById('service-filter'),'');await sleep(10);
  expect(w.location.hash).toBe('#/instances');
  expect(w.history.length).toBe(length);
 }finally{w.close();}
});

t('opening the page at a filtered address applies the filter',async()=>{
 const env=await loaded(),{w,d}=env;try{
  w.history.replaceState(null,'','#/instances?service=redis&plan=small');
  await withRouter(env);
  expect(d.getElementById('service-filter').value).toBe('redis');
  expect(d.getElementById('plan-filter').value).toBe('small');
  expect(visible(d)).toEqual(['r1','r2']);
  expect(w.location.hash).toBe('#/instances?service=redis&plan=small');
 }finally{w.close();}
});

t('opening the page at an address with an unknown service or plan falls back with the list unfiltered',async()=>{
 for(const hash of ['#/instances?service=nope','#/instances?service=redis&plan=nope']){
  const env=await loaded(),{w,d}=env;try{
   w.history.replaceState(null,'',hash);
   await withRouter(env);
   expect(d.getElementById('service-filter').value).toBe('');
   expect(d.getElementById('plan-filter').value).toBe('');
   expect(w.location.hash).toBe('#/broker/summary');
   expect(visible(d)).toEqual(['g1','r1','r2','r4','r5','v1']);
  }finally{w.close();}
 }
});

t('a type and plan missing from the catalog are marked in the filters and filter the list',async()=>{
 const env=await loaded(),{w,d}=env;try{
  const service=[...d.getElementById('service-filter').options].find(o=>o.value==='legacy');
  expect(service.textContent).toBe('legacy (not in the catalog)');
  expect([...d.getElementById('service-filter').options].find(o=>o.value==='redis').textContent).toBe('redis');
  await w.blacksmithViews.activate({screen:'services',service:'legacy',plan:'old'},{});
  const plan=[...d.getElementById('plan-filter').options].find(o=>o.value==='old');
  expect(plan.textContent).toBe('old (not in the catalog)');
  expect(visible(d)).toEqual(['g1']);
  await w.blacksmithViews.activate({screen:'services',service:'redis'},{});
  expect([...d.getElementById('plan-filter').options].find(o=>o.value==='gone').textContent).toBe('gone (not in the catalog)');
  expect([...d.getElementById('plan-filter').options].find(o=>o.value==='small').textContent).toBe('small');
 }finally{w.close();}
});
