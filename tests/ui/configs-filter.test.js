const {test,expect,afterEach}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); } finally { env.w.close(); } }
});

t('the configs type filter shows only the picked types and brings every row back when cleared',()=>{
 const env=boot({noDialogs:true}); envs.push(env);
 const {w,d}=env;
 const configs=[
  {id:'1',name:'a',type:'cloud',team:'x',created_at:'2026-10-24T10:00:00Z'},
  {id:'2',name:'b',type:'cpi',team:'x',created_at:'2026-10-24T10:00:00Z'},
  {id:'3',name:'c',type:'runtime',team:'x',created_at:'2026-10-24T10:00:00Z'},
  {id:'4',name:'d',type:'cpi',team:'x',created_at:'2026-10-24T10:00:00Z'}
 ];
 const host=d.createElement('div'); host.id='blacksmith'; d.body.appendChild(host);
 host.innerHTML=w.__T.formatConfigs({configs});
 const boxes=Array.from(host.querySelectorAll('.configs-filters input[type="checkbox"]'));
 const pick=(types)=>{ boxes.forEach((b)=>{ b.checked=types.includes(b.value); }); w.__T.applyConfigsFilter(); };
 const shown=()=>Array.from(host.querySelectorAll('.configs-table tbody tr, table tbody tr')).filter((r)=>r.style.display!=='none').map((r)=>r.querySelector('.config-name').textContent);
 pick(['cpi']);
 expect(shown()).toEqual(['b','d']);
 pick(['cloud','runtime']);
 expect(shown()).toEqual(['a','c']);
 pick(['cloud','cpi','runtime','resurrection']);
 expect(shown().sort()).toEqual(['a','b','c','d']);
});
