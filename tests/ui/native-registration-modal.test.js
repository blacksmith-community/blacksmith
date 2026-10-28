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

// The registration form is a native <dialog> opened with showModal. Every way out must leave the surface stack, inert, and
// aria-hidden as they were, and must put focus back on the opener.
const start=async()=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 const opener=d.createElement('button');opener.id='reg-opener';opener.textContent='Add';d.body.appendChild(opener);
 await sleep(60);
 opener.focus();
 const dialog=()=>d.getElementById('registration-modal');
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
const mgr=(env)=>env.w.__T.CFRegistrationManager;
const openIt=(env)=>mgr(env).showRegistrationModal();

t('the registration form opens as a native modal dialog on the surface stack with the name field focused',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
 const before=snapshot();
 openIt(env);
 expect(dialog() instanceof w.HTMLDialogElement).toBe(true);
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(dialog().getAttribute('aria-labelledby')).toBe('registration-modal-title');
 expect(dialog().querySelector('.modal-content')).toBeNull();
 expect(dialog().hasAttribute('aria-hidden')).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 expect(d.activeElement.id).toBe('reg-name');
 expect(JSON.parse(snapshot()).inert.length).toBeGreaterThan(0);
 expect(dialog().hasAttribute('inert')).toBe(false);
 mgr(env).hideRegistrationModal(); await stubs.settle();
 expect(snapshot()).toBe(before);
});

const paths=[
 {name:'Escape',close:({key})=>key('Escape')},
 {name:'a click on the backdrop',close:({backdrop})=>backdrop()},
 {name:'a touch on the backdrop',close:({touch})=>touch()},
 {name:'the close button',close:(env)=>env.dialog().querySelector('.modal-close').click()},
 {name:'the Cancel button',close:(env)=>env.d.getElementById('cancel-registration').click()}
];
paths.forEach(({name,close})=>{
 t(`${name} closes the registration form, resets it, and restores the stack, inert, and focus`,async()=>{
  const env=await start(); const {d,dialog,stubs,snapshot}=env;
  const before=snapshot();
  openIt(env);
  d.getElementById('reg-name').value='draft';
  expect(snapshot()).not.toBe(before);
  close(env);
  await stubs.settle();
  expect(dialog().open).toBe(false);
  expect(stubs.isModal(dialog())).toBe(false);
  expect(snapshot()).toBe(before);
  expect(d.getElementById('reg-name').value).toBe('');
  expect(d.activeElement).toBe(env.opener);
  expect(d.body.style.overflow).toBe('');
 });
});

t('a click or touch on the form leaves the registration dialog open',async()=>{
 const env=await start(); const {w,d,dialog,stubs}=env;
 openIt(env);
 const inside=d.getElementById('registration-form');
 inside.click();
 const e=new w.Event('touchstart',{bubbles:true,cancelable:true});
 inside.dispatchEvent(e);
 await stubs.settle();
 expect(e.defaultPrevented).toBe(false);
 expect(dialog().open).toBe(true);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 mgr(env).hideRegistrationModal(); await stubs.settle();
});

t('the progress view opens the dialog natively once, and its Close button closes it',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
 const before=snapshot();
 mgr(env).streamProgress=()=>{};
 mgr(env).showProgressStream('r1');
 expect(dialog().open).toBe(true);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 mgr(env).showProgressStream('r1');
 expect(w.blacksmithSurfaces.size()).toBe(1);
 expect(d.getElementById('registration-modal-title').textContent).toBe('Registration Progress');
 const close=[...dialog().querySelectorAll('.modal-footer button')].find(b=>b.textContent.trim()==='Close');
 close.click();
 await stubs.settle();
 expect(dialog().open).toBe(false);
 expect(snapshot()).toBe(before);
});

t('Escape closes only the topmost surface when another surface sits above the registration form',async()=>{
 const env=await start(); const {w,dialog,stubs,key}=env;
 openIt(env);
 const above={close(){}};
 w.blacksmithSurfaces.push(above);
 key('Escape'); await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(w.blacksmithSurfaces.size()).toBe(2);
 w.blacksmithSurfaces.remove(above);
 key('Escape'); await stubs.settle();
 expect(dialog().open).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(0);
});

t('a confirmation opened over the registration form returns to it, still modal, and a later confirmation is not dismissed by the form',async()=>{
 const env=await start(); const {w,d,dialog,stubs,key}=env;
 openIt(env);
 const field=d.getElementById('reg-api-url'); field.focus();
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 expect(w.blacksmithSurfaces.size()).toBe(2);
 expect(d.getElementById('dialog-modal').hasAttribute('inert')).toBe(false);
 key('Escape');
 expect(await done).toBe(false);
 await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(dialog().hasAttribute('inert')).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 expect(d.activeElement).toBe(field);
 const again=w.showConfirmDialog({title:'Again',message:'Again?'});
 await sleep(10);
 expect(dialog().open).toBe(true);
 d.querySelector('[data-dialog-role="cancel"]').click(); await again;
 await stubs.settle();
 key('Escape'); await stubs.settle();
 expect(dialog().open).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(0);
});
