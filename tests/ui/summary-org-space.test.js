const {test,expect}=require('bun:test');
const {watchUncaught}=require('./watch-uncaught.js');
const {main:{boot}}=require('./certificates/harness.js');
const hours=(n)=>new Date(Date.now()-n*3600000).toISOString();

test('a missing organization or space is explained and never linked',()=>{
 const {w,d}=boot();try{
  w.__T.renderServiceDetail('i1',{service_id:'redis',plan:{name:'small'}},{instance_id:'i1',service_id:'redis',plan_id:'small',instance_name:'x'});
  const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.serviceInstanceDetailsContent.i1;
  const cells=[...host.querySelectorAll('.entity-context-table tr')].map(r=>r.textContent.replace(/\s+/g,' '));
  const text=host.querySelector('.entity-context-table').textContent.replace(/\s+/g,' ');
  expect(text).toContain('Organization');
  expect(text).toContain('Provisioned without a Cloud Foundry context');
  expect(host.querySelector('.entity-context-table a')).toBeNull();
 }finally{w.close();}
});

