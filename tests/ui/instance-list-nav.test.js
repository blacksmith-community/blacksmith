// The left instance list sorts by name, keeps the operator's section when
// another instance opens, and shares one search with the overview table.
const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const envs=[];
afterEach(()=>{while(envs.length){const e=envs.pop();e.w.blacksmithRouter?.destroy();e.w.close();}});

const inst=(name,service='redis')=>({instance_name:name,service_id:service,plan_id:'small',plan:{name:'small'}});
const status={env:'lab',deployment:'blacksmith',plans:{},instances:{
 'id-b':inst('cache-b'),
 'id-c':inst('cache-c'),
 'id-pg':inst('orders-db','postgres')}};
const routes=[['/v2/catalog',(u,o,j)=>j(200,{services:[]})],['/b/status',(u,o,j)=>j(200,status)]];
const start=async()=>{const env=boot({noDialogs:true,router:true,routes});envs.push(env);await sleep(150);return env;};

t('the left list sorts by name, then ID, with numbers in numeric order',()=>{
 const {w,d}=boot();envs.push({w});
 const host=d.createElement('div');
 host.innerHTML=w.__T.renderServicesTemplate({
  z1:inst('rvm-demo-valkey','valkey'),
  z2:inst('certmgmt-10'),
  z3:inst('certmgmt-2'),
  z4:inst('certmgmt-1'),
  a9:inst('certmgmt-2'),
  a1:inst('certmgmt-2')});
 const ids=[...host.querySelectorAll('.service-item')].map(i=>i.dataset.instanceId);
 expect(ids).toEqual(['z4','a1','a9','z3','z2','z1']);
});

t('opening another instance from the list keeps the section',async()=>{
 const {w,d}=await start();
 await w.blacksmithRouter.navigate({screen:'services',instance:'id-b',section:'vms'});
 await sleep(80);
 d.querySelector('#services .service-item[data-instance-id="id-c"]').click();
 await sleep(120);
 expect(w.blacksmithRouter.current).toMatchObject({screen:'services',instance:'id-c',section:'vms'});
});

t('a sub-view does not carry over to the next instance',async()=>{
 const {w,d}=await start();
 const router=w.blacksmithRouter;
 const sent=[];
 const open={screen:'services',instance:'id-b',section:'manifest',subview:'parsed'};
 Object.defineProperty(router,'current',{get:()=>open,configurable:true});
 router.navigate=(route)=>{sent.push(route);return Promise.resolve(route);};
 await sleep(40);
 d.querySelector('#services .service-item[data-instance-id="id-c"]').click();
 expect(sent).toEqual([{screen:'services',instance:'id-c',section:'manifest'}]);
});

t('a service-specific section that the new service lacks falls back to Summary',async()=>{
 const {w,d}=await start();
 await w.blacksmithRouter.navigate({screen:'services',instance:'id-b',section:'testing'});
 await sleep(80);
 d.querySelector('#services .service-item[data-instance-id="id-pg"]').click();
 await sleep(120);
 expect(w.blacksmithRouter.current).toMatchObject({instance:'id-pg',section:'details'});
});

t('an overview row opens the summary after the operator left an instance',async()=>{
 const {w,d}=await start();
 await w.blacksmithRouter.navigate({screen:'services',instance:'id-b',section:'vms'});
 await sleep(80);
 await w.blacksmithRouter.navigate({screen:'services'});
 await sleep(80);
 d.querySelector('tr[data-instance-id="id-c"] a').click();
 await sleep(120);
 expect(w.blacksmithRouter.current).toMatchObject({instance:'id-c',section:'details'});
});

t('a service named postgres-forge falls back from Operations to Summary',async()=>{
 const {w,d}=await start();
 status.instances['id-pf']=inst('orders-forge','postgres-forge');
 try{
  w.serviceInstances['id-pf']=status.instances['id-pf'];
  const sent=[];
  Object.defineProperty(w.blacksmithRouter,'current',{get:()=>({screen:'services',instance:'id-b',section:'testing'}),configurable:true});
  w.blacksmithRouter.navigate=(r)=>{sent.push(r);return Promise.resolve(r);};
  const item=d.querySelector('#services .service-item[data-instance-id="id-c"]');
  w.serviceInstances['id-c']={...w.serviceInstances['id-c'],service_id:'postgres-forge'};
  item.click();
  expect(sent[0].section).toBe('details');
 }finally{delete status.instances['id-pf'];}
});

t('the list and the overview table share one search',async()=>{
 const {w,d}=await start();
 await w.blacksmithRouter.navigate({screen:'services'});
 await sleep(80);
 const left=d.getElementById('instance-search');
 const table=()=>d.getElementById('search-instances-overview-table');
 const shown=()=>[...d.querySelectorAll('.instances-overview-table tbody tr')].filter(r=>r.style.display!=='none').length;
 left.value='cache-b';left.dispatchEvent(new w.Event('input',{bubbles:true}));
 expect(table().value).toBe('cache-b');
 expect(shown()).toBe(1);
 expect(d.querySelectorAll('#services .service-item').length).toBe(1);
 table().value='cache';table().dispatchEvent(new w.Event('input',{bubbles:true}));
 expect(left.value).toBe('cache');
 expect(shown()).toBe(2);
 expect([...d.querySelectorAll('#services .service-item')].length).toBe(2);
});

const boxes=(d)=>({left:d.getElementById('instance-search'),table:d.getElementById('search-instances-overview-table')});
const shownRows=(d)=>[...d.querySelectorAll('.instances-overview-table tbody tr')].filter(r=>r.style.display!=='none').length;
const type=(w,box,value)=>{box.value=value;box.dispatchEvent(new w.Event('input',{bubbles:true}));};

t('searching for an instance ID matches the same instances in both views',async()=>{
 const {w,d}=await start();
 await w.blacksmithRouter.navigate({screen:'services'});
 await sleep(80);
 const {left,table}=boxes(d);
 type(w,left,'id-c');
 expect(d.querySelectorAll('#services .service-item').length).toBe(1);
 expect(shownRows(d)).toBe(1);
 type(w,table,'  id-b ');
 expect(d.querySelectorAll('#services .service-item').length).toBe(1);
 expect(shownRows(d)).toBe(1);
 expect(d.querySelector('.instances-overview-table tbody').textContent).not.toContain('id-c');
});

t('Clear filters empties both search boxes and both views',async()=>{
 const {w,d}=await start();
 await w.blacksmithRouter.navigate({screen:'services'});
 await sleep(80);
 const {left,table}=boxes(d);
 type(w,left,'cache-b');
 expect(shownRows(d)).toBe(1);
 d.getElementById('clear-filters').click();
 expect(left.value).toBe('');
 expect(table.value).toBe('');
 expect(shownRows(d)).toBe(3);
});

t('revealing an instance that the search hides empties both boxes',async()=>{
 const {w,d}=await start();
 await w.blacksmithRouter.navigate({screen:'services'});
 await sleep(80);
 const {left,table}=boxes(d);
 type(w,table,'cache-b');
 expect(left.value).toBe('cache-b');
 w.revealServiceInstance('id-c');
 expect(left.value).toBe('');
 expect(table.value).toBe('');
 expect(shownRows(d)).toBe(3);
});
