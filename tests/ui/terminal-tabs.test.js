const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const OAT=fs.readFileSync(path.join(UI,'vendor','oat','oat.min.js'),'utf8');

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// The dialog's sessions are tabs of one bs-tabs element. Each session is a
// presentation wrapper holding the role=tab button and its close control,
// and its terminal host is the tab's panel.
const start=async()=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 const terms=[];
 w.WTermLib={WTerm:class{constructor(el){this.cols=80;this.rows=24;el.classList.add('wterm');terms.push(this);}init(){return Promise.resolve(this);}focus(){}write(){}setRenderingPaused(){}destroy(){}},version:'0.5.10'};
 const sockets=[];
 w.WebSocket=class{static get OPEN(){return 1;}constructor(){this.readyState=0;this.sent=[];sockets.push(this);}send(m){this.sent.push(m);}close(){}};
 await sleep(60);
 const set=()=>d.getElementById('terminal-tabset');
 const tabs=()=>[...d.querySelectorAll('#terminal-tabs [role=tab]')];
 const panels=()=>[...set().querySelectorAll(':scope > [role=tabpanel]')];
 const selected=()=>tabs().filter(x=>x.getAttribute('aria-selected')==='true').map(x=>x.textContent);
 const shown=()=>panels().filter(x=>!x.hidden).map(x=>x.id);
 return {...env,sockets,terms,set,tabs,panels,selected,shown};
};
const mgr=(env)=>env.w.TerminalManager;
const open=(env,i=0)=>mgr(env).openWithContext('blacksmith','broker',`node/${i}`,'node','broker');

t('the dialog is one bs-tabs element with a tablist, the controls, and the panels',async()=>{
 const env=await start(); const {w,d,set,stubs}=env;
 expect(set().tagName.toLowerCase()).toBe('bs-tabs');
 expect(set() instanceof w.customElements.get('ot-tabs')).toBe(true);
 const list=set().querySelector(':scope > [role=tablist]');
 expect(list.id).toBe('terminal-tabs');
 expect(list.getAttribute('aria-label')).toBe('Terminal sessions');
 expect(set().querySelector(':scope > .terminal-header-controls')).not.toBeNull();
 expect(d.getElementById('terminal-container')).toBeNull();
 expect(d.querySelectorAll('#terminal-modal [role=tab]').length).toBe(0);
});

t('each session adds a presentation wrapper, a role=tab button, and a tabpanel host',async()=>{
 const env=await start(); const {d,set,tabs,panels,selected,shown,stubs}=env;
 open(env); open(env,1);
 expect(tabs().length).toBe(2); expect(panels().length).toBe(2);
 tabs().forEach((tab,i)=>{
  expect(tab.parentElement.getAttribute('role')).toBe('presentation');
  expect(tab.parentElement.classList.contains('terminal-tab')).toBe(true);
  expect(tab.parentElement.querySelector('.tab-close')).not.toBeNull();
  expect(tab.contains(tab.parentElement.querySelector('.tab-close'))).toBe(false);
  expect(tab.getAttribute('aria-controls')).toBe(panels()[i].id);
  expect(panels()[i].getAttribute('aria-labelledby')).toBe(tab.id);
  expect(panels()[i].classList.contains('terminal-instance')).toBe(true);
 });
 expect(selected()).toEqual([tabs()[1].textContent]);
 expect(shown()).toEqual([panels()[1].id]);
 expect(tabs()[1].parentElement.classList.contains('active')).toBe(true);
 expect(tabs()[0].parentElement.classList.contains('active')).toBe(false);
 mgr(env).closeAll(); await stubs.settle();
});

t('selecting a tab shows only its panel and never resizes or reconnects a session',async()=>{
 const env=await start(); const {tabs,panels,selected,shown,sockets,terms,stubs}=env;
 open(env); open(env,1);
 const before=sockets.map(s=>s.sent.length); const count=sockets.length; const termCount=terms.length;
 tabs()[0].click();
 expect(selected()).toEqual([tabs()[0].textContent]);
 expect(shown()).toEqual([panels()[0].id]);
 expect(mgr(env).activeSession).toBe('blacksmith-broker-node/0');
 tabs()[1].click();
 expect(shown()).toEqual([panels()[1].id]);
 expect(sockets.length).toBe(count); expect(terms.length).toBe(termCount);
 expect(sockets.map(s=>s.sent.length)).toEqual(before);
 mgr(env).closeAll(); await stubs.settle();
});

t('the arrow keys move between session tabs',async()=>{
 const env=await start(); const {w,d,tabs,selected,stubs}=env;
 open(env); open(env,1);
 tabs()[1].focus();
 tabs()[1].dispatchEvent(new w.KeyboardEvent('keydown',{key:'Home',bubbles:true,cancelable:true}));
 expect(selected()).toEqual([tabs()[0].textContent]);
 expect(mgr(env).activeSession).toBe('blacksmith-broker-node/0');
 mgr(env).closeAll(); await stubs.settle();
});

t('closing the active tab selects the next session and keeps the others',async()=>{
 const env=await start(); const {tabs,panels,selected,shown,stubs}=env;
 open(env); open(env,1); open(env,2);
 const name0=tabs()[0].textContent;
 tabs()[1].click();
 tabs()[1].parentElement.querySelector('.tab-close').click();
 await stubs.settle();
 expect(tabs().length).toBe(2); expect(panels().length).toBe(2);
 expect(mgr(env).sessions.size).toBe(2);
 expect(selected().length).toBe(1);
 expect(shown().length).toBe(1);
 expect(tabs()[0].textContent).toBe(name0);
 mgr(env).closeAll(); await stubs.settle();
 expect(tabs().length).toBe(0);
});

t('the close control does not select its tab through Oat',async()=>{
 const env=await start(); const {tabs,selected,stubs}=env;
 open(env); open(env,1);
 tabs()[1].click();
 const other=tabs()[0].parentElement.querySelector('.tab-close');
 other.click(); await stubs.settle();
 expect(mgr(env).sessions.size).toBe(1);
 expect(selected()).toEqual([tabs()[0].textContent]);
 mgr(env).closeAll(); await stubs.settle();
});

t('reopening an open session switches to its tab without adding one',async()=>{
 const env=await start(); const {tabs,panels,selected,stubs}=env;
 open(env); open(env,1); open(env,0);
 expect(tabs().length).toBe(2); expect(panels().length).toBe(2);
 expect(selected()).toEqual([tabs()[0].textContent]);
 mgr(env).closeAll(); await stubs.settle();
});
