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


// A confirmation that has been shown once leaves a closed dialog in the page.
// A modal that opens afterwards must not leave that element inert.
t('a confirmation opened over a modal that opened after the first one is usable and closes cleanly',async()=>{
 const env=await start(); const {w,d,dialog,role,stubs,snapshot}=env;
 const first=w.showConfirmDialog({title:'First',message:'First?'});
 await sleep(10);
 role('cancel').click(); await first; await stubs.settle();
 const before=snapshot();
 env.opener.focus();
 w.__T.CFRegistrationManager.showRegistrationModal();
 await sleep(20);
 const reg=d.getElementById('registration-modal');
 expect(w.blacksmithSurfaces.size()).toBe(1);
 const field=d.getElementById('reg-name'); field.focus();
 const done=w.showConfirmDialog({title:'Second',message:'Second?'});
 await sleep(10);
 expect(dialog().open).toBe(true);
 expect(dialog().hasAttribute('inert')).toBe(false);
 expect(reg.hasAttribute('inert')).toBe(true);
 expect(d.activeElement).toBe(role('cancel'));
 role('confirm').click();
 expect(await done).toBe(true);
 await stubs.settle();
 expect(reg.hasAttribute('inert')).toBe(false);
 expect(w.blacksmithSurfaces.size()).toBe(1);
 expect(d.activeElement).toBe(field);
 w.__T.CFRegistrationManager.hideRegistrationModal();
 await stubs.settle();
 expect(snapshot()).toBe(before);
});

t('a confirmation that was inert when it opened is inert again once it closes',async()=>{
 const env=await start(); const {w,d,dialog,role,stubs}=env;
 const first=w.showConfirmDialog({title:'First',message:'First?'});
 await sleep(10);
 role('cancel').click(); await first; await stubs.settle();
 dialog().setAttribute('inert','');
 const done=w.showConfirmDialog({title:'Second',message:'Second?'});
 await sleep(10);
 expect(dialog().hasAttribute('inert')).toBe(false);
 role('cancel').click(); await done; await stubs.settle();
 expect(dialog().hasAttribute('inert')).toBe(true);
});

// A real showModal() makes every node outside the dialog inert, and an inert
// node is not announced. Toasts and the live region have to sit inside the
// topmost open modal dialog, and go back to the body when it closes.
const toastBox=(d)=>d.querySelector('.toast-container');
const live=(d)=>d.getElementById('blacksmith-live-region');

t('toasts and the live region move into an open confirmation and back to the body when it closes',async()=>{
 const env=await start(); const {w,d,dialog,role,stubs}=env;
 w.showNotification('Before','info');
 w.blacksmithNav.announce('Hello'); await sleep(70);
 expect(toastBox(d).parentNode).toBe(d.body);
 expect(live(d).parentNode).toBe(d.body);
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 expect(toastBox(d).parentNode).toBe(dialog());
 expect(live(d).parentNode).toBe(dialog());
 w.showNotification('During','error');
 expect(toastBox(d).parentNode).toBe(dialog());
 expect(toastBox(d).querySelectorAll('output.toast').length).toBe(2);
 role('cancel').click(); await done; await stubs.settle();
 expect(toastBox(d).parentNode).toBe(d.body);
 expect(live(d).parentNode).toBe(d.body);
 expect(toastBox(d).querySelectorAll('output.toast').length).toBe(2);
});

t('a toast container made while a modal is open is moved inside it, and a live region made then too',async()=>{
 const env=await start(); const {w,d,dialog,role,stubs}=env;
 expect(toastBox(d)).toBeNull();
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 w.showNotification('First toast','success');
 w.blacksmithNav.announce('Fresh region'); await sleep(70);
 expect(toastBox(d).parentNode).toBe(dialog());
 expect(live(d).parentNode).toBe(dialog());
 expect(live(d).textContent).toBe('Fresh region');
 role('cancel').click(); await done; await stubs.settle();
 expect(toastBox(d).parentNode).toBe(d.body);
 expect(live(d).parentNode).toBe(d.body);
});

t('with two native modals open the announcers sit in the topmost one and return to the one beneath',async()=>{
 const env=await start(); const {w,d,role,stubs}=env;
 w.showNotification('Base','info');
 w.showCFEndpointForm();
 const form=d.getElementById('cf-endpoint-modal');
 expect(toastBox(d).parentNode).toBe(form);
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 expect(toastBox(d).parentNode).toBe(d.getElementById('dialog-modal'));
 role('cancel').click(); await done; await stubs.settle();
 expect(toastBox(d).parentNode).toBe(form);
 w.hideCFEndpointForm(); await stubs.settle();
 expect(toastBox(d).parentNode).toBe(d.body);
});
