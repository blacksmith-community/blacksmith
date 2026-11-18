const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,idle,TEST_LIMIT}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?(n,f,l)=>test(n,f,l===undefined?TEST_LIMIT:l):test.skip;

const OAT=fs.readFileSync(path.resolve(__dirname,'..','..','ui','vendor','oat','oat.min.js'),'utf8');
const HELP=fs.readFileSync(path.resolve(__dirname,'..','..','ui','action-help.js'),'utf8');
const NAV=fs.readFileSync(path.resolve(__dirname,'..','..','ui','navigation.js'),'utf8');

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// A native modal makes everything outside it inert, so a toast or live
// announcement raised while one is open has to sit inside it. The quick
// navigation, keyboard help, and theme dialogs belong to other scripts and
// are watched through window.blacksmithAnnouncers.
const start=async()=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 w.eval(HELP);
 w.eval(NAV);
 if(d.readyState==='loading') d.dispatchEvent(new w.Event('DOMContentLoaded'));
 await idle(w);
 return env;
};
const toastBox=(d)=>d.querySelector('.toast-container');
const live=(d)=>d.getElementById('blacksmith-live-region');

const cases=[
 {name:'quick navigation',id:'navigation-dialog',open:({d})=>d.getElementById('navigation-trigger').click(),close:({d})=>d.getElementById('navigation-dialog').close()},
 {name:'keyboard help',id:'keyboard-help-dialog',open:({d})=>d.getElementById('keyboard-help-trigger').click(),close:({d})=>d.getElementById('keyboard-help-dialog').close()},
 {name:'the theme chooser',id:'theme-choice-dialog',open:({w})=>w.blacksmithActionHelp.openThemes(),close:({d})=>d.getElementById('theme-choice-dialog').close()}
];
cases.forEach(({name,id,open,close})=>{
 t(`toasts and the live region move into ${name} while it is open and back when it closes`,async()=>{
  const env=await start(); const {w,d,stubs}=env;
  w.showNotification('Before','info'); await idle(env);
  expect(toastBox(d).parentNode).toBe(d.body);
  open(env); await idle(env);
  const dialog=d.getElementById(id);
  expect(dialog.open).toBe(true);
  expect(toastBox(d).parentNode).toBe(dialog);
  expect(live(d).parentNode).toBe(dialog);
  w.showNotification('During','info'); await idle(env);
  expect(dialog.contains(toastBox(d))).toBe(true);
  close(env); await idle(env);
  expect(toastBox(d).parentNode).toBe(d.body);
  expect(live(d).parentNode).toBe(d.body);
  open(env); await idle(env);
  expect(toastBox(d).parentNode).toBe(dialog);
  close(env); await idle(env);
  expect(toastBox(d).parentNode).toBe(d.body);
 });
});

t('a live region created while a modal is open is moved inside it without any toast',async()=>{
 const env=await start(); const {w,d,stubs,dialog}=env;
 live(d).remove();
 expect(toastBox(d)).toBeNull();
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await idle(env);
 const confirm=d.getElementById('dialog-modal');
 w.blacksmithNav.announce('Fresh region'); await idle(env);
 expect(toastBox(d)).toBeNull();
 expect(live(d).parentNode).toBe(confirm);
 d.querySelector('[data-dialog-role="cancel"]').click(); await done; await idle(env);
 expect(live(d).parentNode).toBe(d.body);
});
