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
 const observers=[];
 w.ResizeObserver=class{constructor(cb){this.cb=cb;this.targets=[];observers.push(this);}observe(el){this.targets.push(el);}unobserve(){}disconnect(){}};
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
 return {...env,terms,sockets,mgr,open,deliver,connect,sent,observers};
};



// A strip with more tabs than fit gets a chevron button at each edge that
// has hidden tabs. The dialog's tab list and the minimized bar share one
// set of controls.
const metrics=(el,{scrollWidth,clientWidth,scrollLeft=0})=>{
 Object.defineProperty(el,'scrollWidth',{value:scrollWidth,configurable:true});
 Object.defineProperty(el,'clientWidth',{value:clientWidth,configurable:true});
 el.scrollLeft=scrollLeft;
};
const scrolled=(w,el)=>el.dispatchEvent(new w.Event('scroll'));
const buttons=(host)=>({left:host.querySelector(':scope > .tab-scroll-btn[data-direction="left"]'),right:host.querySelector(':scope > .tab-scroll-btn[data-direction="right"]')});
const rect=(left,right)=>({left,right,top:0,bottom:40,width:right-left,height:40,x:left,y:0});

t('the dialog strip has a labelled chevron button on each side, hidden when nothing overflows',async()=>{
 const env=await start(); const {d,open,mgr}=env;
 open();
 const {left,right}=buttons(d.getElementById('terminal-tabset'));
 expect(left.getAttribute('aria-label')).toBe('Scroll tabs left');
 expect(right.getAttribute('aria-label')).toBe('Scroll tabs right');
 expect(left.type).toBe('button'); expect(right.type).toBe('button');
 expect(left.querySelector('svg.lucide-chevron-left')).not.toBeNull();
 expect(right.querySelector('svg.lucide-chevron-right')).not.toBeNull();
 expect(left.hidden).toBe(true); expect(right.hidden).toBe(true);
 mgr.closeAll();
});

t('each button shows only when tabs are hidden in its direction',async()=>{
 const env=await start(); const {w,d,open,mgr}=env;
 open(); const strip=d.getElementById('terminal-tabs'); const host=d.getElementById('terminal-tabset'); const {left,right}=buttons(host);
 metrics(strip,{scrollWidth:800,clientWidth:300,scrollLeft:0}); scrolled(w,strip);
 expect(left.hidden).toBe(true); expect(right.hidden).toBe(false);
 expect(host.classList.contains('tabs-fade-end')).toBe(true); expect(host.classList.contains('tabs-fade-start')).toBe(false);
 metrics(strip,{scrollWidth:800,clientWidth:300,scrollLeft:250}); scrolled(w,strip);
 expect(left.hidden).toBe(false); expect(right.hidden).toBe(false);
 metrics(strip,{scrollWidth:800,clientWidth:300,scrollLeft:500}); scrolled(w,strip);
 expect(left.hidden).toBe(false); expect(right.hidden).toBe(true);
 metrics(strip,{scrollWidth:300,clientWidth:300,scrollLeft:0}); scrolled(w,strip);
 expect(left.hidden).toBe(true); expect(right.hidden).toBe(true);
 expect(host.classList.contains('tabs-fade-start')||host.classList.contains('tabs-fade-end')).toBe(false);
 mgr.closeAll();
});

t('a resize of the strip refreshes the buttons',async()=>{
 const env=await start(); const {d,open,observers,mgr}=env;
 open(); const strip=d.getElementById('terminal-tabs'); const {right}=buttons(d.getElementById('terminal-tabset'));
 const watcher=observers.find(o=>o.targets.includes(strip));
 expect(watcher).toBeDefined();
 metrics(strip,{scrollWidth:900,clientWidth:300}); watcher.cb([]);
 expect(right.hidden).toBe(false);
 metrics(strip,{scrollWidth:200,clientWidth:300}); watcher.cb([]);
 expect(right.hidden).toBe(true);
 mgr.closeAll();
});

t('adding a tab refreshes the buttons',async()=>{
 const env=await start(); const {d,open,mgr}=env;
 open(); const strip=d.getElementById('terminal-tabs'); const {right}=buttons(d.getElementById('terminal-tabset'));
 metrics(strip,{scrollWidth:900,clientWidth:300});
 open('node/1');
 expect(right.hidden).toBe(false);
 mgr.closeAll();
});

t('a click scrolls about 80 percent of the visible width in that direction',async()=>{
 const env=await start(); const {w,d,open,mgr}=env;
 open(); const strip=d.getElementById('terminal-tabs'); const {left,right}=buttons(d.getElementById('terminal-tabset'));
 metrics(strip,{scrollWidth:2000,clientWidth:300,scrollLeft:100}); scrolled(w,strip);
 right.click();
 expect(strip.scrollLeft).toBe(340);
 left.click();
 expect(strip.scrollLeft).toBe(100);
 left.click();
 expect(strip.scrollLeft).toBe(0);
 mgr.closeAll();
});

t('a vertical wheel over an overflowing strip scrolls it sideways',async()=>{
 const env=await start(); const {w,d,open,mgr}=env;
 open(); const strip=d.getElementById('terminal-tabs');
 metrics(strip,{scrollWidth:800,clientWidth:300,scrollLeft:10}); scrolled(w,strip);
 const wheel=new w.WheelEvent('wheel',{deltaY:120,deltaX:0,bubbles:true,cancelable:true});
 strip.dispatchEvent(wheel);
 expect(strip.scrollLeft).toBe(130); expect(wheel.defaultPrevented).toBe(true);
 const sideways=new w.WheelEvent('wheel',{deltaY:3,deltaX:80,bubbles:true,cancelable:true});
 strip.dispatchEvent(sideways);
 expect(sideways.defaultPrevented).toBe(false); expect(strip.scrollLeft).toBe(130);
 metrics(strip,{scrollWidth:300,clientWidth:300,scrollLeft:0}); scrolled(w,strip);
 const idle=new w.WheelEvent('wheel',{deltaY:120,bubbles:true,cancelable:true});
 strip.dispatchEvent(idle);
 expect(idle.defaultPrevented).toBe(false); expect(strip.scrollLeft).toBe(0);
 mgr.closeAll();
});

t('switching to a tab that is out of view scrolls it fully into view',async()=>{
 const env=await start(); const {w,d,open,mgr}=env;
 open(); open('node/1'); open('node/2');
 const strip=d.getElementById('terminal-tabs');
 metrics(strip,{scrollWidth:900,clientWidth:300,scrollLeft:0}); scrolled(w,strip);
 strip.getBoundingClientRect=()=>rect(0,300);
 const first=d.getElementById('tab-blacksmith-broker-node/0'); const last=d.getElementById('tab-blacksmith-broker-node/2');
 last.getBoundingClientRect=()=>rect(600-strip.scrollLeft,760-strip.scrollLeft);
 first.getBoundingClientRect=()=>rect(0-strip.scrollLeft,160-strip.scrollLeft);
 mgr.switchToSession('blacksmith-broker-node/2');
 expect(strip.scrollLeft).toBeGreaterThanOrEqual(460);
 mgr.switchToSession('blacksmith-broker-node/0');
 expect(strip.scrollLeft).toBe(0);
 mgr.closeAll();
});

t('the minimized bar gets the same controls and reveals the active item',async()=>{
 const env=await start(); const {w,d,open,mgr}=env;
 open(); open('node/1'); open('node/2');
 mgr.minimizeAll();
 const host=d.getElementById('minimized-tabs-host'); const strip=d.getElementById('minimized-terminal-tabs');
 expect(strip.parentElement).toBe(host);
 const {left,right}=buttons(host);
 expect(left.getAttribute('aria-label')).toBe('Scroll tabs left'); expect(right.getAttribute('aria-label')).toBe('Scroll tabs right');
 metrics(strip,{scrollWidth:900,clientWidth:300,scrollLeft:0}); scrolled(w,strip);
 expect(left.hidden).toBe(true); expect(right.hidden).toBe(false);
 right.click(); expect(strip.scrollLeft).toBe(240);
 const wheel=new w.WheelEvent('wheel',{deltaY:60,bubbles:true,cancelable:true});
 strip.dispatchEvent(wheel);
 expect(wheel.defaultPrevented).toBe(true); expect(strip.scrollLeft).toBe(300);
 mgr.closeAll();
});

t('the minimized bar builds its buttons once however often it redraws',async()=>{
 const env=await start(); const {d,open,mgr}=env;
 open(); mgr.minimizeAll(); open('node/1'); mgr.minimizeAll();
 expect(d.getElementById('minimized-tabs-host').querySelectorAll(':scope > .tab-scroll-btn').length).toBe(2);
 expect(d.getElementById('terminal-tabset').querySelectorAll(':scope > .tab-scroll-btn').length).toBe(2);
 mgr.closeAll();
});

t('the chevrons overlay the strip edges and the controls keep their column',()=>{
 const css=fs.readFileSync(path.join(UI,'blacksmith.css'),'utf8');
 expect(css).toMatch(/#terminal-modal \.terminal-tabset > \.tab-scroll-btn\s*\{[^}]*grid-column:\s*1/);
 expect(css).toMatch(/\.tab-scroll-btn\[hidden\]\s*\{[^}]*display:\s*none/);
 expect(css).toMatch(/\.minimized-terminal-tabs\s*\{[^}]*overflow-x:\s*auto/);
});
