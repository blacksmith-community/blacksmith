const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const OAT=fs.readFileSync(path.resolve(__dirname,'..','..','ui','vendor','oat','oat.min.js'),'utf8');

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// The task details view is a native <dialog> opened with showModal. Every way out must leave the surface stack, inert, and
// aria-hidden as they were, and must put focus back on the opener.
const start=async()=>{
 const env=boot({noDialogs:true,routes:[['/b/tasks/42',(u,o,j)=>j(200,{task:{id:42,state:'done',description:'a task'}})],['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 const opener=d.createElement('button');opener.id='task-opener';opener.textContent='Task';d.body.appendChild(opener);
 await sleep(60);
 opener.focus();
 const dialog=()=>d.getElementById('task-details-modal');
 const snapshot=()=>JSON.stringify({
  surfaces:w.blacksmithSurfaces.size(),
  inert:[...d.querySelectorAll('[inert]')].map(n=>n.id||n.tagName),
  hidden:[...d.querySelectorAll('[aria-hidden]')].filter(n=>!n.closest('dialog')).map(n=>(n.id||n.tagName)+'='+n.getAttribute('aria-hidden'))
 });
 const key=(k)=>d.activeElement.dispatchEvent(new w.KeyboardEvent('keydown',{key:k,bubbles:true,cancelable:true}));
 const backdrop=()=>dialog().dispatchEvent(new w.MouseEvent('click',{bubbles:true,cancelable:true}));
 const touch=()=>dialog().dispatchEvent(new w.Event('touchstart',{bubbles:true,cancelable:true}));
 return {...env,opener,dialog,snapshot,key,backdrop,touch};
};
const openIt=async(env)=>{ const p=env.w.showTaskDetails(42); await sleep(30); return p; };
const closeIt=(env)=>env.w.hideTaskDetailsModal();

t('the task details view opens as a native modal dialog on the surface stack',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
 const before=snapshot();
 await openIt(env);
 expect(dialog() instanceof w.HTMLDialogElement).toBe(true);
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(dialog().getAttribute('aria-labelledby')).toBe('task-details-modal-title');
 expect(dialog().querySelector('.modal-content')).toBeNull();
 expect(dialog().hasAttribute('aria-hidden')).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 expect(dialog().contains(d.activeElement)).toBe(true);
 expect(d.body.style.overflow).toBe('hidden');
 expect(d.getElementById('task-description').textContent).toBe('a task');
 closeIt(env); await stubs.settle();
 expect(snapshot()).toBe(before);
 expect(w.taskModalAutoRefresh).toBeFalsy();
});

const paths=[
 {name:'Escape',close:({key})=>key('Escape')},
 {name:'a click on the backdrop',close:({backdrop})=>backdrop()},
 {name:'a touch on the backdrop',close:({touch})=>touch()},
 {name:'the close button',close:(env)=>env.dialog().querySelector('.modal-close').click()}
];
paths.forEach(({name,close})=>{
 t(`${name} closes the task details view and restores the stack, inert, and focus`,async()=>{
  const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
  const before=snapshot();
  await openIt(env);
  expect(snapshot()).not.toBe(before);
  close(env);
  await stubs.settle();
  expect(dialog().open).toBe(false);
  expect(stubs.isModal(dialog())).toBe(false);
  expect(snapshot()).toBe(before);
  expect(d.activeElement).toBe(env.opener);
  expect(d.body.style.overflow).toBe('');
  expect(w.currentTaskId).toBeFalsy();
 });
});

t('a click or touch inside the task details view leaves it open',async()=>{
 const env=await start(); const {w,d,dialog,stubs}=env;
 await openIt(env);
 const inside=d.getElementById('task-details-content');
 inside.click();
 const e=new w.Event('touchstart',{bubbles:true,cancelable:true});
 inside.dispatchEvent(e);
 await stubs.settle();
 expect(e.defaultPrevented).toBe(false);
 expect(dialog().open).toBe(true);
 closeIt(env); await stubs.settle();
});

t('a confirmation opened over the task details view returns to it, still modal',async()=>{
 const env=await start(); const {w,d,dialog,stubs,key}=env;
 await openIt(env);
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 expect(w.blacksmithSurfaces.size()).toBe(2);
 key('Escape');
 expect(await done).toBe(false);
 await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(dialog().hasAttribute('inert')).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 key('Escape'); await stubs.settle();
 expect(dialog().open).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(0);
});

t('a slow load for an earlier task cannot restart polling after a later task has loaded',async()=>{
 const env=await start(); const {w,d,stubs}=env;
 let finish;
 w.__routes.unshift(['/b/tasks/41',(u,o,j)=>new Promise(resolve=>{finish=()=>resolve(j(200,{task:{id:41,state:'processing',description:'earlier task'}}));})]);
 w.__routes.unshift(['/b/tasks/42',(u,o,j)=>j(200,{task:{id:42,state:'done',description:'a task'}})]);
 const first=w.showTaskDetails(41); await sleep(10);
 await w.showTaskDetails(42); await sleep(30);
 const handle=w.taskModalAutoRefresh;
 expect(handle).toBeTruthy();
 finish(); await first; await sleep(30);
 expect(w.taskModalAutoRefresh).toBe(handle);
 w.hideTaskDetailsModal(); await stubs.settle();
});
