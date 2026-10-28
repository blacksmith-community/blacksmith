const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {install}=require('./dom-stubs.js');
const t=JSDOM?test:test.skip;

const OAT=fs.readFileSync(path.resolve(__dirname,'..','..','ui','vendor','oat','oat.min.js'),'utf8');

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// Oat closes a dialog when a touch lands on the dialog element itself, which is
// its padding or backdrop, and leaves a touch on its content alone. The dialog
// moves in this step lean on that, so the real Oat script runs here.
const start=()=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 const stubs=install(w,{dismiss:false});
 w.eval(OAT);
 const dialog=d.createElement('dialog');
 dialog.innerHTML='<p id="inside">Body</p><button id="ok">OK</button>';
 d.body.appendChild(dialog);
 const touch=(target)=>{ const e=new w.Event('touchstart',{bubbles:true,cancelable:true}); target.dispatchEvent(e); return e; };
 return {...env,stubs,dialog,touch};
};

t('a touch on the dialog padding closes it, cancels the touch, and fires close',async()=>{
 const {dialog,touch,stubs,w}=start();
 let closed=0;dialog.addEventListener('close',()=>closed++);
 dialog.showModal();
 expect(stubs.isModal(dialog)).toBe(true);
 const e=touch(dialog);
 expect(e.defaultPrevented).toBe(true);
 expect(dialog.hasAttribute('open')).toBe(false);
 expect(stubs.isModal(dialog)).toBe(false);
 await stubs.settle();
 expect(closed).toBe(1);
});

t('a touch on content inside the dialog leaves it open',async()=>{
 const {d,dialog,touch,stubs}=start();
 dialog.showModal();
 const e1=touch(d.getElementById('inside'));
 const e2=touch(d.getElementById('ok'));
 expect(e1.defaultPrevented).toBe(false);
 expect(e2.defaultPrevented).toBe(false);
 expect(dialog.hasAttribute('open')).toBe(true);
 expect(stubs.isModal(dialog)).toBe(true);
});
