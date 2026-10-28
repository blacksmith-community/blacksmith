const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// Every modal is a native <dialog>. The overlay and container classes that the older modals used, and the
// ModalManager branch that animated them, are gone, so nothing in the page can reach them again.
const sources=()=>fs.readdirSync(UI).filter(f=>/\.(js|css|html)$/.test(f)).map(f=>[f,fs.readFileSync(path.join(UI,f),'utf8')]);

t('no page asset mentions the legacy modal overlay or container class',()=>{
 const found=[];
 sources().forEach(([f,text])=>{
  if(/modal-overlay/.test(text)) found.push(f+' modal-overlay');
  if(/\.modal\b(?![-_\w])/.test(text)&&/\.css$/.test(f)) found.push(f+' .modal');
 });
 expect(found).toEqual([]);
});

t('ModalManager refuses an element that is not a native dialog and changes nothing',async()=>{
 const env=boot({noDialogs:true}); envs.push(env);
 const {w,d,errors}=env;
 await new Promise(r=>setTimeout(r,50));
 errors.length=0;
 const div=d.createElement('div'); div.className='modal-overlay'; div.innerHTML='<button>Inside</button>';
 expect(w.ModalManager.openModal(div)).toBe(false);
 expect(errors).toEqual(['ModalManager.openModal: a native dialog element is required']);
 errors.length=0;
 expect(w.ModalManager.modalStack.length).toBe(0);
 expect(w.ModalManager.currentModal).toBeNull();
 expect(w.blacksmithSurfaces.size()).toBe(0);
 expect(div.isConnected).toBe(false);
 expect(d.body.style.overflow).toBe('');
});
