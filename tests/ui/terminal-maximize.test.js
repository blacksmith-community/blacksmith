const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const OAT=fs.readFileSync(path.join(UI,'vendor','oat','oat.min.js'),'utf8');
const KEY='blacksmith.terminal.maximized';

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// The dialog's maximize button adds a class and remembers the choice. It
// sends nothing to the host, because wterm's ResizeObserver reports the new
// size on its own once the layout changes.
const start=async(beforeOpen)=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 w.WTermLib={WTerm:class{constructor(el){this.cols=80;this.rows=24;el.classList.add('wterm');}init(){return Promise.resolve(this);}focus(){}write(){}setRenderingPaused(){}destroy(){}},version:'0.5.10'};
 const sockets=[];
 w.WebSocket=class{static get OPEN(){return 1;}constructor(){this.readyState=0;this.sent=[];sockets.push(this);}send(m){this.sent.push(m);}close(){}};
 await sleep(60);
 if(beforeOpen) beforeOpen(w);
 const dialog=()=>d.getElementById('terminal-modal');
 const btn=()=>d.getElementById('maximize-btn');
 return {...env,dialog,btn,sockets};
};
const mgr=(env)=>env.w.TerminalManager;
const openIt=(env)=>mgr(env).openWithContext('blacksmith','broker','node/0','node','broker');
const stored=(w)=>w.localStorage.getItem(KEY);

t('the header holds a maximize button in front of minimize',async()=>{
 const env=await start(); const {d,btn}=env;
 const ids=[...d.querySelectorAll('#terminal-modal .terminal-header-controls > button')].map(b=>b.id||b.className);
 expect(ids.slice(0,2)).toEqual(['maximize-btn','minimize-btn']);
 expect(btn().getAttribute('aria-label')).toBe('Maximize terminal');
 expect(btn().getAttribute('title')).toBe('Maximize terminal');
 expect(btn().getAttribute('aria-pressed')).toBe('false');
 expect(btn().querySelector('.icon-maximize')).not.toBeNull();
 expect(btn().querySelector('.icon-restore')).not.toBeNull();
 expect(btn().tagName).toBe('BUTTON');
});

t('the button toggles the maximized class, its labels, and the remembered choice',async()=>{
 const env=await start(); const {w,dialog,btn,stubs}=env;
 openIt(env);
 expect(dialog().classList.contains('terminal-maximized')).toBe(false);
 btn().click();
 expect(dialog().classList.contains('terminal-maximized')).toBe(true);
 expect(btn().getAttribute('aria-pressed')).toBe('true');
 expect(btn().getAttribute('aria-label')).toBe('Restore terminal size');
 expect(btn().getAttribute('title')).toBe('Restore terminal size');
 expect(stored(w)).toBe('true');
 btn().click();
 expect(dialog().classList.contains('terminal-maximized')).toBe(false);
 expect(btn().getAttribute('aria-pressed')).toBe('false');
 expect(btn().getAttribute('aria-label')).toBe('Maximize terminal');
 expect(stored(w)).toBe('false');
 mgr(env).closeAll(); await stubs.settle();
});

t('toggling sends nothing to the host, so wterm alone reports the new size',async()=>{
 const env=await start(); const {btn,sockets,stubs}=env;
 openIt(env);
 const before=sockets.map(s=>s.sent.length);
 btn().click(); btn().click();
 expect(sockets.map(s=>s.sent.length)).toEqual(before);
 mgr(env).closeAll(); await stubs.settle();
});

t('a remembered maximized state applies when the dialog opens',async()=>{
 const env=await start(w=>w.localStorage.setItem(KEY,'true')); const {dialog,btn,stubs}=env;
 openIt(env);
 expect(dialog().classList.contains('terminal-maximized')).toBe(true);
 expect(btn().getAttribute('aria-pressed')).toBe('true');
 expect(btn().getAttribute('aria-label')).toBe('Restore terminal size');
 mgr(env).closeAll(); await stubs.settle();
});

t('minimizing and restoring a maximized dialog keeps it maximized',async()=>{
 const env=await start(); const {dialog,btn,stubs}=env;
 openIt(env); btn().click();
 mgr(env).minimizeAll(); await stubs.settle();
 expect(dialog().open).toBe(false);
 mgr(env).restoreAll(); await stubs.settle();
 expect(dialog().open).toBe(true);
 expect(dialog().classList.contains('terminal-maximized')).toBe(true);
 mgr(env).closeAll(); await stubs.settle();
});

t('closing every session keeps the remembered state for the next open',async()=>{
 const env=await start(); const {w,dialog,btn,stubs}=env;
 openIt(env); btn().click();
 mgr(env).closeAll(); await stubs.settle();
 expect(stored(w)).toBe('true');
 openIt(env);
 expect(dialog().classList.contains('terminal-maximized')).toBe(true);
 mgr(env).closeAll(); await stubs.settle();
});

t('blocked storage falls back to normal size and the button still works',async()=>{
 const env=await start(w=>{
  Object.defineProperty(w,'localStorage',{configurable:true,get(){ throw new w.Error('blocked'); }});
 });
 const {dialog,btn,stubs}=env;
 openIt(env);
 expect(dialog().classList.contains('terminal-maximized')).toBe(false);
 btn().click();
 expect(dialog().classList.contains('terminal-maximized')).toBe(true);
 mgr(env).minimizeAll(); await stubs.settle();
 mgr(env).restoreAll(); await stubs.settle();
 expect(dialog().classList.contains('terminal-maximized')).toBe(true);
 mgr(env).closeAll(); await stubs.settle();
});

t('the button works from the keyboard and adds no global shortcut',async()=>{
 const env=await start(); const {w,d,dialog,btn,stubs}=env;
 openIt(env);
 expect(btn().hasAttribute('aria-keyshortcuts')).toBe(false);
 const down=new w.KeyboardEvent('keydown',{key:'m',ctrlKey:true,shiftKey:true,bubbles:true,cancelable:true});
 d.body.dispatchEvent(down);
 expect(dialog().classList.contains('terminal-maximized')).toBe(false);
 mgr(env).closeAll(); await stubs.settle();
});

const css=fs.readFileSync(path.join(UI,'blacksmith.css'),'utf8');
const rule=(sel)=>{ const i=css.indexOf(sel+' {'); return i<0?'':css.slice(i,css.indexOf('}',i)); };

test('a maximized dialog fills the viewport with no margin, radius, or border',()=>{
 const body=rule('#terminal-modal.terminal-maximized');
 expect(body).toMatch(/width:\s*100vw/);
 expect(body).toMatch(/max-width:\s*100vw/);
 expect(body).toMatch(/height:\s*100dvh/);
 expect(body).toMatch(/max-height:\s*100dvh/);
 expect(body).toMatch(/margin:\s*0/);
 expect(body).toMatch(/border-radius:\s*0/);
 expect(body).toMatch(/border:\s*0/);
});

test('the maximized height rounds to a whole pixel, with a fallback',()=>{
 const body=rule('#terminal-modal.terminal-maximized');
 expect(body).toMatch(/height:\s*100dvh;\s*height:\s*round\(down,\s*100dvh,\s*1px\)/);
});

test('the maximize button swaps its icon by state and shows a focus ring',()=>{
 expect(rule('#maximize-btn .icon-restore')).toMatch(/display:\s*none/);
 expect(rule('#maximize-btn[aria-pressed="true"] .icon-maximize')).toMatch(/display:\s*none/);
 expect(rule('#maximize-btn[aria-pressed="true"] .icon-restore')).toMatch(/display:\s*block/);
 expect(rule('.terminal-control-btn:focus-visible')).toMatch(/outline:/);
});
