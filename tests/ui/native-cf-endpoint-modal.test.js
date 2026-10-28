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

// The Cloud Foundry endpoint form is a native <dialog> that ModalManager opens
// with showModal. Every way out must leave the surface stack, inert, and
// aria-hidden as they were, and must put focus back on the opener.
const start=async()=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 const opener=d.createElement('button');opener.id='cf-opener';opener.textContent='Add';d.body.appendChild(opener);
 await sleep(60);
 opener.focus();
 const dialog=()=>d.getElementById('cf-endpoint-modal');
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
const button=(env,label)=>[...env.dialog().querySelectorAll('button')].find(b=>b.textContent.trim()===label);
const typeName=(env,value)=>{ const n=env.d.getElementById('cf-endpoint-name'); n.value=value; n.dispatchEvent(new env.w.Event('input',{bubbles:true})); };

t('the endpoint form opens as a native modal dialog on the surface stack with the name field focused',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
 const before=snapshot();
 w.showCFEndpointForm();
 expect(dialog() instanceof w.HTMLDialogElement).toBe(true);
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(dialog().getAttribute('aria-labelledby')).toBe('cf-endpoint-modal-title');
 expect(dialog().querySelector('.modal-overlay, .modal-content')).toBeNull();
 expect(dialog().hasAttribute('aria-hidden')).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 expect(d.activeElement.id).toBe('cf-endpoint-name');
 expect(env.opener.hasAttribute('inert')).toBe(true);
 expect(dialog().hasAttribute('inert')).toBe(false);
 w.hideCFEndpointForm(); await stubs.settle();
 expect(snapshot()).toBe(before);
});

const paths=[
 {name:'Escape',close:({key})=>key('Escape')},
 {name:'a click on the backdrop',close:({backdrop})=>backdrop()},
 {name:'a touch on the backdrop',close:({touch})=>touch()},
 {name:'the close button',close:(env)=>env.dialog().querySelector('.modal-close').click()},
 {name:'the Close button',close:(env)=>env.w.eval(button(env,'Close').getAttribute('onclick'))},
 {name:'the Discard draft button',close:(env)=>button(env,'Discard draft').click()}
];
paths.forEach(({name,close})=>{
 t(`${name} closes the form and restores the stack, inert, aria-hidden, and focus`,async()=>{
  const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
  const before=snapshot();
  w.showCFEndpointForm();
  expect(snapshot()).not.toBe(before);
  close(env);
  await stubs.settle();
  expect(dialog().open).toBe(false);
  expect(stubs.isModal(dialog())).toBe(false);
  expect(snapshot()).toBe(before);
  expect(d.activeElement).toBe(env.opener);
  expect(env.opener.hasAttribute('inert')).toBe(false);
  expect(d.body.style.overflow).toBe('');
 });
});

t('a draft survives every close except Discard draft',async()=>{
 const env=await start(); const {w,d,stubs}=env;
 w.showCFEndpointForm(); typeName(env,'keep'); env.key('Escape'); await stubs.settle(); await sleep(320);
 w.showCFEndpointForm(); expect(d.getElementById('cf-endpoint-name').value).toBe('keep');
 button(env,'Discard draft').click(); await stubs.settle(); await sleep(320);
 w.showCFEndpointForm(); expect(d.getElementById('cf-endpoint-name').value).toBe('');
 w.hideCFEndpointForm(); await stubs.settle();
});

t('a click or touch on the form content leaves the dialog open',async()=>{
 const env=await start(); const {w,d,dialog,stubs}=env;
 w.showCFEndpointForm();
 const inside=d.getElementById('cf-endpoint-form');
 inside.click();
 const e=new w.Event('touchstart',{bubbles:true,cancelable:true});
 inside.dispatchEvent(e);
 await stubs.settle();
 expect(e.defaultPrevented).toBe(false);
 expect(dialog().open).toBe(true);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 w.hideCFEndpointForm(); await stubs.settle();
});

t('Escape closes only the topmost surface when another surface sits above the form',async()=>{
 const env=await start(); const {w,dialog,stubs,key}=env;
 w.showCFEndpointForm();
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

t('a confirmation opened over the form returns to it, still modal, and Escape closes only the confirmation',async()=>{
 const env=await start(); const {w,d,dialog,stubs,key}=env;
 w.showCFEndpointForm();
 const field=d.getElementById('cf-endpoint-url'); field.focus();
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 expect(w.blacksmithSurfaces.size()).toBe(2);
 key('Escape');
 expect(await done).toBe(false);
 await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(dialog().hasAttribute('inert')).toBe(false);
 expect(env.opener.hasAttribute('inert')).toBe(true);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 expect(d.activeElement).toBe(field);
 key('Escape'); await stubs.settle();
 expect(dialog().open).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(0);
});

t('a late close event from a closed form does not close it after it reopens',async()=>{
 const env=await start(); const {w,dialog,stubs}=env;
 w.showCFEndpointForm();
 w.hideCFEndpointForm();
 w.showCFEndpointForm();
 await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 w.hideCFEndpointForm(); await stubs.settle();
});
