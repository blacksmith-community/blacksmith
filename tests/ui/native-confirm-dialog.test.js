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

// The confirmation and prompt dialogs share one native <dialog>. Escape, a
// tap or click on the backdrop, and every button must leave the surface stack,
// inert, and aria-hidden as they were, and must put focus back on the opener.
const start=async()=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 const opener=d.createElement('button');opener.id='dlg-opener';opener.textContent='Open';d.body.appendChild(opener);
 await sleep(60);
 opener.focus();
 const dialog=()=>d.getElementById('dialog-modal');
 const role=(r)=>dialog().querySelector(`[data-dialog-role="${r}"]`);
 // What the page looks like to the stack, the focus tree, and a screen reader.
 const snapshot=()=>JSON.stringify({
  surfaces:w.blacksmithSurfaces.size(),
  inert:[...d.querySelectorAll('[inert]')].map(n=>n.id||n.tagName),
  hidden:[...d.querySelectorAll('[aria-hidden]')].filter(n=>!n.closest('#dialog-modal')).map(n=>(n.id||n.tagName)+'='+n.getAttribute('aria-hidden'))
 });
 const key=(k)=>d.activeElement.dispatchEvent(new w.KeyboardEvent('keydown',{key:k,bubbles:true,cancelable:true}));
 const backdrop=()=>dialog().dispatchEvent(new w.MouseEvent('click',{bubbles:true,cancelable:true}));
 const touch=()=>dialog().dispatchEvent(new w.Event('touchstart',{bubbles:true,cancelable:true}));
 return {...env,opener,dialog,role,snapshot,key,backdrop,touch};
};

t('the confirmation opens as a native modal dialog on the surface stack with Cancel focused',async()=>{
 const env=await start(); const {w,d,dialog,role,stubs,snapshot}=env;
 const before=snapshot();
 const done=w.showConfirmDialog({title:'Delete it',message:'Really delete?',danger:true});
 await sleep(10);
 expect(dialog() instanceof w.HTMLDialogElement).toBe(true);
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 expect(dialog().querySelector('.dialog-modal-title').textContent).toBe('Delete it');
 expect(d.activeElement).toBe(role('cancel'));
 expect(env.opener.hasAttribute('inert')).toBe(true);
 expect(dialog().hasAttribute('inert')).toBe(false);
 role('cancel').click(); await done;
 expect(snapshot()).toBe(before);
});

const paths=[
 {name:'Escape',expected:false,close:({key})=>key('Escape')},
 {name:'a click on the backdrop',expected:false,close:({backdrop})=>backdrop()},
 {name:'a touch on the backdrop',expected:false,close:({touch})=>touch()},
 {name:'the Cancel button',expected:false,close:({role})=>role('cancel').click()},
 {name:'the Confirm button',expected:true,close:({role})=>role('confirm').click()}
];
paths.forEach(({name,expected,close})=>{
 t(`${name} resolves ${expected} and restores the stack, inert, aria-hidden, and focus`,async()=>{
  const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
  const before=snapshot();
  const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
  await sleep(10);
  expect(snapshot()).not.toBe(before);
  close(env);
  expect(await done).toBe(expected);
  await stubs.settle();
  expect(dialog().open).toBe(false);
  expect(stubs.isModal(dialog())).toBe(false);
  expect(snapshot()).toBe(before);
  expect(d.activeElement).toBe(env.opener);
  expect(env.opener.hasAttribute('inert')).toBe(false);
 });
});

t('a click or touch on the content leaves the confirmation open',async()=>{
 const env=await start(); const {w,d,dialog,stubs}=env;
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 dialog().querySelector('.dialog-modal-message').click();
 const e=new w.Event('touchstart',{bubbles:true,cancelable:true});
 dialog().querySelector('.dialog-modal-message').dispatchEvent(e);
 await stubs.settle();
 expect(e.defaultPrevented).toBe(false);
 expect(dialog().open).toBe(true);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 dialog().querySelector('[data-dialog-role="cancel"]').click(); await done;
});

t('the typed-word confirmation stays open on Enter with the wrong word and still cancels on Escape',async()=>{
 const env=await start(); const {w,d,dialog,stubs,key}=env;
 const done=w.showConfirmDialog({title:'Wipe',message:'Wipe it?',requireText:'wipe'});
 await sleep(10);
 const ack=dialog().querySelector('.dialog-modal-ack-input');
 expect(d.activeElement).toBe(ack);
 ack.value='nope';
 key('Enter');
 await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(dialog().querySelector('.dialog-modal-error').hidden).toBe(false);
 key('Escape');
 expect(await done).toBe(false);
 expect(dialog().querySelector('.dialog-modal-ack')).toBeNull();
});

t('Escape closes only the topmost surface when another surface sits above the dialog',async()=>{
 const env=await start(); const {w,dialog,stubs,key}=env;
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 const above={close(){}};
 w.blacksmithSurfaces.push(above);
 key('Escape'); await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(w.blacksmithSurfaces.size()).toBe(2);
 w.blacksmithSurfaces.remove(above);
 key('Escape');
 expect(await done).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(0);
});

t('a second confirmation dismisses the first, and a late close event from the first does not close the second',async()=>{
 const env=await start(); const {w,d,dialog,role,stubs,snapshot}=env;
 const before=snapshot();
 const first=w.showConfirmDialog({title:'One',message:'First?'});
 await sleep(10);
 const second=w.showConfirmDialog({title:'Two',message:'Second?'});
 expect(await first).toBe(false);
 await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(dialog().querySelector('.dialog-modal-title').textContent).toBe('Two');
 expect(w.blacksmithSurfaces.size()).toBe(1);
 role('confirm').click();
 expect(await second).toBe(true);
 await stubs.settle();
 expect(snapshot()).toBe(before);
 expect(d.activeElement).toBe(env.opener);
});

t('closing a confirmation opened over a modal returns to that modal, still modal and focused',async()=>{
 const env=await start(); const {w,d,role,stubs,snapshot}=env;
 d.body.insertAdjacentHTML('beforeend','<div id="outer" class="modal-overlay" role="dialog" aria-label="Outer"><button id="outer-btn">Inside</button></div>');
 const outer=d.getElementById('outer');
 const before=snapshot();
 expect(w.ModalManager.openModal(outer)).toBe(true);
 const outerOpen=snapshot();
 d.getElementById('outer-btn').focus();
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 expect(w.blacksmithSurfaces.size()).toBe(2);
 expect(outer.hasAttribute('inert')).toBe(true);
 role('cancel').click();
 expect(await done).toBe(false);
 await stubs.settle();
 expect(snapshot()).toBe(outerOpen);
 expect(outer.hasAttribute('inert')).toBe(false);
 expect(w.ModalManager.isTopmost()).toBe(true);
 expect(d.activeElement).toBe(d.getElementById('outer-btn'));
 w.ModalManager.closeModal(outer);
 expect(snapshot()).toBe(before);
});

t('the prompt cancels with null on Escape and on a backdrop click, and returns the value on Enter',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot,key,backdrop}=env;
 const before=snapshot();
 const input=()=>dialog().querySelector('.dialog-modal-input');
 let done=w.__T.showPromptDialog({title:'Name',message:'Name?',defaultValue:'abc'});
 await sleep(10);
 expect(dialog().open).toBe(true);
 expect(d.activeElement).toBe(input());
 key('Escape');
 expect(await done).toBeNull();
 await stubs.settle();
 expect(snapshot()).toBe(before);
 expect(input().value).toBe('');
 done=w.__T.showPromptDialog({title:'Name',message:'Name?'});
 await sleep(10);
 backdrop();
 expect(await done).toBeNull();
 await stubs.settle();
 expect(snapshot()).toBe(before);
 done=w.__T.showPromptDialog({title:'Secret',message:'Password?',secret:true});
 await sleep(10);
 expect(input().getAttribute('type')).toBe('password');
 input().value='hunter2';
 key('Enter');
 expect(await done).toBe('hunter2');
 await stubs.settle();
 expect(input().value).toBe('');
 expect(input().getAttribute('type')).toBe('text');
 expect(snapshot()).toBe(before);
 expect(d.activeElement).toBe(env.opener);
});

// A native dialog is hidden by the browser until it opens, so the flex layout
// belongs to the open state only. The old overlay classes have no readers left.
test('the dialog lays out as flex only while open, and no overlay class remains',()=>{
 const UI=path.resolve(__dirname,'..','..','ui');
 const css=fs.readFileSync(path.join(UI,'blacksmith.css'),'utf8');
 const rule=(selector)=>{ const start=css.indexOf('\n'+selector+' {'); expect(start).toBeGreaterThan(-1); return css.slice(start,css.indexOf('}',start)); };
 expect(rule('.dialog-modal')).not.toMatch(/display:/);
 expect(rule('.dialog-modal[open]')).toMatch(/display: flex/);
 const found=[];
 fs.readdirSync(UI).filter(f=>/\.(js|css|html)$/.test(f)).forEach(f=>{ if(/dialog-modal-overlay/.test(fs.readFileSync(path.join(UI,f),'utf8'))) found.push(f); });
 expect(found).toEqual([]);
});
