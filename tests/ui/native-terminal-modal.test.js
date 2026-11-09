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

// The terminal is a native <dialog> opened with showModal. Every way out must leave the surface stack, inert, and
// aria-hidden as they were, and must put focus back on the opener.
const start=async()=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 w.WTermLib={WTerm:class{constructor(el){this.cols=80;this.rows=24;el.classList.add('wterm');}init(){return Promise.resolve(this);}focus(){}write(){}setRenderingPaused(){}destroy(){}},version:'0.5.10'};
 w.WebSocket=class{static get OPEN(){return 1;}constructor(){this.readyState=0;}send(){}close(){}};
 const opener=d.createElement('button');opener.id='term-opener';opener.textContent='Open';d.body.appendChild(opener);
 await sleep(60);
 opener.focus();
 const dialog=()=>d.getElementById('terminal-modal');
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
const mgr=(env)=>env.w.TerminalManager;
const openIt=(env)=>{ env.opener.focus(); mgr(env).openWithContext('blacksmith','broker','node/0','node','broker'); };

t('the terminal opens as a native modal dialog and isolates the page behind it',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
 const before=snapshot();
 openIt(env);
 expect(dialog() instanceof w.HTMLDialogElement).toBe(true);
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(dialog().getAttribute('aria-label')).toBe('SSH Terminal');
 expect(dialog().getAttribute('aria-describedby')).toBe('terminal-keyboard-help');
 expect(dialog().querySelector('.modal-content')).toBeNull();
 expect(dialog().hasAttribute('inert')).toBe(false);
 expect(JSON.parse(snapshot()).inert.length).toBeGreaterThan(0);
 mgr(env).closeAll(); await stubs.settle();
 expect(dialog().open).toBe(false);
 expect(snapshot()).toBe(before);
});

t('Escape does not close the terminal and is left for the shell',async()=>{
 const env=await start(); const {w,d,dialog,stubs}=env;
 openIt(env);
 const event=new w.Event('cancel',{cancelable:true}); dialog().dispatchEvent(event);
 expect(event.defaultPrevented).toBe(true);
 const down=new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true});
 d.querySelector('.terminal-instance').dispatchEvent(down);
 expect(down.defaultPrevented).toBe(true);
 await stubs.settle();
 expect(dialog().open).toBe(true);
 mgr(env).closeAll(); await stubs.settle();
});

t('a click or touch on the backdrop leaves the terminal open',async()=>{
 const env=await start(); const {w,dialog,stubs,backdrop,touch}=env;
 openIt(env);
 backdrop(); touch(); await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 mgr(env).closeAll(); await stubs.settle();
});

t('a close that arrives while sessions remain reopens the terminal as a modal',async()=>{
 const env=await start(); const {w,dialog,stubs,snapshot}=env;
 openIt(env);
 const before=JSON.parse(snapshot()).inert.length;
 dialog().close(); await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(JSON.parse(snapshot()).inert.length).toBe(before);
 mgr(env).closeAll(); await stubs.settle();
 expect(dialog().open).toBe(false);
});

t('the close button closes every session and restores the page and focus',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
 const before=snapshot();
 openIt(env);
 dialog().querySelector('.modal-close').click();
 await stubs.settle(); await sleep(10);
 expect(mgr(env).sessions.size).toBe(0);
 expect(dialog().open).toBe(false);
 expect(snapshot()).toBe(before);
 expect(d.activeElement).toBe(env.opener);
});

t('closing the last tab closes the terminal',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
 const before=snapshot();
 openIt(env);
 d.querySelector('#terminal-tabs .tab-close').click();
 await stubs.settle();
 expect(dialog().open).toBe(false);
 expect(snapshot()).toBe(before);
});

t('minimize closes the dialog so the page is usable, focuses Restore, and Restore reopens it as a modal',async()=>{
 const env=await start(); const {w,d,dialog,stubs,snapshot}=env;
 const before=snapshot();
 openIt(env);
 d.getElementById('minimize-btn').focus();
 mgr(env).minimizeAll(); await stubs.settle();
 expect(dialog().open).toBe(false);
 expect(JSON.parse(snapshot()).inert).toEqual(JSON.parse(before).inert);
 expect(w.blacksmithSurfaces.size()).toBe(0);
 expect(d.getElementById('minimized-terminal-bar').classList.contains('active')).toBe(true);
 expect(d.activeElement.closest('#minimized-terminal-bar')).not.toBeNull();
 expect(d.activeElement.getAttribute('onclick')).toContain('restoreAll');
 mgr(env).restoreAll(); await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(stubs.isModal(dialog())).toBe(true);
 expect(d.getElementById('minimized-terminal-bar').classList.contains('active')).toBe(false);
 mgr(env).closeAll(); await stubs.settle();
 expect(JSON.parse(snapshot()).inert).toEqual(JSON.parse(before).inert);
 expect(w.blacksmithSurfaces.size()).toBe(0);
});

t('a second session reuses the open dialog',async()=>{
 const env=await start(); const {w,d,dialog,stubs}=env;
 openIt(env);
 mgr(env).openWithContext('blacksmith','broker','node/1','node','broker');
 expect(mgr(env).sessions.size).toBe(2);
 expect(w.document.querySelectorAll('dialog[open]').length).toBe(1);
 mgr(env).closeAll(); await stubs.settle();
});

t('toasts move into the terminal while it is open and back after',async()=>{
 const env=await start(); const {w,d,dialog,stubs}=env;
 openIt(env);
 w.showNotification('hello','info'); await sleep(10);
 const region=d.getElementById('blacksmith-live-region');
 expect(dialog().contains(region)).toBe(true);
 mgr(env).closeAll(); await stubs.settle();
 expect(region.parentNode).toBe(d.body);
});
