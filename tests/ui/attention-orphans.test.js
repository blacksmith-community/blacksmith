const {test,expect}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');

const quiet=()=>({deployment:'blacksmith',env:'lab',plans:{},instances:{a1:{service_id:'redis',plan:{name:'small'},vm_status:'running'}}});
const scan={state:'completed',completed_at:'2026-10-30T10:00:00Z',errors:[]};
const certs=['/b/certificates/inventory?attention=true',(u,o,json)=>json(200,{certificates:[],scan,summary:{certificates:0}})];
const okHealth={overall_status:'healthy',bosh:{status:'healthy',available:true},vault:{status:'healthy',available:true},cf:{status:'healthy',enabled:true}};
const health=['/b/internal/health',(u,o,json)=>json(200,okHealth)];
const noTasks=['/b/tasks',(u,o,json)=>json(200,[])];
const answer=(body,status=200)=>['/b/orphans',(u,o,json)=>json(status,status===200?{orphans:[],unclaimed:[],unclaimed_known:true,removal_after_seconds:86400,...body}:body)];
const orphan=(extra={})=>({instance_id:'o1',instance_name:'cache-old',deployment_name:'redis-small-o1',seconds_left:4*3600,due:false,...extra});
const mount=async(routes)=>{
 const env=boot({routes:[certs,health,noTasks,...routes]});const {w,d}=env;
 const input=quiet();
 const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.__T.renderBlacksmithOverview(input);
 await w.__T.refreshAttentionRequired(host,input);
 return {...env,host,panel:host.querySelector('.entity-attention')};
};
const row=(panel)=>panel.querySelector('[data-attention="orphans"]');
const rows=(panel)=>[...panel.querySelectorAll('[data-attention]')].map(r=>r.dataset.attention);

test('an orphan is listed with the time left and a link to the instance',async()=>{
 const {w,panel}=await mount([answer({orphans:[orphan()]})]);
 try{
  const text=row(panel).textContent.replace(/\s+/g,' ');
  expect(text).toContain('1 orphaned instance');
  expect(text).toContain('cache-old');
  expect(text).toContain('Expected removal in about 4 hours');
  const link=row(panel).querySelector('a');
  expect(link.getAttribute('href')).toBe('#/instances/o1/summary');
  expect(link.dataset.keepKey).toBe('attention-link:orphan:o1');
  expect(panel.textContent).not.toContain('Nothing needs attention');
 }finally{w.close();}
});

test('the orphan row sits in tier one, after VM alerts and ahead of certificates',async()=>{
 const env=boot({routes:[['/b/certificates/inventory?attention=true',(u,o,json)=>json(200,{certificates:[{id:'e',expiry:'expired'}],scan,summary:{certificates:1}})],health,noTasks,answer({orphans:[orphan()]})]});
 const {w,d}=env;
 try{
  const input={...quiet(),instances:{a1:{service_id:'redis',plan:{name:'small'},vm_status:'failing'}}};
  const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.__T.renderBlacksmithOverview(input);
  await w.__T.refreshAttentionRequired(host,input);
  expect(rows(host.querySelector('.entity-attention'))).toEqual(['instances','orphans','certificates-expired']);
  expect(host.querySelector('[data-attention="orphans"]').dataset.tone).toBe('warning');
 }finally{w.close();}
});

test('an entry that is due says so, and a short wait reads in minutes',async()=>{
 const {w,panel}=await mount([answer({orphans:[orphan({seconds_left:0,due:true}),orphan({instance_id:'o2',instance_name:'cache-new',seconds_left:25*60}),orphan({instance_id:'o3',instance_name:'cache-none',seconds_left:null})]})]);
 try{
  const items=[...row(panel).querySelectorAll('li')].map(li=>li.textContent.replace(/\s+/g,' ').trim());
  expect(items[0]).toContain('Removal is due on the next reconciler run');
  expect(items[1]).toContain('Expected removal in about 25 minutes');
  expect(items[2]).toContain('Removal time unknown');
  expect(row(panel).textContent).toContain('3 orphaned instances');
 }finally{w.close();}
});

test('a deployment with no Cloud Foundry instance is listed as text without a link',async()=>{
 const {w,panel}=await mount([answer({unclaimed:[{instance_id:'s1',deployment_name:'valkey-solo-s1',first_seen:new Date(Date.now()-3*3600000).toISOString()}]})]);
 try{
  const text=row(panel).textContent.replace(/\s+/g,' ');
  expect(text).toContain('1 deployment with no Cloud Foundry instance');
  expect(text).toContain('valkey-solo-s1');
  expect(row(panel).querySelector('a')).toBeNull();
  expect(row(panel).querySelector('time')).not.toBeNull();
  expect(text).toContain('counted from when the broker started');
 }finally{w.close();}
});

const note=(panel)=>panel.querySelector('[data-attention-note="unclaimed"]');

test('with nothing orphaned and the unclaimed list known, the box is all clear with no note',async()=>{
 const {w,panel}=await mount([answer({})]);
 try{
  expect(panel.textContent).toContain('Nothing needs attention');
  expect(row(panel)).toBeNull();
  expect(note(panel)).toBeNull();
 }finally{w.close();}
});

test('with the unclaimed list unknown the box keeps the all-clear and says it has not looked',async()=>{
 const {w,panel}=await mount([answer({unclaimed_known:false})]);
 try{
  expect(panel.textContent).toContain('Nothing needs attention');
  expect(row(panel)).toBeNull();
  expect(note(panel).textContent).toBe('BOSH deployments with no Cloud Foundry instance have not been checked since the broker started.');
  expect(note(panel).classList.contains('entity-attention-fetched')).toBe(true);
 }finally{w.close();}
});

test('the note also shows under a list of orphans when the unclaimed list is unknown',async()=>{
 const {w,panel}=await mount([answer({orphans:[orphan()],unclaimed_known:false})]);
 try{
  expect(row(panel)).not.toBeNull();
  expect(note(panel)).not.toBeNull();
 }finally{w.close();}
});

test('a failed request says Unknown and never all clear',async()=>{
 const {w,panel}=await mount([answer({error:'x'},500)]);
 try{
  expect(row(panel).textContent).toContain('Unknown');
  expect(row(panel).querySelector('a')).toBeNull();
  expect(panel.textContent).not.toContain('Nothing needs attention');
 }finally{w.close();}
});

test('the row reads Loading until the request returns',()=>{
 const {w,d}=boot({routes:[certs,health,noTasks]});
 try{
  const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.__T.renderBlacksmithOverview(quiet());
  expect(row(host.querySelector('.entity-attention')).textContent).toContain('Loading');
 }finally{w.close();}
});

test('names are escaped and stay out of attributes other than the instance id link',async()=>{
 const evil='"><img src=x onerror=alert(1)>';
 const {w,panel}=await mount([answer({orphans:[orphan({instance_name:evil,deployment_name:evil})],unclaimed:[{deployment_name:evil,first_seen:new Date().toISOString()}]})]);
 try{
  expect(row(panel).querySelector('img')).toBeNull();
  expect(row(panel).textContent).toContain(evil);
  for(const el of row(panel).querySelectorAll('*')){
   for(const attr of [...el.attributes]){
    expect(attr.value).not.toContain('onerror');
    if(attr.name==='title'||attr.name.startsWith('aria-label'))expect(attr.value).not.toContain('img');
   }
  }
 }finally{w.close();}
});
