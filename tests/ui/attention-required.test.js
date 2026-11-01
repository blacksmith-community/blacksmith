const {test,expect}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');

const data=(extra={})=>({deployment:'blacksmith',env:'lab',plans:{},instances:{
 a1:{service_id:'redis',plan:{name:'small'},instance_name:'cache-a',vm_status:'running'},
 b2:{service_id:'redis',plan:{name:'small'},instance_name:'cache-b',vm_status:'failing'},
 c3:{service_id:'valkey',plan:{name:'solo'},vm_status:'unresponsive agent'},
 d4:{service_id:'redis',plan:{name:'small'},instance_name:'gone',vm_status:'failing',deleted:true}
},...extra});
const quiet=()=>({deployment:'blacksmith',env:'lab',plans:{},instances:{a1:{service_id:'redis',plan:{name:'small'},vm_status:'running'}}});
const scanned={state:'completed',completed_at:'2026-10-30T10:00:00Z',errors:[]};
const inventory=(certs,status=200,scan=scanned)=>['/b/certificates/inventory?attention=true',(u,o,json)=>json(status,status===200?{certificates:certs,scan,summary:{certificates:certs.length}}:{error:'off'})];
const mount=(w,d,input)=>{ const host=d.createElement('div'); d.body.appendChild(host); host.innerHTML=w.__T.renderBlacksmithOverview(input); return host; };
const section=(host)=>host.querySelector('.entity-attention');
const certRow=(host)=>section(host).querySelector('[data-attention="certificates"]');

test('the summary lists instances with VM alerts above the inventory and links each one',()=>{
 const {w,d}=boot();try{
  const host=mount(w,d,data());
  const panel=section(host);
  expect(panel.querySelector('h3').textContent).toBe('Attention required');
  expect(panel.compareDocumentPosition(host.querySelector('.entity-services'))&4).toBeTruthy();
  const links=[...panel.querySelectorAll('[data-attention="instances"] a')];
  expect(links.map(a=>[a.textContent,a.getAttribute('href')])).toEqual([['cache-b','#/instances/b2/summary'],['c3','#/instances/c3/summary']]);
  expect(panel.querySelector('[data-attention="instances"]').textContent).toContain('failing');
  expect(panel.textContent).not.toContain('gone');
  // The instance list was never fetched here, so no fetched time is claimed.
  expect(panel.querySelector('[data-attention="instances"] .entity-attention-fetched')).toBeNull();
  expect(panel.querySelector('[data-attention="instances"]').textContent).toContain('VM job state: failing');
 }finally{w.close();}
});

test('the certificate row says Loading until the request returns, with no fetched time',()=>{
 const {w,d}=boot();try{
  const host=mount(w,d,data());
  expect(certRow(host).textContent).toContain('Loading');
  expect(certRow(host).querySelector('.entity-attention-fetched')).toBeNull();
  expect(certRow(host).textContent).not.toMatch(/\b0\b/);
 }finally{w.close();}
});

test('a returned request shows the count and links to the filtered certificates',async()=>{
 const {w,d,calls}=boot({routes:[inventory([{id:'x1'},{id:'x2'},{id:'x3'}])]});
 try{
  const host=mount(w,d,data());
  await w.__T.refreshAttentionRequired(host,data());
  const link=certRow(host).querySelector('a');
  expect(link.getAttribute('href')).toBe('#/certificates?attention=true');
  expect(link.textContent).toBe('3 certificates need attention');
  expect(calls.filter(c=>c==='GET /b/certificates/inventory?attention=true').length).toBe(1);
  expect(host.querySelectorAll('.entity-attention').length).toBe(1);
  expect(section(host).querySelector('.entity-attention-fetched time')).not.toBeNull();
 }finally{w.close();}
});

test('a failed request keeps the row Unknown rather than zero',async()=>{
 const {w,d}=boot({routes:[inventory([],403)]});
 try{
  const host=mount(w,d,data());
  await w.__T.refreshAttentionRequired(host,data());
  expect(certRow(host).textContent).toContain('Unknown');
  expect(certRow(host).querySelector('a')).toBeNull();
 }finally{w.close();}
});

test('a first scan that never completed says Unknown with a reason, never all clear',async()=>{
 for(const scan of [{state:'never'},{state:'failed',completed_at:'2026-10-30T10:00:00Z',errors:[{instance_id:'a1',error:'x'}]}]){
  const {w,d}=boot({routes:[inventory([],200,scan)]});
  try{
   const host=mount(w,d,quiet());
   await w.__T.refreshAttentionRequired(host,quiet());
   const text=section(host).textContent;
   expect(text).not.toContain('Nothing needs attention');
   expect(certRow(host).textContent).toContain('Unknown');
   expect(certRow(host).textContent).toContain('No completed certificate scan yet');
   expect(certRow(host).querySelector('.entity-attention-fetched')).toBeNull();
  }finally{w.close();}
 }
});

test('leaving the summary while the request runs does not strand it on Unknown',async()=>{
 const {w,d}=boot({routes:[inventory([{id:'x1'}])]});
 try{
  const host=mount(w,d,data());
  const pending=w.__T.refreshAttentionRequired(host,data());
  const old=section(host);
  old.remove();  // set aside while loading
  host.appendChild(old);
  await pending;
  expect(certRow(host).textContent).not.toContain('Unknown');
  expect(certRow(host).querySelector('a').textContent).toBe('1 certificate needs attention');
 }finally{w.close();}
});

test('with nothing to flag the section says so in one line',async()=>{
 const {w,d}=boot({routes:[inventory([])]});
 try{
  const host=mount(w,d,quiet());
  // Until the certificates are known, nothing can be called clear.
  expect(section(host).textContent).not.toContain('Nothing needs attention');
  await w.__T.refreshAttentionRequired(host,quiet());
  expect(section(host).textContent.replace(/\s+/g,' ').trim()).toMatch(/^Attention required\s*Nothing needs attention\./);
  expect(section(host).querySelector('ul')).toBeNull();
 }finally{w.close();}
});

test('instance names are escaped in the section',()=>{
 const {w,d}=boot();try{
  const evil='"><img src=x onerror=alert(1)>';
  const input=data(); input.instances.b2.instance_name=evil;
  const host=mount(w,d,input);
  expect(section(host).querySelector('img')).toBeNull();
  expect(section(host).textContent).toContain(evil);
 }finally{w.close();}
});

const bootFleet=()=>boot({noDialogs:true,routes:[
 ['/b/certificates/inventory',(u,o,json)=>json(200,{certificates:[{id:'x1',owners:[{kind:'instance',instance_id:'a1'}]}],scan:{state:'completed',completed_at:'2026-10-30T10:00:00Z',errors:[]},summary:{certificates:1}})],
 ['/v2/catalog',(u,o,json)=>json(200,{services:[{id:'redis',name:'Redis',plans:[{id:'small',name:'small'}]}]})],
 ['/b/status',(u,o,json)=>json(200,{env:'lab',deployment:'blacksmith',plans:{},instances:{
  a1:{service_id:'redis',plan:{name:'small'},instance_name:'cache-a',vm_status:'running'},
  b2:{service_id:'redis',plan:{name:'small'},instance_name:'cache-b',vm_status:'running'},
  c3:{service_id:'redis',plan:{name:'small'},instance_name:'cache-c',vm_status:'failing'}}})]]});
const attentionCalls=(calls)=>calls.filter(c=>c==='GET /b/certificates/inventory?attention=true').length;

test('page load makes one attention request and the instances screen shares it',async()=>{
 const {w,d,calls}=bootFleet();
 try{
  await new Promise(r=>setTimeout(r,150));
  expect(attentionCalls(calls)).toBe(1);
  expect(w.blacksmithData.instances.a1.instance_name).toBe('cache-a');
  expect(typeof w.blacksmithDataFetchedAt).toBe('number');
  await w.blacksmithViews.activate({screen:'services'},{});
  await new Promise(r=>setTimeout(r,150));
  expect(attentionCalls(calls)).toBe(1);
  const column=[...d.querySelectorAll('.instances-overview-table tbody tr')].map(r=>r.lastElementChild.textContent.trim());
  expect(column.sort()).toEqual(['0','0','1']);
 }finally{w.close();}
});
