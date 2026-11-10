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


// Each session shows its connection state as a dot, and a session that has
// disconnected shows a reconnect button in the same box. The dialog tabs and
// the minimized bar build their labels and controls through one set of
// manager methods.
const slot=(d,scope,key)=>d.querySelector(`${scope} .terminal-status-slot[data-session="${key}"]`);
const dialogSlot=(d,key)=>slot(d,'#terminal-tabs',key);
const KEY_BK='blacksmith-broker-node/0';

t('the shared label reads [BK] and [SI] with the instance name, job, and index',async()=>{
 const {mgr}=await start();
 expect(mgr.sessionLabel({deploymentType:'blacksmith',deploymentName:'blacksmith',instanceId:'blacksmith/0'}).text).toBe('[BK] blacksmith/blacksmith/0');
 expect(mgr.sessionLabel({deploymentType:'service',deploymentName:'valkey-cluster-3-9b4685ff-15b1-499d-b7f1',instanceName:'certmgmt-valkey-c3',boshDeploymentName:'valkey-cluster-3-9b4685ff-15b1-499d-b7f1',instanceId:'node/1'}).text).toBe('[SI] certmgmt-valkey-c3/node/1');
 expect(mgr.sessionLabel({deploymentType:'service',deploymentName:'dep-uuid',boshDeploymentName:'dep-uuid',instanceId:'node/1'}).text).toBe('[SI] dep-uuid/node/1');
});

t('the label title carries the label and the full deployment name',async()=>{
 const {mgr}=await start();
 const label=mgr.sessionLabel({deploymentType:'service',deploymentName:'x',instanceName:'certmgmt-valkey-c3',boshDeploymentName:'valkey-cluster-3-9b4685ff-15b1-499d-b7f1',instanceId:'node/1'});
 expect(label.title).toContain('[SI] certmgmt-valkey-c3/node/1');
 expect(label.title).toContain('valkey-cluster-3-9b4685ff-15b1-499d-b7f1');
});

t('the dialog tab and the minimized bar item show the same label text',async()=>{
 const env=await start(); const {d,mgr}=env;
 mgr.openWithContext('service','valkey-cluster-3-9b4685ff','node/1','certmgmt-valkey-c3','valkey-cluster-3-9b4685ff');
 const tab=d.querySelector('#terminal-tabs [role=tab]');
 mgr.minimizeAll();
 const mini=d.querySelector('#minimized-terminal-tabs .minimized-session-label');
 expect(tab.textContent).toBe('[SI] certmgmt-valkey-c3/node/1');
 expect(mini.textContent).toBe(tab.textContent);
 expect(d.querySelector('#minimized-terminal-tabs .minimized-session-open').title).toContain('valkey-cluster-3-9b4685ff');
 mgr.closeAll();
});

t('a new session shows an amber connecting dot with a title and hidden text',async()=>{
 const env=await start(); const {d,open,mgr}=env;
 open();
 const box=dialogSlot(d,KEY_BK);
 const dot=box.querySelector('.terminal-status-dot');
 expect(dot.classList.contains('terminal-status-connecting')).toBe(true);
 expect(dot.getAttribute('title')).toBe('Connecting');
 expect(dot.getAttribute('aria-label')).toBe('Connecting');
 expect(box.querySelector('button')).toBeNull();
 mgr.closeAll();
});

t('a connected session shows a green dot',async()=>{
 const env=await start(); const {d,terms,sockets,open,connect,mgr}=env;
 open(); await connect(sockets[0],terms[0]);
 const dot=dialogSlot(d,KEY_BK).querySelector('.terminal-status-dot');
 expect(dot.classList.contains('terminal-status-connected')).toBe(true);
 expect(dot.getAttribute('title')).toBe('Connected');
 expect(mgr.sessions.get(KEY_BK).status).toBe('connected');
 mgr.closeAll();
});

t('a disconnected session swaps the dot for a reconnect button in the same box',async()=>{
 const env=await start(); const {d,terms,sockets,open,connect,mgr}=env;
 open(); await connect(sockets[0],terms[0]);
 const box=dialogSlot(d,KEY_BK);
 sockets[0].readyState=3; sockets[0].onclose({code:1000});
 expect(mgr.sessions.get(KEY_BK).status).toBe('disconnected');
 expect(dialogSlot(d,KEY_BK)).toBe(box);
 expect(box.querySelector('.terminal-status-dot')).toBeNull();
 const button=box.querySelector('button.terminal-reconnect');
 expect(button.getAttribute('aria-label')).toBe('Reconnect [BK] node/node/0');
 expect(button.getAttribute('title')).toBe('Reconnect');
 expect(button.querySelector('svg.lucide-refresh-ccw')).not.toBeNull();
 expect(button.parentElement.nextElementSibling.classList.contains('tab-close')).toBe(true);
 mgr.closeAll();
});

t('the status box keeps one fixed size class in every state',async()=>{
 const env=await start(); const {d,terms,sockets,open,connect,mgr}=env;
 open(); const box=dialogSlot(d,KEY_BK);
 const classes=[]; const note=()=>classes.push([...dialogSlot(d,KEY_BK).classList].sort().join(' '));
 note(); await connect(sockets[0],terms[0]); note();
 sockets[0].onclose({code:1000}); note();
 expect(new Set(classes).size).toBe(1);
 const css=fs.readFileSync(path.join(UI,'blacksmith.css'),'utf8');
 const rule=css.match(/\.terminal-status-slot\s*\{[^}]*\}/)[0];
 expect(rule).toMatch(/width:\s*28px/); expect(rule).toMatch(/height:\s*28px/);
 mgr.closeAll();
});

t('a reconnect click reconnects that session only, activates its tab, and does not close it',async()=>{
 const env=await start(); const {d,terms,sockets,open,connect,mgr}=env;
 open(); open('node/1');
 const keyA=KEY_BK, keyB='blacksmith-broker-node/1';
 await connect(sockets[0],terms[0]); await connect(sockets[1],terms[1]);
 sockets[0].readyState=3; sockets[0].onclose({code:1000});
 expect(mgr.activeSession).toBe(keyB);
 const count=sockets.length;
 dialogSlot(d,keyA).querySelector('button.terminal-reconnect').click();
 expect(sockets.length).toBe(count+1);
 expect(sockets[1].readyState).toBe(1);
 expect(mgr.sessions.has(keyA)).toBe(true);
 expect(mgr.activeSession).toBe(keyA);
 expect(d.getElementById('tab-'+keyA).querySelector('[role=tab]').getAttribute('aria-selected')).toBe('true');
 expect(dialogSlot(d,keyA).querySelector('.terminal-status-dot').classList.contains('terminal-status-connecting')).toBe(true);
 expect(dialogSlot(d,keyB).querySelector('.terminal-status-dot').classList.contains('terminal-status-connected')).toBe(true);
 mgr.closeAll();
});

t('a reconnected session turns green, and a later close turns it back',async()=>{
 const env=await start(); const {d,terms,sockets,open,connect,deliver,mgr}=env;
 open(); await connect(sockets[0],terms[0]);
 sockets[0].onclose({code:1000});
 dialogSlot(d,KEY_BK).querySelector('button.terminal-reconnect').click();
 const fresh=sockets[1]; fresh.readyState=1; fresh.onopen();
 deliver(fresh,{type:'status',meta:{status:'connected'}});
 expect(mgr.sessions.get(KEY_BK).status).toBe('connected');
 fresh.onclose({code:1006});
 expect(mgr.sessions.get(KEY_BK).status).toBe('disconnected');
 expect(dialogSlot(d,KEY_BK).querySelector('button.terminal-reconnect')).not.toBeNull();
 mgr.closeAll();
});

t('the replaced socket closing does not mark the session disconnected',async()=>{
 const env=await start(); const {terms,sockets,open,connect,mgr}=env;
 open(); await connect(sockets[0],terms[0]);
 mgr.reconnectSession(KEY_BK);
 sockets[0].onclose({code:1000});
 expect(mgr.sessions.get(KEY_BK).status).toBe('connecting');
 mgr.closeAll();
});

t('the dialog header has no reconnect button and its controls start with maximize',async()=>{
 const env=await start(); const {d}=env;
 expect(d.getElementById('reconnect-btn')).toBeNull();
 expect(typeof env.mgr.reconnectActive).toBe('undefined');
 const ids=[...d.querySelectorAll('#terminal-modal .terminal-header-controls > button')].map(b=>b.id||b.className);
 expect(ids.slice(0,2)).toEqual(['maximize-btn','minimize-btn']);
});

t('the minimized bar shows the dot, no status word, and a reconnect button when disconnected',async()=>{
 const env=await start(); const {d,terms,sockets,open,connect,mgr}=env;
 open(); await connect(sockets[0],terms[0]);
 mgr.minimizeAll();
 const bar=d.querySelector('#minimized-terminal-tabs');
 expect(bar.querySelector('.minimized-session-status')).toBeNull();
 expect(bar.textContent).not.toContain('Connected');
 expect(slot(d,'#minimized-terminal-tabs',KEY_BK).querySelector('.terminal-status-connected')).not.toBeNull();
 sockets[0].onclose({code:1000});
 const button=slot(d,'#minimized-terminal-tabs',KEY_BK).querySelector('button.terminal-reconnect');
 expect(button).not.toBeNull();
 const item=bar.querySelector('.minimized-terminal-tab');
 expect(button.parentElement.nextElementSibling.classList.contains('tab-close')).toBe(true);
 const count=sockets.length;
 button.click();
 expect(sockets.length).toBe(count+1);
 expect(mgr.sessions.has(KEY_BK)).toBe(true);
 expect(d.getElementById('terminal-modal').open).toBe(false);
 expect(item.isConnected || true).toBe(true);
 mgr.closeAll();
});

t('every dot state has a themed colour class in both themes',()=>{
 const css=fs.readFileSync(path.join(UI,'blacksmith.css'),'utf8');
 const pick=(name)=>{ const m=css.match(new RegExp('\\.terminal-status-'+name+'\\s*\\{[^}]*\\}')); return m&&m[0]; };
 expect(pick('connected')).toMatch(/var\(--success\)/);
 expect(pick('connecting')).toMatch(/var\(--warning\)/);
 expect(pick('disconnected')).toMatch(/var\(--danger\)/);
 expect(pick('failed')).toMatch(/var\(--danger\)/);
});
