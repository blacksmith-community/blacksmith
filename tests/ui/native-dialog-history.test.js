const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const OAT=fs.readFileSync(path.join(UI,'vendor','oat','oat.min.js'),'utf8');
const ROUTES=fs.readFileSync(path.join(UI,'routes.js'),'utf8');

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// These tests run the real router from ui/routes.js. Opening an inspection pushes a real history entry, and Back and
// Forward are real history traversals, so jsdom fires the popstate and hashchange events the router listens for.
const start=async()=>{
 const env=boot({noDialogs:true,routes:[
  ['/b/tasks/42',(u,o,j)=>j(200,{task:{id:42,state:'done',description:'a task'}})],
  ['/b/configs/42',(u,o,j)=>j(200,{id:'42',name:'default',type:'cloud',team:'t',created_at:'2026-10-24T10:00:00Z',content:'azs: []',metadata:{}})],
  ['/b/configs/cloud/default/versions',(u,o,j)=>j(200,{configs:[{id:'42*',name:'default',type:'cloud'}],count:1})],
  ['/b/certificates/items/c1',(u,o,j)=>j(200,{id:'c1',name:'Broker certificate',findings:[],owners:[]})],
  ['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],
  ['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 w.eval(ROUTES);
 await sleep(100);
 expect(typeof w.blacksmithRouter?.inspect).toBe('function');
 const events=[];
 w.addEventListener('popstate',()=>events.push('popstate'));
 w.addEventListener('hashchange',()=>events.push('hashchange'));
 const snapshot=()=>JSON.stringify({
  surfaces:w.blacksmithSurfaces.size(),
  inert:[...d.querySelectorAll('[inert]')].map(n=>n.id||n.tagName),
  hidden:[...d.querySelectorAll('[aria-hidden]')].filter(n=>!n.closest('dialog')).map(n=>(n.id||n.tagName)+'='+n.getAttribute('aria-hidden'))
 });
 const traverse=async(direction)=>{ events.length=0; w.history[direction](); await sleep(150); };
 return {...env,events,snapshot,traverse};
};

const kinds=[
 {name:'task details',id:'task-details-modal',hash:'inspect=task&id=42',open:(w)=>w.showTaskDetails(42)},
 {name:'config details',id:'config-details-modal',hash:'inspect=config&id=42',open:(w)=>w.showConfigVersions('cloud','default','42')}
];
kinds.forEach(({name,id,hash,open})=>{
 t(`Back closes the ${name} view through the router, and Forward opens it again`,async()=>{
  const env=await start(); const {w,d,stubs,snapshot,traverse,events}=env;
  const dialog=()=>d.getElementById(id);
  const before=snapshot();
  await open(w); await sleep(100);
  expect(dialog().open).toBe(true);
  expect(w.location.hash).toContain(hash);
  expect(w.blacksmithRouter.current.inspection).toBeTruthy();
  expect(w.blacksmithSurfaces.size()).toBe(1);

  await traverse('back');
  expect(events).toContain('popstate');
  expect(w.location.hash).not.toContain('inspect=');
  await stubs.settle();
  expect(dialog().open).toBe(false);
  expect(w.blacksmithRouter.current.inspection).toBeUndefined();
  expect(snapshot()).toBe(before);
  expect(d.querySelector('dialog[open]')).toBeNull();

  await traverse('forward');
  expect(events).toContain('popstate');
  expect(w.location.hash).toContain(hash);
  await stubs.settle();
  expect(dialog().open).toBe(true);
  expect(stubs.isModal(dialog())).toBe(true);
  expect(w.blacksmithSurfaces.size()).toBe(1);

  await traverse('back'); await stubs.settle();
  expect(dialog().open).toBe(false);
  expect(snapshot()).toBe(before);
 });

 t(`Forward to the ${name} view while a confirmation is open stacks the dialog above it`,async()=>{
  const env=await start(); const {w,d,stubs,snapshot,traverse}=env;
  const dialog=()=>d.getElementById(id);
  const before=snapshot();
  await open(w); await sleep(100);
  await traverse('back'); await stubs.settle();
  expect(dialog().open).toBe(false);
  const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
  await sleep(10);
  expect(d.getElementById('dialog-modal').open).toBe(true);

  await traverse('forward'); await stubs.settle();
  expect(dialog().open).toBe(true);
  expect(stubs.isModal(dialog())).toBe(true);
  expect(dialog().hasAttribute('inert')).toBe(false);
  expect(w.blacksmithSurfaces.size()).toBe(2);

  await traverse('back'); await stubs.settle();
  expect(dialog().open).toBe(false);
  d.querySelector('[data-dialog-role="cancel"]').click(); expect(await done).toBe(false);
  await stubs.settle();
  expect(d.querySelector('dialog[open]')).toBeNull();
  expect(snapshot()).toBe(before);
 });
});

// The certificate inspector is the fleet drawer, which the router opens and closes as an inspection too.
t('Back closes the certificate inspector through the router, and Forward opens it again',async()=>{
 const env=await start(); const {w,d,stubs,snapshot,traverse,events}=env;
 const drawer=()=>d.getElementById('cert-fleet-drawer');
 const shown=()=>!!drawer()&&!drawer().hidden&&drawer().classList.contains('cert-fleet-drawer--open');
 const before=snapshot();
 await w.blacksmithRouter.inspect({kind:'certificate',id:'c1'}); await sleep(100);
 expect(shown()).toBe(true);
 expect(w.location.hash).toContain('inspect=certificate&id=c1');
 expect(w.blacksmithRouter.current.inspection).toBeTruthy();
 expect(w.blacksmithSurfaces.size()).toBe(1);

 await traverse('back'); await stubs.settle();
 expect(events).toContain('popstate');
 expect(w.location.hash).not.toContain('inspect=');
 expect(shown()).toBe(false);
 expect(w.blacksmithRouter.current.inspection).toBeUndefined();
 expect(w.blacksmithSurfaces.size()).toBe(0);
 expect(snapshot()).toBe(before);

 await traverse('forward'); await stubs.settle();
 expect(events).toContain('popstate');
 expect(w.location.hash).toContain('inspect=certificate&id=c1');
 expect(shown()).toBe(true);
 expect(w.blacksmithSurfaces.size()).toBe(1);

 await traverse('back'); await stubs.settle();
 expect(shown()).toBe(false);
 expect(snapshot()).toBe(before);
});

t('Back through the router closes the task details view but leaves the open terminal in place',async()=>{
 const env=await start(); const {w,d,stubs,traverse,snapshot}=env;
 w.WTermLib={WTerm:class{constructor(el){this.cols=80;this.rows=24;el.classList.add('wterm');}init(){return Promise.resolve(this);}focus(){}write(){}setRenderingPaused(){}destroy(){}},version:'0.5.10'};
 w.WebSocket=class{static get OPEN(){return 1;}constructor(){this.readyState=0;}send(){}close(){}};
 const before=snapshot();
 await w.showTaskDetails(42); await sleep(100);
 expect(d.getElementById('task-details-modal').open).toBe(true);
 w.TerminalManager.openWithContext('blacksmith','broker','node/0','node','broker');
 const terminal=()=>d.getElementById('terminal-modal');
 expect(terminal().open).toBe(true);

 await traverse('back'); await stubs.settle();
 expect(w.location.hash).not.toContain('inspect=');
 expect(d.getElementById('task-details-modal').open).toBe(false);
 expect(terminal().open).toBe(true);
 expect(stubs.isModal(terminal())).toBe(true);
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 expect(d.getElementById('dialog-modal').open).toBe(true);
 expect(d.getElementById('dialog-modal').hasAttribute('inert')).toBe(false);
 d.querySelector('[data-dialog-role="cancel"]').click(); expect(await done).toBe(false);
 await stubs.settle();
 expect(terminal().open).toBe(true);
 expect(terminal().hasAttribute('inert')).toBe(false);
 w.TerminalManager.closeAll(); await stubs.settle();
 expect(snapshot()).toBe(before);
});
