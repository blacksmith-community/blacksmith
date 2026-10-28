const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const H=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;
const {boot,sleep}=H.fleet;

const OAT=fs.readFileSync(path.resolve(__dirname,'..','..','ui','vendor','oat','oat.min.js'),'utf8');

// The fleet harness has no catalog route, so the page's own catalog load logs one 404 at boot.
const CATALOG=/^Error loading data: Error: HTTP 404/;
const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.errors.filter(e=>!CATALOG.test(e))).toEqual([]); } finally { env.w.close(); } }
});

// The certificate settings form is a native <dialog> that ModalManager opens
// with showModal. Every way out must leave the surface stack, inert, and
// aria-hidden as they were, and must put focus back on the Settings button.
const start=async()=>{
 const env=await boot(); envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 await w.blacksmithNav.switchTab('certificates'); await sleep(120);
 const opener=d.getElementById('cert-settings-btn');
 opener.focus();
 const dialog=()=>d.getElementById('cert-settings-modal');
 const snapshot=()=>JSON.stringify({
  surfaces:w.blacksmithSurfaces.size(),
  inert:[...d.querySelectorAll('[inert]')].map(n=>n.id||n.tagName),
  hidden:[...d.querySelectorAll('[aria-hidden]')].filter(n=>!n.closest('dialog')).map(n=>(n.id||n.tagName)+'='+n.getAttribute('aria-hidden'))
 });
 const key=(k)=>d.activeElement.dispatchEvent(new w.KeyboardEvent('keydown',{key:k,bubbles:true,cancelable:true}));
 const backdrop=()=>dialog().dispatchEvent(new w.MouseEvent('click',{bubbles:true,cancelable:true}));
 const touch=()=>dialog().dispatchEvent(new w.Event('touchstart',{bubbles:true,cancelable:true}));
 const open=async()=>{ opener.click(); await sleep(100); };
 return {...env,opener,dialog,snapshot,key,backdrop,touch,open};
};
const cancel=(env)=>[...env.dialog().querySelectorAll('.cert-settings-actions button')].find(b=>b.textContent==='Cancel');

t('the settings form opens as a native modal dialog on the surface stack with the first field focused',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot,open}=env;
 await sleep(60);
 const before=snapshot();
 await open();
 expect(dialog() instanceof w.HTMLDialogElement).toBe(true);
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(dialog().getAttribute('aria-labelledby')).toBe('cert-settings-title');
 expect(dialog().querySelector('.modal-overlay, .modal[role=dialog]')).toBeNull();
 expect(dialog().hasAttribute('aria-hidden')).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 expect(dialog().querySelectorAll('input').length).toBe(8);
 expect(d.activeElement).toBe(d.getElementById('cert-setting-warning_days'));
 expect(JSON.parse(env.snapshot()).inert.length).toBeGreaterThan(0);
 expect(dialog().hasAttribute('inert')).toBe(false);
 cancel(env).click(); await stubs.settle(); await sleep(320);
 expect(snapshot()).toBe(before);
});

const paths=[
 {name:'Escape',close:({key})=>key('Escape')},
 {name:'a click on the backdrop',close:({backdrop})=>backdrop()},
 {name:'a touch on the backdrop',close:({touch})=>touch()},
 {name:'the close button',close:(env)=>env.dialog().querySelector('.modal-close').click()},
 {name:'the Cancel button',close:(env)=>cancel(env).click()}
];
paths.forEach(({name,close})=>{
 t(`${name} closes the settings form and restores the stack, inert, aria-hidden, and focus`,async()=>{
  const env=await start(); const {d,dialog,stubs,snapshot,open}=env;
  await sleep(60);
  const before=snapshot();
  await open();
  expect(snapshot()).not.toBe(before);
  const shown=dialog();
  close(env);
  await stubs.settle();
  expect(shown.open).toBe(false);
  expect(stubs.isModal(shown)).toBe(false);
  expect(snapshot()).toBe(before);
  expect(d.activeElement).toBe(env.opener);
  expect(env.opener.hasAttribute('inert')).toBe(false);
  expect(d.body.style.overflow).toBe('');
 });
});

t('a click or touch on the form leaves the settings dialog open',async()=>{
 const env=await start(); const {w,dialog,stubs,open}=env;
 await open();
 const inside=dialog().querySelector('form');
 inside.click();
 const e=new w.Event('touchstart',{bubbles:true,cancelable:true});
 inside.dispatchEvent(e);
 await stubs.settle();
 expect(e.defaultPrevented).toBe(false);
 expect(dialog().open).toBe(true);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 cancel(env).click(); await stubs.settle();
});

t('a saved form closes the dialog and restores focus to the Settings button',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot,open}=env;
 await sleep(60);
 const before=snapshot();
 await open();
 const shown=dialog();
 dialog().querySelector('form').dispatchEvent(new w.Event('submit',{bubbles:true,cancelable:true}));
 await sleep(120); await stubs.settle();
 expect(shown.open).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(0);
 expect(d.activeElement).toBe(env.opener);
 expect(snapshot()).toBe(before);
});

t('Escape closes only the topmost surface when another surface sits above the settings form',async()=>{
 const env=await start(); const {w,dialog,stubs,key,open}=env;
 await open();
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

t('a confirmation opened over the settings form returns to it, still modal, and Escape closes only the confirmation',async()=>{
 const env=await start(); const {w,d,dialog,stubs,key,open}=env;
 await open();
 const field=d.getElementById('cert-setting-critical_days'); field.focus();
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 expect(w.blacksmithSurfaces.size()).toBe(2);
 key('Escape');
 expect(await done).toBe(false);
 await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(dialog().hasAttribute('inert')).toBe(false);
 expect(JSON.parse(env.snapshot()).inert.length).toBeGreaterThan(0);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 expect(d.activeElement).toBe(field);
 key('Escape'); await stubs.settle();
 expect(dialog().open).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(0);
});
