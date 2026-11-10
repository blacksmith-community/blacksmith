const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const OAT=fs.readFileSync(path.join(UI,'vendor','oat','oat.min.js'),'utf8');
// Synthetic keystrokes and output. Neither may ever reach the console.

// The page loads wterm as a classic script that sets window.WTermLib. This
// stand-in keeps init() pending until a test resolves it, so a test can deliver
// output before the core is ready. Like the real WTerm, it drops any write
// that arrives before init() resolves.
const fakeWTerm=(terms)=>class FakeWTerm{
 constructor(element,options={}){
  this.element=element;this.options=options;this.cols=80;this.rows=24;
  this.onData=options.onData||null;this.onResize=options.onResize||null;
  this.written=[];this.dropped=[];this.focused=0;this.destroyed=false;this.ready=false;
  this.paused=options.renderingPaused===true;
  this.initPromise=new Promise((resolve,reject)=>{this._resolve=resolve;this._reject=reject;});
  element.classList.add('wterm');
  terms.push(this);
 }
 resolveInit(cols=120,rows=40){ if(this.destroyed) return this._resolve(this); this.cols=cols;this.rows=rows;this.ready=true;this._resolve(this); }
 rejectInit(){ this.destroyed=true; this._reject(new Error('wterm: failed to initialize: synthetic')); }
 init(){ return this.initPromise; }
 write(data){ (this.ready&&!this.destroyed?this.written:this.dropped).push(data); }
 focus(){ this.focused++; }
 setRenderingPaused(paused){ this.paused=paused; }
 destroy(){ this.destroyed=true; }
};

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{
  expect(env.alerts).toEqual([]);
  expect(env.errors).toEqual([]);
 } finally { env.w.close(); } }
});

const start=async()=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w}=env;
 w.eval(OAT);
 const terms=[];const sockets=[];
 w.WTermLib={WTerm:fakeWTerm(terms),version:'0.5.10'};
 w.WebSocket=class{
  static get OPEN(){return 1;}
  constructor(url){this.url=url;this.readyState=0;this.sent=[];sockets.push(this);}
  send(message){this.sent.push(JSON.parse(message));}
  close(code){this.readyState=3;this.closedWith=code;}
 };
 await sleep(60);
 const mgr=w.TerminalManager;
 const open=(instance='node/0')=>mgr.openWithContext('blacksmith','broker',instance,'node','broker');
 const deliver=(socket,msg)=>socket.onmessage({data:JSON.stringify(msg)});
 const connect=async(socket,term)=>{
  socket.readyState=1; socket.onopen();
  deliver(socket,{type:'handshake'});
  if(term && !term.ready) term.resolveInit();
  await sleep(150);
  deliver(socket,{type:'status',meta:{status:'connected'}});
 };
 const sent=(socket,type)=>socket.sent.filter(m=>m.type===type);
 return {...env,terms,sockets,mgr,open,deliver,connect,sent};
};

// The palette button opens a menu of five color schemes. A choice recolors
// every session at once, is remembered per browser, and never reaches the
// remote terminal, because it changes colors and no sizes.
const KEY='blacksmith.terminal.scheme';
const THEMES=['theme-solarized-dark','theme-monokai','theme-light'];
const items=(d)=>[...d.querySelectorAll('#terminal-scheme-menu [role="menuitemradio"]')];
const item=(d,scheme)=>d.querySelector(`#terminal-scheme-menu [data-scheme="${scheme}"]`);
const themeOf=(el)=>THEMES.filter(c=>el.classList.contains(c));
const choose=async(d,scheme)=>{ item(d,scheme).click(); await sleep(40); };

t('the palette button sits between Show VMs and maximize',async()=>{
 const {w,open}=await start();
 open();
 const d=w.document;
 const ids=[...d.querySelectorAll('.terminal-header-controls button[id]')].map(b=>b.id);
 expect(ids.slice(0,4)).toEqual(['vms-btn','scheme-btn','maximize-btn','minimize-btn']);
 const b=d.getElementById('scheme-btn');
 expect(b.getAttribute('aria-label')).toBe('Terminal color scheme');
 expect(b.getAttribute('title')).toBe('Terminal color scheme');
 expect(b.getAttribute('aria-haspopup')).toBe('menu');
 expect(b.getAttribute('aria-expanded')).toBe('false');
 expect(b.querySelector('svg.lucide-palette')).not.toBeNull();
});

t('the menu offers five choices and exactly one is checked',async()=>{
 const {w,open}=await start();
 open();
 const d=w.document;
 expect(d.querySelector('#terminal-scheme-menu [role="menu"]')).not.toBeNull();
 expect(items(d).map(i=>i.dataset.scheme)).toEqual(['blacksmith','solarized-dark','monokai','light','console']);
 expect(items(d).map(i=>i.querySelector('.terminal-scheme-name').textContent)).toEqual(['Blacksmith','Solarized Dark','Monokai','Light','Match console']);
 expect(items(d).filter(i=>i.getAttribute('aria-checked')==='true').map(i=>i.dataset.scheme)).toEqual(['blacksmith']);
 items(d).forEach(i=>expect(i.querySelector('.terminal-scheme-swatch')).not.toBeNull());
});

t('a choice themes every open session and a session opened afterwards',async()=>{
 const {w,open}=await start();
 open('node/0'); open('node/1');
 const d=w.document;
 await choose(d,'monokai');
 const els=()=>[...d.querySelectorAll('#terminal-modal .terminal-instance')];
 expect(els().length).toBe(2);
 els().forEach(e=>expect(themeOf(e)).toEqual(['theme-monokai']));
 expect(d.getElementById('terminal-modal').dataset.terminalScheme).toBe('monokai');
 open('node/2');
 expect(els().length).toBe(3);
 els().forEach(e=>expect(themeOf(e)).toEqual(['theme-monokai']));
 await choose(d,'light');
 els().forEach(e=>expect(themeOf(e)).toEqual(['theme-light']));
 await choose(d,'solarized-dark');
 els().forEach(e=>expect(themeOf(e)).toEqual(['theme-solarized-dark']));
 await choose(d,'blacksmith');
 els().forEach(e=>expect(themeOf(e)).toEqual([]));
 expect(items(d).filter(i=>i.getAttribute('aria-checked')==='true').map(i=>i.dataset.scheme)).toEqual(['blacksmith']);
});

t('the choice is stored and applied when the dialog opens',async()=>{
 const {w,open}=await start();
 open();
 const d=w.document;
 await choose(d,'light');
 expect(w.localStorage.getItem(KEY)).toBe('light');
 const again=await start();
 again.w.localStorage.setItem(KEY,'monokai');
 again.open();
 const el=again.w.document.querySelector('#terminal-modal .terminal-instance');
 expect(themeOf(el)).toEqual(['theme-monokai']);
 expect(items(again.w.document).filter(i=>i.getAttribute('aria-checked')==='true').map(i=>i.dataset.scheme)).toEqual(['monokai']);
});

t('an unknown stored value falls back to Blacksmith',async()=>{
 const {w,open}=await start();
 w.localStorage.setItem(KEY,'neon-pink');
 open();
 const d=w.document;
 expect(themeOf(d.querySelector('#terminal-modal .terminal-instance'))).toEqual([]);
 expect(items(d).filter(i=>i.getAttribute('aria-checked')==='true').map(i=>i.dataset.scheme)).toEqual(['blacksmith']);
});

t('blocked storage falls back to Blacksmith without an error',async()=>{
 const {w,open}=await start();
 const boom=()=>{throw new Error('blocked');};
 Object.defineProperty(w,'localStorage',{configurable:true,get:()=>({getItem:boom,setItem:boom,removeItem:boom})});
 open();
 const d=w.document;
 expect(themeOf(d.querySelector('#terminal-modal .terminal-instance'))).toEqual([]);
 await choose(d,'monokai');
 expect(themeOf(d.querySelector('#terminal-modal .terminal-instance'))).toEqual(['theme-monokai']);
});

t('Match console follows the console theme live',async()=>{
 const {w,open}=await start();
 open();
 const d=w.document;
 const el=()=>d.querySelector('#terminal-modal .terminal-instance');
 d.documentElement.removeAttribute('data-theme');
 await choose(d,'console');
 expect(w.localStorage.getItem(KEY)).toBe('console');
 expect(themeOf(el())).toEqual(['theme-light']);
 d.documentElement.setAttribute('data-theme','dark');
 await sleep(40);
 expect(themeOf(el())).toEqual([]);
 d.documentElement.removeAttribute('data-theme');
 await sleep(40);
 expect(themeOf(el())).toEqual(['theme-light']);
 await choose(d,'monokai');
 d.documentElement.setAttribute('data-theme','dark');
 await sleep(40);
 expect(themeOf(el())).toEqual(['theme-monokai']);
});

t('a scheme change sends no resize message and reconnects nothing',async()=>{
 const {w,open,sockets,terms,connect,sent}=await start();
 open();
 await connect(sockets[0],terms[0]);
 const d=w.document;
 const before=sent(sockets[0],'resize').length;
 const count=sockets.length;
 await choose(d,'monokai'); await choose(d,'light'); await choose(d,'blacksmith');
 expect(sent(sockets[0],'resize').length).toBe(before);
 expect(sockets.length).toBe(count);
 expect(sockets[0].closedWith).toBeUndefined();
});

t('Escape closes the menu and returns focus to the button',async()=>{
 const {w,open}=await start();
 open();
 const d=w.document;
 const b=d.getElementById('scheme-btn');
 b.click(); await sleep(60);
 expect(b.getAttribute('aria-expanded')).toBe('true');
 expect(d.activeElement.getAttribute('role')).toBe('menuitemradio');
 expect(d.activeElement.dataset.scheme).toBe('blacksmith');
 d.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
 await sleep(60);
 expect(b.getAttribute('aria-expanded')).toBe('false');
 expect(d.activeElement).toBe(b);
});

t('the arrow keys move between items and wrap',async()=>{
 const {w,open}=await start();
 open();
 const d=w.document;
 d.getElementById('scheme-btn').click(); await sleep(60);
 const key=(k)=>d.activeElement.dispatchEvent(new w.KeyboardEvent('keydown',{key:k,bubbles:true,cancelable:true}));
 key('ArrowDown'); expect(d.activeElement.dataset.scheme).toBe('solarized-dark');
 key('ArrowUp'); key('ArrowUp'); expect(d.activeElement.dataset.scheme).toBe('console');
 key('ArrowDown'); expect(d.activeElement.dataset.scheme).toBe('blacksmith');
 key('End'); expect(d.activeElement.dataset.scheme).toBe('console');
 key('Home'); expect(d.activeElement.dataset.scheme).toBe('blacksmith');
});

t('choosing a scheme returns focus to the active terminal',async()=>{
 const {w,open,sockets,terms,connect}=await start();
 open();
 await connect(sockets[0],terms[0]);
 const d=w.document;
 d.getElementById('scheme-btn').click(); await sleep(60);
 const before=terms[0].focused;
 item(d,'light').click(); await sleep(80);
 expect(terms[0].focused).toBeGreaterThan(before);
});

t('the swatches take their colors from the scheme custom properties',async()=>{
 const {w,open}=await start();
 open();
 const d=w.document;
 for(const i of items(d)){
  const sw=i.querySelector('.terminal-scheme-swatch');
  expect(sw.style.cssText).not.toMatch(/#[0-9a-f]{3,6}/i);
  expect(sw.querySelectorAll('.terminal-scheme-dot').length).toBe(4);
 }
 expect(item(d,'monokai').querySelector('.terminal-scheme-swatch').classList.contains('theme-monokai')).toBe(true);
 expect(item(d,'blacksmith').querySelector('.terminal-scheme-swatch').className).not.toMatch(/theme-/);
});
