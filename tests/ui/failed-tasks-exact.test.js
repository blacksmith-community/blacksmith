const {test,expect}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');
const hours=(n)=>new Date(Date.now()-n*3600000).toISOString();

const quiet=()=>({deployment:'blacksmith',env:'lab',plans:{},instances:{a1:{service_id:'redis',plan:{name:'small'},vm_status:'running'}}});
const scan={state:'completed',completed_at:'2026-10-30T10:00:00Z',errors:[]};
const certs=['/b/certificates/inventory?attention=true',(u,o,json)=>json(200,{certificates:[],scan,summary:{certificates:0}})];
const okHealth={overall_status:'healthy',bosh:{status:'healthy',available:true},vault:{status:'healthy',available:true},cf:{status:'healthy',enabled:true}};
const health=['/b/internal/health',(u,o,json)=>json(200,okHealth)];
// The broker's object form. A test names only what it cares about.
const answer=(body,seen)=>['/b/tasks',(u,o,json)=>{seen?.push(u);return json(200,{tasks:[],offset:0,limit:200,has_more:false,exact:true,...body});}];
const mount=async(routes)=>{
 const env=boot({routes});const {w,d}=env;
 const input=quiet();
 const host=d.createElement('div');d.body.appendChild(host);host.innerHTML=w.__T.renderBlacksmithOverview(input);
 await w.__T.refreshAttentionRequired(host,input);
 return {...env,host,panel:host.querySelector('.entity-attention')};
};
const err=(id,h)=>({id,state:'error',started_at:hours(h)});
const failedRow=(panel)=>panel.querySelector('[data-attention="failed-tasks"]');

test('the failed tasks row asks for the exact object form with the error state',async()=>{
 const seen=[];
 const {w}=await mount([certs,health,answer({},seen)]);
 try{
  const url=seen.pop();
  expect(url).toContain('states=error');
  expect(url).toContain('paged=1');
  expect(url).toContain('type=recent');
 }finally{w.close();}
});

test('a complete answer gives an exact count with no at least',async()=>{
 const {w,panel}=await mount([certs,health,answer({tasks:[err(9,2),err(8,5),err(7,40)]})]);
 try{
  expect(failedRow(panel).textContent).toContain('Failed tasks (24h): 2');
  expect(failedRow(panel).textContent).not.toContain('at least');
 }finally{w.close();}
});

test('more errors beyond a page that still reaches into the last 24 hours makes the count at least',async()=>{
 const {w,panel}=await mount([certs,health,answer({tasks:[err(9,2),err(8,5)],has_more:true})]);
 try{
  expect(failedRow(panel).textContent).toContain('Failed tasks (24h): at least 2');
 }finally{w.close();}
});

test('more errors beyond a page that already reached older than 24 hours leaves the count exact',async()=>{
 const {w,panel}=await mount([certs,health,answer({tasks:[err(9,2),err(8,40)],has_more:true})]);
 try{
  expect(failedRow(panel).textContent).toContain('Failed tasks (24h): 1');
  expect(failedRow(panel).textContent).not.toContain('at least');
 }finally{w.close();}
});

test('a search that could not be completed never reads as nothing needs attention',async()=>{
 const {w,panel}=await mount([certs,health,answer({tasks:[],has_more:true,exact:false})]);
 try{
  expect(panel.textContent).not.toContain('Nothing needs attention');
  const row=failedRow(panel);
  expect(row).not.toBeNull();
  expect(row.textContent).toContain('Failed tasks (24h)');
  expect(row.textContent).toContain('Unknown');
  expect(row.textContent).toContain('could not check every task');
  expect(row.dataset.tone).toBe('notice');
 }finally{w.close();}
});

test('an incomplete search that still found failures reports at least that many',async()=>{
 const {w,panel}=await mount([certs,health,answer({tasks:[err(9,2)],has_more:true,exact:false})]);
 try{
  expect(failedRow(panel).textContent).toContain('Failed tasks (24h): at least 1');
 }finally{w.close();}
});

test('a complete empty answer is all clear',async()=>{
 const {w,panel}=await mount([certs,health,answer({})]);
 try{
  expect(panel.textContent).toContain('Nothing needs attention');
 }finally{w.close();}
});

test('an older broker that answers a bare array still counts',async()=>{
 const {w,panel}=await mount([certs,health,['/b/tasks',(u,o,json)=>json(200,[err(1,1),err(2,3)])]]);
 try{
  expect(failedRow(panel).textContent).toContain('Failed tasks (24h): 2');
 }finally{w.close();}
});
