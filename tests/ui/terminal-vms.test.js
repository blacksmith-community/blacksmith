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

// The Show VMs button sits in the control group. It names the active
// session's instance, minimizes the dialog without touching any session,
// and opens that deployment's VMs view through the router.
const btn=(d)=>d.getElementById('vms-btn');
const routes=(w)=>{const calls=[];w.blacksmithRouter={navigate:async(r)=>{calls.push(r);}};return calls;};
const SI_ID='9b4685ff-15b1-499d-b7f1';
const openSI=(w,mgr)=>{w.serviceInstances={[SI_ID]:{instance_name:'certmgmt-valkey-c3',plan_id:'valkey-cluster-3'}};mgr.openWithContext('service-instance',SI_ID,'node/1','certmgmt-valkey-c3','valkey-cluster-3-'+SI_ID);};

t('the Show VMs button sits in front of maximize with the server icon',async()=>{
 const {w,mgr,open}=await start();
 open();
 const d=w.document;
 const controls=[...d.querySelectorAll('.terminal-header-controls > *')].map(x=>x.id||x.className);
 expect(controls.slice(0,2)).toEqual(['vms-btn','maximize-btn']);
 expect(btn(d).querySelector('svg.lucide-server')).not.toBeNull();
 expect(btn(d).querySelector('svg').getAttribute('aria-hidden')).toBe('true');
});

t('a broker session labels the button and opens the broker VMs view',async()=>{
 const {w,mgr,open}=await start();
 const calls=routes(w);
 open();
 const d=w.document;
 expect(btn(d).getAttribute('aria-label')).toBe('Show VMs for node');
 expect(btn(d).getAttribute('title')).toBe('Show VMs for node');
 expect(btn(d).disabled).toBe(false);
 btn(d).click();
 await sleep(20);
 expect(calls).toEqual([{screen:'blacksmith',section:'vms'}]);
 expect(d.getElementById('terminal-modal').open).toBe(false);
 expect(mgr.sessions.size).toBe(1);
 expect(d.getElementById('minimized-terminal-bar').classList.contains('active')).toBe(true);
});

t('a service instance session opens that instance VMs view',async()=>{
 const {w,mgr,sockets}=await start();
 const calls=routes(w);
 openSI(w,mgr);
 const d=w.document;
 expect(btn(d).getAttribute('aria-label')).toBe('Show VMs for certmgmt-valkey-c3');
 btn(d).click();
 await sleep(20);
 expect(calls).toEqual([{screen:'services',instance:SI_ID,section:'vms'}]);
 expect(mgr.sessions.size).toBe(1);
 expect(sockets.every(s=>s.closedWith===undefined)).toBe(true);
});

t('the label follows the active tab',async()=>{
 const {w,mgr,open}=await start();
 open('node/0');
 openSI(w,mgr);
 const d=w.document;
 expect(btn(d).getAttribute('aria-label')).toBe('Show VMs for certmgmt-valkey-c3');
 mgr.switchToSession('blacksmith-broker-node/0');
 expect(btn(d).getAttribute('aria-label')).toBe('Show VMs for node');
});

t('an instance that cannot be resolved disables the button and says why',async()=>{
 const {w,mgr}=await start();
 const calls=routes(w);
 w.serviceInstances={};
 mgr.openWithContext('service-instance',SI_ID,'node/1','gone-instance','valkey-cluster-3-'+SI_ID);
 const d=w.document;
 expect(btn(d).disabled).toBe(true);
 expect(btn(d).getAttribute('title')).toMatch(/no longer exists|cannot be found|not found/i);
 btn(d).click();
 await sleep(20);
 expect(calls).toEqual([]);
 expect(d.getElementById('terminal-modal').open).toBe(true);
});
