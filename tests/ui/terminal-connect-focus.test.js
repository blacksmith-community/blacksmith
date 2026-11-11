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
  this.written=[];this.dropped=[];this.focused=0;this.destroyed=false;this.destroyCalls=0;this.ready=false;
  this.paused=options.renderingPaused===true;
  this.initPromise=new Promise((resolve,reject)=>{this._resolve=resolve;this._reject=reject;});
  element.classList.add('wterm');
  // Like wterm, the terminal owns a real input that takes focus. A call to
  // focus() therefore moves document.activeElement, which is what the
  // operator would see in a browser.
  this.input=element.ownerDocument.createElement('input');
  element.appendChild(this.input);
  terms.push(this);
 }
 resolveInit(cols=120,rows=40){ if(this.destroyed) return this._resolve(this); this.cols=cols;this.rows=rows;this.ready=true;this._resolve(this); }
 rejectInit(){ this._reject(new Error('wterm: failed to initialize: synthetic')); }
 init(){ return this.initPromise; }
 write(data){ (this.ready&&!this.destroyed?this.written:this.dropped).push(data); }
 focus(){ this.focused++; this.input.focus(); }
 setRenderingPaused(paused){ this.paused=paused; }
 destroy(){ this.destroyCalls++; this.destroyed=true; }
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



// A terminal that connects in the background must not take keyboard focus.
// The operator may be typing into a page field while a session connects or
// reconnects, and a focus call into a hidden terminal swallows the keystrokes.
const KEY_A='blacksmith-broker-node/0';

t('a session that connects while the dialog is open and active takes focus once',async()=>{
 const env=await start();const {terms,sockets,open,connect,mgr}=env;
 open('node/0');await connect(sockets[0],terms[0]);await sleep(150);
 expect(terms[0].focused).toBeGreaterThan(0);
 expect(env.d.activeElement).toBe(terms[0].input);
 mgr.closeAll();
});

t('a session that connects while minimized does not take focus',async()=>{
 const env=await start();const {terms,sockets,open,connect,mgr,d}=env;
 open('node/0');
 mgr.minimizeAll();
 const field=d.createElement('input');d.body.appendChild(field);field.focus();
 await connect(sockets[0],terms[0]);await sleep(200);
 expect(terms[0].focused).toBe(0);
 expect(env.d.activeElement).toBe(field);
 mgr.closeAll();
});

t('a session that reconnects while minimized does not take focus',async()=>{
 const env=await start();const {terms,sockets,open,connect,mgr,d}=env;
 open('node/0');await connect(sockets[0],terms[0]);await sleep(150);
 mgr.minimizeAll();
 const field=d.createElement('input');d.body.appendChild(field);field.focus();
 const before=terms[0].focused;
 await connect(sockets[0],terms[0]);await sleep(200);
 expect(terms[0].focused).toBe(before);
 expect(d.activeElement).toBe(field);
 mgr.closeAll();
});

t('a background session in the open dialog does not take focus when it connects',async()=>{
 const env=await start();const {terms,sockets,open,connect,mgr}=env;
 open('node/0');open('node/1');
 await connect(sockets[0],terms[0]);await sleep(200);
 expect(terms[0].focused).toBe(0);
 mgr.closeAll();
});

// A focus requested while the core loads waits for init(), and it must meet
// the same conditions as every other focus once init() finishes.
t('a focus asked for while the core loads is dropped when the dialog is minimized first',async()=>{
 const env=await start();const {terms,sockets,open,mgr,d}=env;
 open('node/0');
 await sleep(200);
 mgr.minimizeAll();
 const field=d.createElement('input');d.body.appendChild(field);field.focus();
 terms[0].resolveInit();
 await sleep(50);
 expect(terms[0].focused).toBe(0);
 expect(d.activeElement).toBe(field);
 mgr.closeAll();
});

t('a focus asked for while the core loads is honored when the dialog stays open',async()=>{
 const env=await start();const {terms,open,mgr,d}=env;
 open('node/0');
 await sleep(200);
 terms[0].resolveInit();
 await sleep(50);
 expect(terms[0].focused).toBeGreaterThan(0);
 expect(d.activeElement).toBe(terms[0].input);
 mgr.closeAll();
});
