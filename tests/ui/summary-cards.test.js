const {test,expect}=require('bun:test');
const {watchUncaught}=require('./watch-uncaught.js');
const {main:{boot}}=require('./certificates/harness.js');

const data=()=>({deployment:'blacksmith',env:'lab',plans:{'redis/small':{id:'small',name:'small',service:{id:'redis',name:'Redis'}}},instances:{
 a1:{service_id:'redis',plan_id:'small',instance_name:'cache-a',vm_status:'running'},
 b2:{service_id:'redis',plan_id:'small',instance_name:'cache-b',vm_status:'failing'}}});

test('the Service types box counts types and jumps to the table',()=>{
 const {w,d}=boot();try{
  const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.__T.renderBlacksmithOverview(data());
  const metrics=[...host.querySelectorAll('.entity-metric')];
  expect(metrics.length).toBe(4);
  const box=metrics.find(m=>m.textContent.includes('Service types'));
  expect(box.querySelector('strong').textContent).toBe('1');
  expect(box.dataset.entityAnchor).toBe('entity-services');
  expect(box.getAttribute('aria-label')).toBe('1 service type. View service types');
  expect(host.querySelector('.entity-services-scroll').id).toBe('entity-services');
 }finally{w.close();}
});

test('every Summary card has an explicit label',()=>{
 const {w,d}=boot();try{
  const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.__T.renderBlacksmithOverview(data());
  const labels=[...host.querySelectorAll('.entity-metric')].map(m=>m.getAttribute('aria-label'));
  expect(labels).toContain('2 service instances. View service instances');
  expect(labels.every(Boolean)).toBe(true);
 }finally{w.close();}
});

test('the services scroll region has its own name',()=>{
 const {w,d}=boot();try{
  const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.__T.renderBlacksmithOverview(data());
  const region=host.querySelector('.entity-services-scroll');
  const section=host.querySelector('.entity-services');
  expect(region.getAttribute('aria-label')).toBe('Service types and plans table');
  expect(region.hasAttribute('aria-labelledby')).toBe(false);
  expect(section.getAttribute('aria-labelledby')).toBe('entity-services-heading');
 }finally{w.close();}
});

test('with no service types the card is disabled and says so',()=>{
 const {w,d}=boot();try{
  const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.__T.renderBlacksmithOverview({deployment:'b',env:'l',plans:{},instances:{}});
  const card=[...host.querySelectorAll('.entity-metric')].find(m=>m.textContent.includes('Service types'));
  expect(card.getAttribute('aria-disabled')).toBe('true');
  expect(card.hasAttribute('data-entity-anchor')).toBe(false);
  expect(card.getAttribute('aria-label')).toBe('0 service types. Nothing to show');
 }finally{w.close();}
});

test('the Service types card carries a keep key that appears once in the UI',()=>{
 const fs=require('fs'),path=require('path');
 const ui=path.join(__dirname,'..','..','ui');
 const all=fs.readdirSync(ui).filter(f=>f.endsWith('.js')||f.endsWith('.html')).map(f=>fs.readFileSync(path.join(ui,f),'utf8')).join('\n');
 expect(all.split('summary-card:service-types').length-1).toBe(1);
});
