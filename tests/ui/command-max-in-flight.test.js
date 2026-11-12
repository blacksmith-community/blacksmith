const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {expectNoAlerts}=require('./certificates/no-alerts.js');
const t=JSDOM?test:test.skip;

const AVAILABILITY_URL='/b/upgrade/commands/availability';
const envs=[];
afterEach(()=>{ while(envs.length){ expectNoAlerts(envs.pop()); } });

// setup boots the page with an availability answer that carries the limit.
// A limit of null leaves the key out, as an older broker would, and
// 'fail' makes the request fail on the network.
async function setup(limit){
 const env=boot({noDialogs:true}),{w}=env;envs.push(env);
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])]);
 w.__routes.push([AVAILABILITY_URL,(u,o,j)=>{
  if(limit==='fail') return Promise.reject(new Error('network down'));
  const body={enabled:true,allow_root:true};
  if(limit!==null) body.max_in_flight_limit=limit;
  return j(200,body);
 }]);
 await sleep(30);w.serviceInstances={cache:{service_id:'valkey',plan_id:'small'}};
 await w.__T.initUpgradeTab();await sleep(10);
 await w.__T.requestOperation('command');w.__T.setBatchStep(1,false);
 return env;
}
const group=(d)=>d.getElementById('command-max-in-flight');
const radios=(d)=>[...group(d).querySelectorAll('input[type=radio]')];
const selectOf=(d)=>group(d).querySelector('select');
const checked=(d)=>{ const r=radios(d).find(x=>x.checked); return r?r.value:null; };
const note=(d)=>d.getElementById('command-max-in-flight-note');

t('a limit of 1 shows one selected choice and says the director allows one node',async()=>{
 const env=await setup(1);const {w,d}=env;try{
 expect(radios(d).map(r=>r.value)).toEqual(['1']);
 expect(checked(d)).toBe('1');
 expect(w.__T.commandState.maxInFlight).toBe('1');
 const only=d.getElementById('command-max-in-flight-only');
 expect(only.hidden).toBe(false);
 expect(only.textContent).toBe('The director allows one node at a time.');
 expect(group(d).getAttribute('aria-describedby')).toContain('command-max-in-flight-only');
 }finally{w.close();}
});

t('a limit of 5 shows radios from 1 to 5 with 2 selected',async()=>{
 const env=await setup(5);const {w,d}=env;try{
 expect(radios(d).map(r=>r.value)).toEqual(['1','2','3','4','5']);
 expect(d.getElementById('command-max-in-flight-only').hidden).toBe(true);
 expect(selectOf(d)).toBeNull();
 expect(checked(d)).toBe('2');
 d.getElementById('command-max-in-flight-5').click();
 expect(w.__T.commandState.maxInFlight).toBe('5');
 }finally{w.close();}
});

t('a limit of 10 still shows radios',async()=>{
 const env=await setup(10);const {w,d}=env;try{
 expect(radios(d).length).toBe(10);expect(selectOf(d)).toBeNull();
 }finally{w.close();}
});

t('a limit of 12 shows a native select with every value',async()=>{
 const env=await setup(12);const {w,d}=env;try{
 expect(radios(d).length).toBe(0);
 const select=selectOf(d);
 expect(select).not.toBeNull();
 expect([...select.options].map(o=>o.value)).toEqual(['1','2','3','4','5','6','7','8','9','10','11','12']);
 expect(select.value).toBe('2');
 expect(d.querySelector('#batch-flow-options input[type=number][id*="in-flight"]')).toBeNull();
 select.value='11';select.dispatchEvent(new w.Event('change',{bubbles:true}));
 expect(w.__T.commandState.maxInFlight).toBe('11');
 }finally{w.close();}
});

t('the review payload keeps its shape and sends 0 when parallel is off',async()=>{
 const env=await setup(12);const {w,d}=env;try{
 const select=selectOf(d);
 select.value='9';select.dispatchEvent(new w.Event('change',{bubbles:true}));
 expect(w.__T.commandValues().max_in_flight).toBe(0);
 d.getElementById('command-parallel').click();
 expect(w.__T.commandValues().max_in_flight).toBe(9);
 expect(Object.keys(w.__T.commandValues()).sort()).toEqual(['command','instance_ids','max_in_flight','parallel','run_as','timeout_seconds']);
 }finally{w.close();}
});

t('while the limit is unknown the choices are 1, 2, and 3',async()=>{
 const env=await setup(null);const {w,d}=env;try{
 expect(radios(d).map(r=>r.value)).toEqual(['1','2','3']);
 expect(checked(d)).toBe('2');
 }finally{w.close();}
});

t('a failed availability request keeps 1, 2, and 3 and the review check',async()=>{
 const env=await setup('fail');const {w,d}=env;try{
 expect(radios(d).map(r=>r.value)).toEqual(['1','2','3']);
 d.getElementById('command-parallel').click();
 d.getElementById('command-max-in-flight-3').click();
 w.__T.commandState.text='x';
 w.__T.recordCommandPreview({parallel_available:true,worker_count:3,max_in_flight_limit:2});
 expect(radios(d).map(r=>r.value)).toEqual(['1','2']);
 }finally{w.close();}
});

t('a limit of 0 from the broker keeps the fallback choices',async()=>{
 const env=await setup(0);const {w,d}=env;try{
 expect(radios(d).map(r=>r.value)).toEqual(['1','2','3']);
 }finally{w.close();}
});

t('a chosen value above the limit falls back to the default with a note',async()=>{
 const env=await setup(null);const {w,d}=env;try{
 d.getElementById('command-parallel').click();
 d.getElementById('command-max-in-flight-3').click();
 expect(w.__T.commandState.maxInFlight).toBe('3');
 expect(note(d).hidden).toBe(true);
 w.__T.recordCommandPreview({parallel_available:true,worker_count:3,max_in_flight_limit:2});
 expect(w.__T.commandState.maxInFlight).toBe('2');
 expect(checked(d)).toBe('2');
 expect(note(d).hidden).toBe(false);
 expect(note(d).textContent).toContain('3');
 expect(note(d).textContent).toContain('2');
 d.getElementById('command-max-in-flight-1').click();
 expect(note(d).hidden).toBe(true);
 }finally{w.close();}
});

t('an untouched default above a small limit adjusts without a note',async()=>{
 const env=await setup(1);const {d}=env;try{
 expect(note(d).hidden).toBe(true);
 }finally{env.w.close();}
});

t('the help text says the choices go up to what the director allows',async()=>{
 const env=await setup(5);const {w,d}=env;try{
 const help=d.getElementById('command-max-in-flight-help').textContent;
 expect(help).toContain('up to what the director allows');
 expect(help).not.toContain('stop at 3');
 expect(group(d).getAttribute('aria-describedby')).toContain('command-max-in-flight-note');
 }finally{w.close();}
});
