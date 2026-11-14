const {test,expect}=require('bun:test');
const {watchUncaught}=require('./watch-uncaught.js');
const {main:{boot}}=require('./certificates/harness.js');
const hours=(n)=>new Date(Date.now()-n*3600000).toISOString();

const data=()=>({deployment:'blacksmith',env:'lab',plans:{'redis/small':{id:'small',name:'small',service:{id:'redis',name:'Redis'}}},instances:{
 a1:{service_id:'redis',plan_id:'small',instance_name:'cache-a',vm_status:'running'},
 b2:{service_id:'redis',plan_id:'small',instance_name:'cache-b',vm_status:'failing'}}});
const quiet=()=>({deployment:'blacksmith',env:'lab',plans:{},instances:{a1:{service_id:'redis',plan:{name:'small'},vm_status:'running'}}});
const scan={state:'completed',completed_at:'2026-10-30T10:00:00Z',errors:[]};
const certs=(list)=>['/b/certificates/inventory?attention=true',(u,o,json)=>json(200,{certificates:list,scan,summary:{certificates:list.length}})];
// The mock answers as the server does, so states=error and limit are honoured.
const tasks=(list,status=200)=>['/b/tasks',(u,o,json)=>{
 if(status!==200)return json(status,list);
 const q=new URL(u,'http://x').searchParams;const states=(q.get('states')||'').split(',').filter(Boolean);
 const out=list.filter(t=>!states.length||states.includes(t.state)).slice(0,Number(q.get('limit')||50));
 return json(200,out);
}];
const health=(body,status=200)=>['/b/internal/health',(u,o,json)=>json(status,body)];
const okHealth={overall_status:'healthy',bosh:{status:'healthy',available:true},vault:{status:'healthy',available:true},cf:{status:'healthy',enabled:true}};
const mount=async(routes,input=data())=>{
 const env=boot({routes});const {w,d}=env;
 const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.__T.renderBlacksmithOverview(input);
 await w.__T.refreshAttentionRequired(host,input);
 return {...env,host,panel:host.querySelector('.entity-attention')};
};
const rows=(panel)=>[...panel.querySelectorAll('[data-attention]')].map(r=>r.dataset.attention);

test('rows come in three fixed tiers, failures first',async()=>{
 const {w,panel}=await mount([
  certs([{id:'e',expiry:'expired'},{id:'w',expiry:'warning'},{id:'c',expiry:'critical'}]),
  tasks([{id:1,state:'error',started_at:hours(2)}]),
  health({...okHealth,overall_status:'unhealthy',vault:{status:'unhealthy',available:false}})]);
 try{
  expect(rows(panel)).toEqual(['failed-tasks','broker-health','instances','certificates-expired','certificates-expiring']);
 }finally{w.close();}
});

test('failed tasks counts only error tasks from the last 24 hours and links to Tasks with the Error chip',async()=>{
 const {w,panel}=await mount([certs([]),health(okHealth),tasks([
  {id:1,state:'error',started_at:hours(1)},{id:2,state:'error',started_at:hours(23)},
  {id:3,state:'error',started_at:hours(30)},{id:4,state:'done',started_at:hours(1)}])],quiet());
 try{
  const row=panel.querySelector('[data-attention="failed-tasks"]');
  expect(row.textContent).toContain('Failed tasks (24h)');
  const a=row.querySelector('a');
  expect(row.textContent).toContain('Failed tasks (24h): 2');
  expect(a.textContent).toBe('View failed tasks');
  expect(a.getAttribute('href')).toBe('#/broker/tasks?state=error');
  expect(a.dataset.keepKey).toBe('attention-link:failed-tasks');
 }finally{w.close();}
});

test('broker health row appears only when unhealthy and names the failed component',async()=>{
 const bad=await mount([certs([]),tasks([]),health({...okHealth,overall_status:'degraded',vault:{status:'unhealthy',available:false}})],quiet());
 try{
  const row=bad.panel.querySelector('[data-attention="broker-health"]');
  expect(row.textContent).toContain('Broker health');
  expect(row.textContent).toContain('Vault');
  expect(row.textContent).not.toContain('BOSH');
 }finally{bad.w.close();}
 const good=await mount([certs([]),tasks([]),health(okHealth)],quiet());
 try{ expect(good.panel.querySelector('[data-attention="broker-health"]')).toBeNull(); }finally{good.w.close();}
});

test('with nothing to flag the section keeps its plain line',async()=>{
 const {w,panel}=await mount([certs([]),tasks([]),health(okHealth)],quiet());
 try{ expect(panel.textContent).toContain('Nothing needs attention'); expect(panel.querySelector('[data-attention]')).toBeNull(); }finally{w.close();}
});

test('a failed tasks or health request says Unknown and never claims clear',async()=>{
 const {w,panel}=await mount([certs([]),tasks([],500),health({},500)],quiet());
 try{
  expect(panel.textContent).not.toContain('Nothing needs attention');
  expect(panel.querySelector('[data-attention="failed-tasks"]').textContent).toContain('Unknown');
 }finally{w.close();}
});

test('each row carries a severity word, an icon, and a tone',async()=>{
 const {w,panel}=await mount([certs([{id:'e',expiry:'expired'},{id:'w',expiry:'warning'}]),tasks([{id:1,state:'error',started_at:hours(1)}]),health(okHealth)]);
 try{
  const expect_=(name,tone,word)=>{
   const row=panel.querySelector(`[data-attention="${name}"]`);
   expect(row.dataset.tone).toBe(tone);
   expect(row.querySelector('.entity-attention-severity').textContent).toBe(word);
   expect(row.querySelector('svg')).not.toBeNull();
  };
  expect_('failed-tasks','critical','Critical');
  expect_('instances','critical','Critical');
  expect_('certificates-expired','critical','Critical');
  expect_('certificates-expiring','warning','Warning');
 }finally{w.close();}
});

test('attention links are underlined',()=>{
 const css=require('fs').readFileSync(require('path').join(__dirname,'..','..','ui','blacksmith.css'),'utf8');
 expect(css).toMatch(/\.entity-attention a[^{]*\{[^}]*text-decoration:\s*underline/);
 expect(css).toMatch(/\.entity-attention-row\[data-tone="critical"\][^{]*\{[^}]*border-left[^}]*var\(--cert-expired\)/);
});

test('the tasks route carries a state and parses back',()=>{
 const {JSDOM}=require('./certificates/support.js');
 const dom=new JSDOM('<main id="console-main"><h1 id="console-title">Blacksmith</h1></main>',{url:'http://localhost/',runScripts:'outside-only'});
 const w=watchUncaught(dom.window);w.eval(require('fs').readFileSync(require('path').join(__dirname,'..','..','ui','routes.js'),'utf8'));
 const api=w.BlacksmithRoutes;
 try{
  expect(api.format({screen:'blacksmith',section:'tasks',state:'error'})).toBe('#/broker/tasks?state=error');
  expect(api.parse('#/broker/tasks?state=error')).toEqual({screen:'blacksmith',section:'tasks',state:'error'});
  expect(api.parse('#/broker/tasks?state=bogus')).toEqual({screen:'blacksmith',section:'tasks'});
  expect(api.parse('#/broker/vms?state=error')).toEqual({screen:'blacksmith',section:'vms'});
 }finally{w.close();}
});

test('failures are counted past the newest 100 tasks, because the server filters to errors',async()=>{
 const list=[...Array.from({length:120},(_,i)=>({id:1000+i,state:'done',started_at:hours(1)})),
  ...Array.from({length:150},(_,i)=>({id:i,state:'error',started_at:hours(2)}))];
 const {w,calls,panel}=await mount([certs([]),health(okHealth),tasks(list)],quiet());
 try{
  expect(calls.some(c=>/\/b\/tasks\?.*states=error/.test(c))).toBe(true);
  expect(panel.querySelector('[data-attention="failed-tasks"]').textContent).toContain('Failed tasks (24h): 150');
  expect(panel.textContent).not.toContain('Nothing needs attention');
 }finally{w.close();}
});

test('a full response reads as a lower bound',async()=>{
 const list=Array.from({length:230},(_,i)=>({id:i,state:'error',started_at:hours(2)}));
 const {w,panel}=await mount([certs([]),health(okHealth),tasks(list)],quiet());
 try{ expect(panel.querySelector('[data-attention="failed-tasks"]').textContent).toContain('at least 200'); }finally{w.close();}
});

test('a hung request times out into Unknown while the other rows still show',async()=>{
 const env=boot({setup:(w)=>{w.blacksmithAttentionTimeoutMs=30;},routes:[certs([{id:'e',expiry:'expired'}]),['/b/tasks',()=>new Promise(()=>{})],['/b/internal/health',()=>new Promise(()=>{})]]});
 const {w,d}=env;
 try{
  const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.__T.renderBlacksmithOverview(quiet());
  await w.__T.refreshAttentionRequired(host,quiet());
  const panel=host.querySelector('.entity-attention');
  expect(panel.querySelector('[data-attention="failed-tasks"]').textContent).toContain('Unknown');
  expect(panel.querySelector('[data-attention="broker-health"]').textContent).toContain('Unknown');
  expect(panel.querySelector('[data-attention="certificates-expired"]')).not.toBeNull();
 }finally{w.close();}
});

test('each tier paints as its data arrives',async()=>{
 let release;const slow=new Promise(r=>{release=r;});
 const env=boot({routes:[certs([{id:'e',expiry:'expired'}]),['/b/tasks',(u,o,json)=>slow.then(()=>json(200,[]))],health(okHealth)]});
 const {w,d}=env;
 try{
  const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.__T.renderBlacksmithOverview(quiet());
  const done=w.__T.refreshAttentionRequired(host,quiet());
  await new Promise(r=>setTimeout(r,60));
  expect(host.querySelector('[data-attention="certificates-expired"]')).not.toBeNull();
  expect(host.querySelector('[data-attention="failed-tasks"]').textContent).toContain('Loading');
  release();await done;
  expect(host.querySelector('[data-attention="failed-tasks"]')).toBeNull();
 }finally{w.close();}
});

test('the failed-tasks link carries keep keys that appear once in the UI',()=>{
 const fs=require('fs'),path=require('path');
 const ui=path.join(__dirname,'..','..','ui');
 const all=fs.readdirSync(ui).filter(f=>f.endsWith('.js')||f.endsWith('.html')).map(f=>fs.readFileSync(path.join(ui,f),'utf8')).join('\n');
 for(const key of ['attention-link:failed-tasks'])expect(all.split(key).length-1).toBe(1);
});

// The Tasks view, opened through the router the way the Summary link does.
const taskList=[{id:1,state:'error',description:'a',started_at:hours(1)},{id:2,state:'done',description:'b',started_at:hours(1)},{id:3,state:'processing',description:'c',started_at:hours(1)}];
const bootTasks=()=>boot({noDialogs:true,routes:[tasks(taskList),certs([]),health(okHealth),
 ['/v2/catalog',(u,o,json)=>json(200,{services:[]})],
 ['/b/status',(u,o,json)=>json(200,{env:'lab',deployment:'blacksmith',plans:{},instances:{}})]]});
const pressed=(d)=>[...d.querySelectorAll('#blacksmith .compact-tasks-controls input[type="checkbox"][value]')].filter(b=>b.checked).map(b=>b.value);
const visible=(d)=>[...d.querySelectorAll('#blacksmith .tasks-table tbody tr')].filter(r=>r.style.display!=='none').map(r=>r.querySelector('.task-state .status-badge')?.dataset.state);

test('opening the tasks route with state=error leaves only Error pressed and only error rows visible',async()=>{
 const {w,d}=bootTasks();
 try{
  await new Promise(r=>setTimeout(r,150));
  await w.blacksmithViews.activate({screen:'blacksmith',section:'tasks',state:'error'},{});
  await new Promise(r=>setTimeout(r,150));
  expect(pressed(d)).toEqual(['error']);
  expect(visible(d)).toEqual(['error']);
  expect(w.blacksmithViews.read('blacksmith').state).toBe('error');
  // A plain visit shows every state again, and the address drops the state.
  await w.blacksmithViews.activate({screen:'blacksmith',section:'tasks'},{});
  expect(pressed(d).sort()).toEqual(['cancelled','done','error','processing','queued']);
  expect(visible(d).length).toBe(3);
  expect(w.blacksmithViews.read('blacksmith').state).toBeUndefined();
 }finally{w.close();}
});


test('a repaint keeps focus on a certificate link and a VM-alert link',async()=>{
 let release;const slow=new Promise(r=>{release=r;});
 const input=data();
 const {w,d}=boot({routes:[certs([{id:'e',expiry:'expired'}]),['/b/tasks',(u,o,json)=>slow.then(()=>json(200,[]))],['/b/internal/health',(u,o,json)=>slow.then(()=>json(200,okHealth))]]});
 try{
  const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.__T.renderBlacksmithOverview(input);
  const done=w.__T.refreshAttentionRequired(host,input);
  await new Promise(r=>setTimeout(r,60));
  for(const selector of ['[data-attention="certificates-expired"] a','[data-attention="instances"] a']){
   const link=host.querySelector(selector);
   link.focus();
   const key=link.dataset.keepKey;
   expect(key).toBeTruthy();
   release();await done;
   expect(d.activeElement.dataset.keepKey).toBe(key);
   expect(d.activeElement.closest('.entity-attention')).not.toBeNull();
  }
 }finally{w.close();}
});

test('the attention links carry keep keys that appear once in the UI',()=>{
 const fs=require('fs'),path=require('path');
 const ui=path.join(__dirname,'..','..','ui');
 const all=fs.readdirSync(ui).filter(f=>f.endsWith('.js')||f.endsWith('.html')).map(f=>fs.readFileSync(path.join(ui,f),'utf8')).join('\n');
 for(const key of ['attention-link:certificates-expired','attention-link:certificates-expiring','attention-link:certificates"','attention-link:instance:'])expect(all.split(key).length-1).toBe(1);
});
