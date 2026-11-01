const {test,expect}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');

const data=(extra={})=>({deployment:'blacksmith',env:'lab',plans:{},instances:{
 a1:{service_id:'redis',plan:{name:'small'},instance_name:'cache-a',vm_status:'running'},
 b2:{service_id:'redis',plan:{name:'small'},instance_name:'cache-b',vm_status:'failing'},
 c3:{service_id:'valkey',plan:{name:'solo'},vm_status:'unresponsive agent'},
 d4:{service_id:'redis',plan:{name:'small'},instance_name:'gone',vm_status:'failing',deleted:true}
},...extra});
const quiet=()=>({deployment:'blacksmith',env:'lab',plans:{},instances:{a1:{service_id:'redis',plan:{name:'small'},vm_status:'running'}}});
const inventory=(certs,status=200)=>['/b/certificates/inventory?attention=true',(u,o,json)=>json(status,status===200?{certificates:certs}:{error:'off'})];
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
  expect(panel.querySelector('.entity-attention-fetched time')).not.toBeNull();
 }finally{w.close();}
});

test('the certificate row says Unknown until the request returns',()=>{
 const {w,d}=boot();try{
  const host=mount(w,d,data());
  expect(certRow(host).textContent).toContain('Unknown');
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
