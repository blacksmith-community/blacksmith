const {test,expect,afterEach}=require('bun:test');
const {JSDOM}=require('./certificates/support.js');
const {setupRotate,cert}=require('./rotate-flow-support.js');
const t=JSDOM?test:test.skip;
const envs=[];
afterEach(()=>{while(envs.length){const env=envs.pop();expect(env.alerts).toEqual([]);env.w.close();}});
const certs=[
 cert('/s/a_crt','a_crt',5,'inst-a','cache-a'),
 cert('/s/b_crt','b_crt',20,'inst-b','cache-b'),
 cert('/s/c_crt','c_crt',100,'inst-c','cache-c'),
 cert('/s/d_crt','d_crt',400,'inst-d','cache-d'),
 cert('/s/e_crt','e_crt',-3,'inst-e','cache-e')
];
const dateOf=(days)=>new Date(Date.now()+days*86400000).toISOString().slice(0,10);
const shown=(d)=>[...d.querySelectorAll('#upgrade .upgrade-instances [data-instance-id]')].map(r=>r.dataset.instanceId).sort();
const open=async()=>{
 const env=await setupRotate({certs,bootOptions:{noDialogs:true}});envs.push(env);
 env.T.setBatchStep(2,false);
 return env;
};
const change=(w,el,value)=>{el.value=value;el.dispatchEvent(new w.Event('change',{bubbles:true}));};
const choose=async(env,value)=>{change(env.w,env.d.getElementById('batch-expiring'),value);await env.sleep(10);};
const range=async(env,from,to)=>{
 change(env.w,env.d.getElementById('batch-expiring-from'),from);
 change(env.w,env.d.getElementById('batch-expiring-to'),to);
 await env.sleep(10);
};

t('Custom range is the last choice and opens two labelled date fields',async()=>{
 const env=await open();const {d}=env;
 const f=d.getElementById('batch-expiring');
 const last=f.options[f.options.length-1];
 expect([last.value,last.textContent]).toEqual(['custom','Custom range']);
 expect([...f.options].slice(1,-1).map(o=>o.value)).toEqual(['7','14','30','60','90','180','365']);
 const row=d.getElementById('batch-expiring-range');
 expect(row.hidden).toBe(true);
 await choose(env,'custom');
 expect(row.hidden).toBe(false);
 for(const [id,label] of [['batch-expiring-from','From'],['batch-expiring-to','To']]){
  const input=d.getElementById(id);
  expect(input.type).toBe('date');
  expect(d.querySelector(`label[for="${id}"]`).textContent).toBe(label);
 }
 expect(d.getElementById('batch-expiring-row').children.length).toBe(2);
});
t('both ends are inclusive UTC dates and a range narrows the list',async()=>{
 const env=await open();const {d}=env;
 await choose(env,'custom');
 await range(env,dateOf(20),dateOf(100));
 expect(shown(d)).toEqual(['inst-b','inst-c']);
 await range(env,dateOf(21),dateOf(99));
 expect(shown(d)).toEqual([]);
});
t('an open end leaves that side open',async()=>{
 const env=await open();const {d}=env;
 await choose(env,'custom');
 await range(env,dateOf(100),'');
 expect(shown(d)).toEqual(['inst-c','inst-d']);
 await range(env,'',dateOf(20));
 expect(shown(d)).toEqual(['inst-a','inst-b','inst-e']);
 await range(env,'','');
 expect(shown(d)).toEqual(['inst-a','inst-b','inst-c','inst-d','inst-e']);
});
t('a custom range filters on the actual date, so an expired certificate counts only inside it',async()=>{
 const env=await open();const {d}=env;
 await choose(env,'custom');
 await range(env,dateOf(0),dateOf(30));
 expect(shown(d)).toEqual(['inst-a','inst-b']);
 await range(env,dateOf(-5),dateOf(-1));
 expect(shown(d)).toEqual(['inst-e']);
});
t('a From after To shows an inline status message and keeps the previous results',async()=>{
 const env=await open();const {d,w}=env;
 await choose(env,'custom');
 await range(env,dateOf(20),dateOf(100));
 const msg=d.getElementById('batch-expiring-range-error');
 expect(msg.getAttribute('role')).toBe('status');
 expect(msg.textContent).toBe('');
 await range(env,dateOf(200),dateOf(100));
 expect(msg.textContent).toContain('From');
 expect(shown(d)).toEqual(['inst-b','inst-c']);
 expect(d.getElementById('batch-expiring-from').getAttribute('aria-describedby')).toBe('batch-expiring-range-error');
 await range(env,dateOf(100),dateOf(400));
 expect(msg.textContent).toBe('');
 expect(shown(d)).toEqual(['inst-c','inst-d']);
});
t('choosing a fixed window hides the fields and restores the fixed behaviour',async()=>{
 const env=await open();const {d}=env;
 await choose(env,'custom');
 await range(env,dateOf(100),dateOf(400));
 await choose(env,'30');
 expect(d.getElementById('batch-expiring-range').hidden).toBe(true);
 expect(shown(d)).toEqual(['inst-a','inst-b','inst-e']);
 await choose(env,'');
 expect(shown(d)).toEqual(['inst-a','inst-b','inst-c','inst-d','inst-e']);
});
t('a custom range comes back with the rotate draft after another operation',async()=>{
 const env=await open();const {d,T}=env;
 await choose(env,'custom');
 await range(env,dateOf(20),dateOf(100));
 await T.requestOperation('upgrade');await env.sleep(20);
 await T.requestOperation('rotate');await env.sleep(40);
 expect(d.getElementById('batch-expiring').value).toBe('custom');
 expect(d.getElementById('batch-expiring-from').value).toBe(dateOf(20));
 expect(d.getElementById('batch-expiring-to').value).toBe(dateOf(100));
 expect(d.getElementById('batch-expiring-range').hidden).toBe(false);
 expect(shown(d)).toEqual(['inst-b','inst-c']);
});
