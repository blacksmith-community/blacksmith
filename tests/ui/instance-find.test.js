// Operators find an instance by org, space, or IP. The left list and the
// overview table share one matcher, the org and space filters narrow both,
// and the palette searches the same fields.
const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const t=JSDOM?test:test.skip;

const envs=[];
afterEach(()=>{while(envs.length){const e=envs.pop();e.w.blacksmithRouter?.destroy();e.w.close();}});

const inst=(name,extra={})=>({instance_name:name,service_id:'redis',plan_id:'small',plan:{name:'small'},...extra});
const build=(instances)=>({env:'lab',deployment:'blacksmith',plans:{},instances});
const fleet=()=>({
 'id-a':inst('cache-a',{organization_name:'payments',space_name:'prod',vm_status:'running',vm_ips:['10.0.0.5','fd00::5']}),
 'id-b':inst('cache-b',{organization_name:'payments',space_name:'staging',vm_status:'running',vm_ips:['10.0.0.55']}),
 'id-c':inst('cache-c',{organization_name:'search',space_name:'prod',vm_status:'running',vm_ips:['10.0.1.9']}),
 'id-d':inst('cache-d')});
const start=async(instances=fleet())=>{
 const routes=[['/v2/catalog',(u,o,j)=>j(200,{services:[]})],['/b/status',(u,o,j)=>j(200,build(instances))]];
 const env=boot({noDialogs:true,router:true,routes});envs.push(env);await sleep(150);return env;
};
const type=(w,box,value)=>{box.value=value;box.dispatchEvent(new w.Event('input',{bubbles:true}));};
const shown=(d)=>[...d.querySelectorAll('.instances-overview-table tbody tr')].filter(r=>r.style.display!=='none').map(r=>r.dataset.instanceId);
const listed=(d)=>[...d.querySelectorAll('#services .services-items-container .service-item')].map(i=>i.dataset.instanceId);

t('the overview shows an Org / Space column, and says Not reported when the record has none',async()=>{
 const {d}=await start();
 const heads=[...d.querySelectorAll('.instances-overview-table thead th')].map(h=>h.textContent);
 expect(heads).toEqual(['Name','Service','Plan','Org / Space','VM job state','Certificates']);
 expect(d.querySelector('tr[data-instance-id="id-a"]').cells[3].textContent).toBe('payments / prod');
 expect(d.querySelector('tr[data-instance-id="id-d"]').cells[3].textContent).toBe('Not reported');
});

t('one search finds an instance by org or space in the list and the overview',async()=>{
 const {w,d}=await start();
 const box=d.getElementById('search-instances-overview-table');
 type(w,box,'staging');
 expect(shown(d)).toEqual(['id-b']);
 expect(listed(d)).toEqual(['id-b']);
 type(w,d.getElementById('instance-search'),'search');
 expect(shown(d)).toEqual(['id-c']);
 expect(box.value).toBe('search');
});

t('an IP finds the instance that owns the VM, and only an exact address counts',async()=>{
 const {w,d}=await start();
 const box=d.getElementById('instance-search');
 type(w,box,'10.0.0.5');
 expect(listed(d)).toEqual(['id-a']);
 expect(shown(d)).toEqual(['id-a']);
 type(w,box,'FD00:0:0:0:0:0:0:5');
 expect(listed(d)).toEqual(['id-a']);
 type(w,box,'10.0.0.6');
 expect(listed(d)).toEqual([]);
});

t('an IP with no cached VM data says so rather than reporting no match',async()=>{
 const {w,d}=await start({'id-a':inst('cache-a'),'id-b':inst('cache-b')});
 type(w,d.getElementById('instance-search'),'10.0.0.5');
 expect(d.getElementById('filter-count').textContent).toContain('VM addresses are not available yet');
 expect(d.querySelector('.instances-overview .no-results-msg').textContent).toContain('VM addresses are not available yet');
 const {w:w2,d:d2}=await start();
 type(w2,d2.getElementById('instance-search'),'10.0.0.6');
 expect(d2.getElementById('filter-count').textContent).not.toContain('not available yet');
});

t('the org and space filters narrow the list and the overview table together',async()=>{
 const {w,d}=await start();
 const org=d.getElementById('org-filter'),space=d.getElementById('space-filter');
 expect([...org.options].map(o=>o.value)).toEqual(['','payments','search']);
 org.value='payments';org.dispatchEvent(new w.Event('change',{bubbles:true}));
 expect(listed(d)).toEqual(['id-a','id-b']);
 expect(shown(d)).toEqual(['id-a','id-b']);
 expect([...space.options].map(o=>o.value)).toEqual(['','prod','staging']);
 space.value='prod';space.dispatchEvent(new w.Event('change',{bubbles:true}));
 expect(listed(d)).toEqual(['id-a']);
 expect(shown(d)).toEqual(['id-a']);
 d.getElementById('clear-filters').click();
 expect(org.value).toBe('');
 expect(listed(d).length).toBe(4);
 expect(shown(d).length).toBe(4);
});

t('the selected org and space are kept in the URL like the service filter',async()=>{
 const {w,d}=await start();
 const org=d.getElementById('org-filter');
 org.value='search';org.dispatchEvent(new w.Event('change',{bubbles:true}));
 await sleep(40);
 expect(w.blacksmithViews.read('services')).toEqual({screen:'services',org:'search'});
 await w.blacksmithRouter.navigate({screen:'services',org:'payments',space:'staging'});
 await sleep(80);
 expect(listed(d)).toEqual(['id-b']);
 expect(d.getElementById('space-filter').value).toBe('staging');
});

t('routes carry the org and space beside the service and plan',()=>{
 const w=watchUncaught(new JSDOM('<body></body>',{url:'http://localhost/',runScripts:'outside-only'}).window);
 w.eval(fs.readFileSync(path.join(__dirname,'../../ui/routes.js'),'utf8'));
 const api=w.BlacksmithRoutes;
 const route={screen:'services',service:'redis',plan:'small',org:'a b',space:'prod/x'};
 const hash=api.format(route);
 expect(hash).toBe('#/instances?service=redis&plan=small&org=a%20b&space=prod%2Fx');
 expect(api.parse(hash)).toEqual(route);
 expect(api.format({screen:'services',org:'payments'})).toBe('#/instances?org=payments');
 expect(api.parse('#/instances?org=payments')).toEqual({screen:'services',org:'payments'});
 w.close();
});

t('the palette finds an instance by org, space, or exact IP',()=>{
 const w=watchUncaught(new JSDOM('<body></body>',{url:'http://localhost/',runScripts:'outside-only'}).window);
 w.eval(fs.readFileSync(path.join(__dirname,'../../ui/navigation.js'),'utf8'));
 const {destinations,search,ipLookupNote}=w.BlacksmithNavigation;
 const instances=fleet();
 const entries=destinations([],instances);
 const ids=(q)=>search(entries,q).filter(e=>e.instanceId).map(e=>e.instanceId);
 expect(ids('payments')).toEqual(['id-a','id-b']);
 expect(ids('payments staging')).toEqual(['id-b']);
 expect(ids('10.0.0.5')).toEqual(['id-a']);
 expect(ids('fd00::5')).toEqual(['id-a']);
 expect(ids('10.0.0.6')).toEqual([]);
 expect(ipLookupNote(instances,'10.0.0.6')).toBe('');
 expect(ipLookupNote({'x':inst('x')},'10.0.0.6')).toContain('VM addresses are not available yet');
 expect(ipLookupNote(instances,'payments')).toBe('');
 w.close();
});
