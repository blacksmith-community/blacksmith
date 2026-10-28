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

// The certificate inspector is a native <dialog> that ModalManager opens
// with showModal. Every way out must leave the surface stack, inert, and
// aria-hidden as they were, and must put focus back on the opener.
const start=async()=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 const opener=d.createElement('button');opener.id='inspect-opener';opener.textContent='Add';d.body.appendChild(opener);
 await sleep(60);
 opener.focus();
 const dialog=()=>d.querySelector('dialog[id^="cert-modal-"]');
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
const CERT={subject:'CN=broker.example',issuer:'CN=Example CA',pemEncoded:'-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----',textDetails:'Certificate:\n  Data:'};
const open=(env)=>env.w.inspectCertificate(CERT);

t('the inspector opens as a native modal dialog on the surface stack and keeps its tabs',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
 const before=snapshot();
 open(env);
 expect(dialog() instanceof w.HTMLDialogElement).toBe(true);
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(dialog().getAttribute('aria-labelledby')).toBe(dialog().id+'-title');
 expect(dialog().querySelector('.modal-overlay, .modal[role=dialog]')).toBeNull();
 expect(dialog().hasAttribute('aria-hidden')).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 expect(dialog().querySelectorAll('.cert-tab-btn').length).toBe(3);
 expect(dialog().querySelector('.cert-tab-pane[data-tab="details"]').textContent).toContain('broker.example');
 expect(env.opener.hasAttribute('inert')).toBe(true);
 expect(dialog().hasAttribute('inert')).toBe(false);
 dialog().querySelector('.modal-close').click(); await stubs.settle();
 expect(snapshot()).toBe(before);
});

const paths=[
 {name:'Escape',close:({key})=>key('Escape')},
 {name:'a click on the backdrop',close:({backdrop})=>backdrop()},
 {name:'a touch on the backdrop',close:({touch})=>touch()},
 {name:'the close button',close:(env)=>env.dialog().querySelector('.modal-close').click()}
];
paths.forEach(({name,close})=>{
 t(`${name} closes the inspector and restores the stack, inert, aria-hidden, and focus`,async()=>{
  const env=await start(); const {d,dialog,stubs,snapshot}=env;
  const before=snapshot();
  open(env);
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

t('a click or touch on the certificate content leaves the inspector open',async()=>{
 const env=await start(); const {w,dialog,stubs}=env;
 open(env);
 const inside=dialog().querySelector('.modal-body');
 inside.click();
 const e=new w.Event('touchstart',{bubbles:true,cancelable:true});
 inside.dispatchEvent(e);
 await stubs.settle();
 expect(e.defaultPrevented).toBe(false);
 expect(dialog().open).toBe(true);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 dialog().querySelector('.modal-close').click(); await stubs.settle();
});

t('the tab buttons still switch the visible pane',async()=>{
 const env=await start(); const {dialog,stubs}=env;
 open(env);
 dialog().querySelector('.cert-tab-btn[data-tab="pem"]').click();
 expect(dialog().querySelector('.cert-tab-pane[data-tab="pem"]').hidden).toBe(false);
 expect(dialog().querySelector('.cert-tab-pane[data-tab="details"]').hidden).toBe(true);
 expect(dialog().querySelector('.cert-tab-btn[data-tab="pem"]').getAttribute('aria-selected')).toBe('true');
 expect(dialog().open).toBe(true);
 dialog().querySelector('.modal-close').click(); await stubs.settle();
});

t('Escape closes only the topmost surface when another surface sits above the inspector',async()=>{
 const env=await start(); const {w,dialog,stubs,key}=env;
 open(env);
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

t('a confirmation opened over the inspector returns to it, still modal, and Escape closes only the confirmation',async()=>{
 const env=await start(); const {w,d,dialog,stubs,key}=env;
 open(env);
 const tab=dialog().querySelector('.cert-tab-btn[data-tab="text"]'); tab.focus();
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
 expect(d.activeElement).toBe(tab);
 key('Escape'); await stubs.settle();
 expect(dialog().open).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(0);
});
