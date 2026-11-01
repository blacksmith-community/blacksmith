const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');

const instances=()=>({
 a1:{service_id:'redis',plan_id:'small',plan:{id:'small',name:'small'},instance_name:'cache-a',vm_status:'running'},
 b2:{service_id:'redis',plan_id:'large',plan:{id:'large',name:'large'},vm_status:'failing'},
 c3:{service_id:'valkey',plan_id:'solo',plan:{id:'solo',name:'solo'},instance_name:'kv-c'},
 d4:{service_id:'redis',plan_id:'small',plan:{id:'small',name:'small'},instance_name:'gone',deleted:true}
});
const catalog={services:[{id:'redis',name:'Redis',plans:[{id:'small',name:'small'},{id:'large',name:'large'}]},{id:'valkey',name:'Valkey',plans:[{id:'solo',name:'solo'}]}]};
const mount=(w,d)=>{
 w.plansData=catalog;
 const host=d.createElement('div'); host.id='services'; d.body.appendChild(host);
 host.innerHTML=w.__T.renderServicesTemplate(instances());
 return host;
};
const cells=(row)=>[...row.children].map(c=>c.textContent.replace(/\s+/g,' ').trim());
const scanned={state:'completed',completed_at:'2026-10-30T10:00:00Z',errors:[]};
const inventory=(certs,status=200,scan=scanned)=>['/b/certificates/inventory?attention=true',(u,o,json)=>json(status,status===200?{certificates:certs,scan,summary:{certificates:certs.length}}:{error:'off'})];
const owned=(...ids)=>({owners:ids.map(id=>({kind:'instance',instance_id:id}))});

test('with no instance open the detail pane lists the instances in a searchable table',()=>{
 const {w,d}=boot();try{
  const host=mount(w,d);
  const pane=host.querySelector('.service-detail');
  expect(pane.querySelector('.no-selection')).toBeNull();
  expect(pane.textContent).not.toContain('Select a service instance');
  const table=pane.querySelector('table.instances-overview-table');
  expect([...table.querySelectorAll('thead th')].map(th=>th.textContent)).toEqual(['Name','Service','Plan','VM job state','Certificates']);
  expect([...table.querySelectorAll('tbody tr')].map(cells).map(r=>r.slice(0,4))).toEqual([
   ['cache-a','Redis','small','running'],
   ['b2','Redis','large','failing'],
   ['kv-c','Valkey','solo','—']]);
  expect(pane.querySelector('.table-controls-container .table-search-input')).not.toBeNull();
  expect(pane.querySelector('.table-toolbar-actions .copy-btn-logs')).not.toBeNull();
  expect(host.querySelectorAll('.services-list .service-item').length).toBe(4);
 }finally{w.close();}
});

test('a row opens its instance the way the card does',()=>{
 const {w,d}=boot();try{
  const host=mount(w,d);
  const routed=[];
  w.blacksmithRouter={navigate:(route)=>{routed.push(route);return Promise.resolve(true);}};
  const rows=[...host.querySelectorAll('.instances-overview-table tbody tr')];
  expect(rows.every(r=>!r.hasAttribute('tabindex'))).toBe(true);
  expect(rows.map(r=>r.querySelector('td a').getAttribute('href'))).toEqual(['#/instances/a1/details','#/instances/b2/details','#/instances/c3/details']);
  rows[1].querySelector('td').dispatchEvent(new w.MouseEvent('click',{bubbles:true}));
  rows[0].querySelector('a').dispatchEvent(new w.MouseEvent('click',{bubbles:true,cancelable:true}));
  expect(routed).toEqual([{screen:'services',instance:'b2',section:'details'}]);
 }finally{w.close();}
});

test('the Certificates column counts attention certificates from one request',async()=>{
 const {w,d,calls}=boot({routes:[inventory([
  {id:'x1',...owned('a1')},{id:'x2',...owned('a1','c3')},{id:'x3',...owned('a1')},{id:'x4',owners:[{kind:'broker'}]}])]});
 try{
  const host=mount(w,d);
  const column=()=>[...host.querySelectorAll('.instances-overview-table tbody tr')].map(r=>r.lastElementChild.textContent.trim());
  expect(column()).toEqual(['Unknown','Unknown','Unknown']);
  await w.__T.fillInstanceCertificates();
  expect(column()).toEqual(['3','0','1']);
  expect(calls.filter(c=>c==='GET /b/certificates/inventory?attention=true').length).toBe(1);
  const sorts=[...host.querySelectorAll('.instances-overview-table tbody tr')].map(r=>r.lastElementChild.dataset.sortValue);
  expect(sorts).toEqual(['3','0','1']);
 }finally{w.close();}
});

test('a failed certificates request says Unknown and never zero',async()=>{
 const {w,d}=boot({routes:[inventory([],403)]});
 try{
  const host=mount(w,d);
  await w.__T.fillInstanceCertificates();
  const column=[...host.querySelectorAll('.instances-overview-table tbody tr')].map(r=>r.lastElementChild.textContent.trim());
  expect(column).toEqual(['Unknown','Unknown','Unknown']);
 }finally{w.close();}
});

test('an instance the scan could not read is Unknown, not zero',async()=>{
 const scan={...scanned,errors:[{instance_id:'b2',error:'unreachable'},{instance_id:'c3',error:'timeout',last_success_at:'2026-10-29T10:00:00Z'}]};
 const {w,d}=boot({routes:[inventory([{id:'x1',...owned('a1')}],200,scan)]});
 try{
  const host=mount(w,d);
  await w.__T.fillInstanceCertificates();
  const column=[...host.querySelectorAll('.instances-overview-table tbody tr')].map(r=>r.lastElementChild.textContent.trim());
  expect(column).toEqual(['1','Unknown','0']);
 }finally{w.close();}
});

test('a platform-owned certificate is not counted against an instance',async()=>{
 const {w,d}=boot({routes:[inventory([{id:'p1',owners:[{kind:'platform',instance_name:'cache-a'}]},{id:'x1',...owned('a1')}])]});
 try{
  const host=mount(w,d);
  await w.__T.fillInstanceCertificates();
  const column=[...host.querySelectorAll('.instances-overview-table tbody tr')].map(r=>r.lastElementChild.textContent.trim());
  expect(column).toEqual(['1','0','0']);
 }finally{w.close();}
});

test('a first scan that never completed leaves every count Unknown',async()=>{
 for(const scan of [{state:'never'},{state:'failed',completed_at:'2026-10-30T10:00:00Z'}]){
  const {w,d}=boot({routes:[inventory([],200,scan)]});
  try{
   const host=mount(w,d);
   await w.__T.fillInstanceCertificates();
   const column=[...host.querySelectorAll('.instances-overview-table tbody tr')].map(r=>r.lastElementChild.textContent.trim());
   expect(column).toEqual(['Unknown','Unknown','Unknown']);
  }finally{w.close();}
 }
});

test('instance names and services are escaped in the table',()=>{
 const {w,d}=boot();try{
  const evil='"><img src=x onerror=alert(1)>';
  w.plansData=null;
  const host=d.createElement('div'); d.body.appendChild(host);
  host.innerHTML=w.__T.renderServicesTemplate({e1:{service_id:evil,plan:{name:evil},instance_name:evil,vm_status:evil}});
  expect(host.querySelector('.instances-overview-table img')).toBeNull();
  expect(host.querySelector('.instances-overview-table tbody td').textContent).toBe(evil);
 }finally{w.close();}
});

test('All instances brings the overview back after an instance was open',async()=>{
 const {w,d,calls}=boot({noDialogs:true,routes:[
  ['/b/certificates/inventory',(u,o,json)=>json(200,{certificates:[],scan:scanned,summary:{certificates:0}})],
  ['/v2/catalog',(u,o,json)=>json(200,catalog)],
  ['/b/status',(u,o,json)=>json(200,{env:'lab',deployment:'blacksmith',plans:{},instances:instances()})]]});
 try{
  await sleep(150);
  await w.blacksmithViews.activate({screen:'services'},{});
  const table=()=>d.querySelectorAll('#services .instances-overview-table tbody tr').length;
  expect(table()).toBe(3);
  await w.blacksmithViews.activate({screen:'services',instance:'a1',section:'details'},{}).catch(()=>{});
  await sleep(100);
  expect(d.getElementById('services').classList.contains('instance-open')).toBe(true);
  expect(table()).toBe(0);
  await w.blacksmithViews.activate({screen:'services'},{});
  expect(d.getElementById('services').classList.contains('instance-open')).toBe(false);
  expect(table()).toBe(3);
  await sleep(100);
  expect([...d.querySelectorAll('#services .instances-overview-table tbody tr')].map(r=>r.lastElementChild.textContent.trim())).toEqual(['0','0','0']);
  // The open instance is kept, so opening it again brings its screen back.
  await w.blacksmithViews.activate({screen:'services',instance:'a1',section:'details'},{}).catch(()=>{});
  expect(d.getElementById('services').classList.contains('instance-open')).toBe(true);
 }finally{w.close();}
});
