const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const OAT=fs.readFileSync(path.join(UI,'vendor','oat','oat.min.js'),'utf8');
const WASM_URL='/vendor/wterm/0.5.10/wterm.wasm';
// Synthetic keystrokes and output. Neither may ever reach the console.
const SECRET='SYNTHETIC-KEYS-7f3a';
const OUTPUT='SYNTHETIC-OUTPUT-91c2';

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
  expect(env.logs.filter(line=>line.includes(SECRET)||line.includes(OUTPUT))).toEqual([]);
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

t('the page loads the vendored wterm bundle and stylesheet and no xterm asset',()=>{
 const html=fs.readFileSync(path.join(UI,'index.html'),'utf8');
 expect(html).not.toMatch(/xterm/i);
 expect(html).toContain('<link rel="stylesheet" href="/vendor/wterm/0.5.10/terminal.css">');
 expect(html).toContain('<script src="/vendor/wterm/0.5.10/wterm.min.js"></script>');
 expect(html.indexOf('/vendor/wterm/0.5.10/wterm.min.js')).toBeLessThan(html.indexOf('/blacksmith.js'));
 expect(fs.existsSync(path.join(UI,'xterm.min.js'))).toBe(false);
 expect(fs.existsSync(path.join(UI,'xterm.css'))).toBe(false);
});

t('a session builds a wterm terminal on its own element with the versioned WASM URL',async()=>{
 const env=await start(); const {d,terms,open,mgr}=env;
 open();
 expect(terms.length).toBe(1);
 const term=terms[0];
 expect(term.options.wasmUrl).toBe(WASM_URL);
 expect(typeof term.options.onData).toBe('function');
 expect(typeof term.options.onResize).toBe('function');
 expect(term.options.autoResize).toBe(true);
 expect(term.options.renderer).toBe('dom');
 expect(term.options.webmcp).toBe(false);
 expect(term.options.debug).toBe(false);
 expect(term.options.cursorBlink).toBe(false);
 expect(term.options.announceOutput).toBe(true);
 expect(term.options.scrollbackLines).toBe(1000);
 // The size comes from measuring the container, and the remote host never
 // gets a title, bell, binary, clipboard, or alternate core hook.
 ['cols','rows','core','onBinary','onTitle','onBell','onClipboardWrite','onShellIntegration','onWorkingDirectory','maxImageWidth','maxImageHeight'].forEach(key=>expect(term.options[key]).toBeUndefined());
 expect(term.element.classList.contains('terminal-instance')).toBe(true);
 expect(term.element.parentElement.id).toBe('terminal-container');
 expect(term.element.getAttribute('aria-describedby')).toBe('terminal-keyboard-help');
 expect(term.element.getAttribute('aria-label')).toContain('node/0');
 expect(term.element.hasAttribute('tabindex')).toBe(false);
 term.resolveInit(); await sleep(0);
 mgr.closeAll();
});

t('output that arrives before init resolves is written in order once it does',async()=>{
 const env=await start(); const {terms,sockets,open,deliver,mgr}=env;
 open();
 const term=terms[0], socket=sockets[0];
 socket.readyState=1; socket.onopen();
 deliver(socket,{type:'output',data:OUTPUT+'-1 '});
 deliver(socket,{type:'output',data:OUTPUT+'-2 '});
 expect(term.written).toEqual([]);
 expect(term.dropped).toEqual([]);
 term.resolveInit(); await sleep(0);
 deliver(socket,{type:'output',data:OUTPUT+'-3'});
 expect(term.dropped).toEqual([]);
 const text=term.written.join('');
 expect(text.startsWith('Connecting to Blacksmith broker :: node/0\r\n')).toBe(true);
 expect(text.endsWith(OUTPUT+'-1 '+OUTPUT+'-2 '+OUTPUT+'-3')).toBe(true);
 mgr.closeAll();
});

t('the start message waits for init and carries the applied grid size',async()=>{
 const env=await start(); const {terms,sockets,open,deliver,sent,mgr}=env;
 open();
 const term=terms[0], socket=sockets[0];
 socket.readyState=1; socket.onopen();
 deliver(socket,{type:'handshake'});
 await sleep(150);
 expect(sent(socket,'control')).toEqual([]);
 term.resolveInit(132,43); await sleep(0);
 const control=sent(socket,'control');
 expect(control.length).toBe(1);
 expect(control[0].meta).toEqual({action:'start',width:132,height:43,term:'xterm-256color'});
 mgr.closeAll();
});

t('keystrokes reach the websocket only once the session is connected',async()=>{
 const env=await start(); const {terms,sockets,open,deliver,connect,sent,mgr}=env;
 open();
 const term=terms[0], socket=sockets[0];
 socket.readyState=1; socket.onopen(); term.resolveInit(); await sleep(0);
 term.onData(SECRET+'-early');
 expect(sent(socket,'input')).toEqual([]);
 await connect(socket,term);
 term.onData(SECRET+'\r');
 expect(sent(socket,'input').map(m=>m.data)).toEqual([SECRET+'\r']);
 mgr.closeAll();
});

t('a resize sends the applied columns and rows to the backend',async()=>{
 const env=await start(); const {terms,sockets,open,connect,sent,mgr}=env;
 open();
 const term=terms[0], socket=sockets[0];
 await connect(socket,term);
 term.onResize(100,30);
 const resize=sent(socket,'control').filter(m=>m.meta.action==='resize');
 expect(resize.map(m=>m.meta)).toEqual([{action:'resize',width:100,height:30}]);
 mgr.closeAll();
});

t('closing a session destroys its terminal and closes its socket',async()=>{
 const env=await start(); const {d,terms,sockets,open,connect,deliver,mgr}=env;
 open(); open('node/1');
 await connect(sockets[0],terms[0]);
 await connect(sockets[1],terms[1]);
 mgr.closeSession('blacksmith-broker-node/0');
 expect(terms[0].destroyed).toBe(true);
 expect(terms[1].destroyed).toBe(false);
 expect(sockets[0].readyState).toBe(3);
 expect(d.getElementById('terminal-blacksmith-broker-node/0')).toBeNull();
 const before=terms[0].written.length;
 deliver(sockets[0],{type:'output',data:OUTPUT});
 sockets[0].onclose({code:1006});
 expect(terms[0].written.length).toBe(before);
 expect(terms[0].dropped).toEqual([]);
 mgr.closeAll();
 expect(terms[1].destroyed).toBe(true);
 expect(sockets[1].readyState).toBe(3);
});

t('a session closed before init resolves never writes to its terminal',async()=>{
 const env=await start(); const {terms,sockets,open,deliver,mgr}=env;
 open();
 const term=terms[0], socket=sockets[0];
 socket.readyState=1; socket.onopen();
 deliver(socket,{type:'output',data:OUTPUT});
 mgr.closeAll();
 expect(term.destroyed).toBe(true);
 term.resolveInit(); await sleep(0);
 expect(term.written).toEqual([]);
 expect(term.dropped).toEqual([]);
});

t('a retried connection is the socket that closing the session closes',async()=>{
 const env=await start(); const {terms,sockets,open,mgr}=env;
 open();
 terms[0].resolveInit(); await sleep(0);
 sockets[0].onclose({code:1006});
 await sleep(1100);
 expect(sockets.length).toBe(2);
 mgr.closeAll();
 expect(sockets[1].readyState).toBe(3);
 await sleep(1100);
 expect(sockets.length).toBe(2);
});

t('switching sessions pauses rendering for the hidden terminal',async()=>{
 const env=await start(); const {terms,open,mgr}=env;
 open(); open('node/1');
 terms.forEach(term=>term.resolveInit()); await sleep(0);
 expect(terms[0].paused).toBe(true);
 expect(terms[1].paused).toBe(false);
 mgr.switchToSession('blacksmith-broker-node/0');
 expect(terms[0].paused).toBe(false);
 expect(terms[1].paused).toBe(true);
 await sleep(150);
 expect(terms[0].focused).toBeGreaterThan(0);
 mgr.closeAll();
});

t('Ctrl+Shift+F6 leaves terminal input without reaching the terminal',async()=>{
 const env=await start(); const {w,d,terms,open,mgr}=env;
 open();
 const term=terms[0]; term.resolveInit(); await sleep(0);
 const input=d.createElement('textarea'); term.element.appendChild(input);
 const reached=[]; input.addEventListener('keydown',e=>reached.push(e.key));
 input.focus();
 const chord=new w.KeyboardEvent('keydown',{key:'F6',ctrlKey:true,shiftKey:true,bubbles:true,cancelable:true});
 input.dispatchEvent(chord);
 expect(reached).toEqual([]);
 expect(chord.defaultPrevented).toBe(true);
 expect(d.activeElement.id).toBe('minimize-btn');
 input.dispatchEvent(new w.KeyboardEvent('keydown',{key:'a',bubbles:true,cancelable:true}));
 expect(reached).toEqual(['a']);
 mgr.closeAll();
});

t('reconnecting clears the screen and routes keystrokes and resizes to the new socket',async()=>{
 const env=await start(); const {terms,sockets,open,connect,sent,mgr}=env;
 open();
 const term=terms[0];
 await connect(sockets[0],term);
 mgr.reconnectActive();
 expect(sockets.length).toBe(2);
 expect(sockets[0].readyState).toBe(3);
 expect(term.written.join('')).toContain('\x1bc');
 const fresh=sockets[1]; fresh.readyState=1; fresh.onopen(); await sleep(0);
 expect(sent(fresh,'control')[0].meta).toEqual({action:'start',width:term.cols,height:term.rows,term:'xterm-256color'});
 term.onData(SECRET);
 term.onResize(90,20);
 expect(sent(fresh,'input').map(m=>m.data)).toEqual([SECRET]);
 expect(sent(fresh,'control').filter(m=>m.meta.action==='resize').map(m=>m.meta)).toEqual([{action:'resize',width:90,height:20}]);
 expect(sent(sockets[0],'input')).toEqual([]);
 mgr.closeAll();
});

t('a terminal that fails to start says so and closes its socket',async()=>{
 const env=await start(); const {w,d,terms,sockets,open,mgr}=env;
 const raised=[]; const error=w.Feedback.error;
 w.Feedback.error=(message,options)=>{ raised.push({message,options}); };
 open();
 sockets[0].readyState=1; sockets[0].onopen();
 terms[0].rejectInit(); await sleep(0);
 w.Feedback.error=error;
 expect(raised.length).toBe(1);
 expect(raised[0].message).toContain('terminal could not start');
 expect(raised[0].message).not.toContain('synthetic');
 expect(raised[0].options).toEqual({toast:true});
 const message=d.querySelector('.terminal-instance [role=alert]');
 expect(message).not.toBeNull();
 expect(message.textContent).toContain('terminal could not start');
 expect(sockets[0].readyState).toBe(3);
 mgr.closeAll();
});

const FAILED='The terminal could not start. Reload the page and try again.';
const raisedErrors=(w)=>{ const raised=[]; w.Feedback.error=(message,options)=>{ raised.push({message,options}); }; return raised; };

t('a missing terminal library shows the fixed failure and never opens a socket',async()=>{
 const env=await start(); const {w,d,sockets,open,mgr}=env;
 delete w.WTermLib;
 const raised=raisedErrors(w);
 open(); await sleep(0);
 expect(raised).toEqual([{message:FAILED,options:{toast:true}}]);
 expect(sockets.length).toBe(0);
 const host=d.getElementById('terminal-blacksmith-broker-node/0');
 expect(host.querySelector('[role=alert]').textContent).toBe(FAILED);
 expect(d.getElementById('tab-blacksmith-broker-node/0')).not.toBeNull();
 mgr.closeAll();
 expect(d.getElementById('terminal-blacksmith-broker-node/0')).toBeNull();
});

t('a terminal constructor that throws shows the fixed failure and never opens a socket',async()=>{
 const env=await start(); const {w,d,sockets,open,mgr}=env;
 w.WTermLib={WTerm:class{constructor(){throw new Error('synthetic constructor failure');}},version:'0.5.10'};
 const raised=raisedErrors(w);
 open(); await sleep(0);
 expect(raised).toEqual([{message:FAILED,options:{toast:true}}]);
 expect(sockets.length).toBe(0);
 expect(d.querySelector('#terminal-blacksmith-broker-node\\/0 [role=alert]').textContent).toBe(FAILED);
 mgr.closeAll();
});

t('a failed start raises no second disconnect notification when its socket closes',async()=>{
 const env=await start(); const {w,d,terms,sockets,open,mgr}=env;
 const raised=raisedErrors(w);
 open();
 sockets[0].readyState=1; sockets[0].onopen();
 terms[0].rejectInit(); await sleep(0);
 // A socket closed while connecting reports 1006 in a browser.
 sockets[0].onclose({code:1006});
 await sleep(1100);
 expect(raised.length).toBe(1);
 expect(d.body.textContent).not.toContain('Disconnected:');
 expect(sockets.length).toBe(1);
 mgr.closeAll();
});

t('a retry while the terminal starts sends exactly one start message, on the new socket',async()=>{
 const env=await start(); const {terms,sockets,open,deliver,sent,mgr}=env;
 open();
 const term=terms[0];
 sockets[0].readyState=1; sockets[0].onopen();
 deliver(sockets[0],{type:'handshake'});
 deliver(sockets[0],{type:'error',data:'Session not connected'});
 await sleep(1100);
 expect(sockets.length).toBe(2);
 sockets[1].readyState=1; sockets[1].onopen();
 deliver(sockets[1],{type:'handshake'});
 await sleep(150);
 term.resolveInit(); await sleep(0);
 const starts=socket=>sent(socket,'control').filter(m=>m.meta.action==='start');
 expect(starts(sockets[0])).toEqual([]);
 expect(starts(sockets[1]).length).toBe(1);
 mgr.closeAll();
});

t('a resize between the start message and connected reaches the backend once connected',async()=>{
 const env=await start(); const {terms,sockets,open,deliver,sent,mgr}=env;
 open();
 const term=terms[0], socket=sockets[0];
 socket.readyState=1; socket.onopen();
 deliver(socket,{type:'handshake'});
 term.resolveInit(120,40);
 await sleep(150);
 expect(sent(socket,'control').map(m=>m.meta)).toEqual([{action:'start',width:120,height:40,term:'xterm-256color'}]);
 term.cols=100; term.rows=30; term.onResize(100,30);
 expect(sent(socket,'control').length).toBe(1);
 deliver(socket,{type:'status',meta:{status:'connected'}});
 expect(sent(socket,'control').filter(m=>m.meta.action==='resize').map(m=>m.meta)).toEqual([{action:'resize',width:100,height:30}]);
 mgr.closeAll();
});

t('a focus request made while the terminal starts is honored for the active session once ready',async()=>{
 const env=await start(); const {terms,open,mgr}=env;
 open(); open('node/1');
 await sleep(150);
 expect(terms.map(term=>term.focused)).toEqual([0,0]);
 terms.forEach(term=>term.resolveInit()); await sleep(0);
 expect(terms[1].focused).toBe(1);
 expect(terms[0].focused).toBe(0);
 mgr.closeAll();
});

t('the keyup of Ctrl+Shift+F6 never reaches the terminal input',async()=>{
 const env=await start(); const {w,d,terms,open,mgr}=env;
 open();
 const term=terms[0]; term.resolveInit(); await sleep(0);
 const input=d.createElement('textarea'); term.element.appendChild(input);
 const reached=[]; input.addEventListener('keyup',e=>reached.push(e.key));
 const chord=new w.KeyboardEvent('keyup',{key:'F6',ctrlKey:true,shiftKey:true,bubbles:true,cancelable:true});
 input.dispatchEvent(chord);
 expect(reached).toEqual([]);
 expect(chord.defaultPrevented).toBe(true);
 input.dispatchEvent(new w.KeyboardEvent('keyup',{key:'a',bubbles:true,cancelable:true}));
 expect(reached).toEqual(['a']);
 mgr.closeAll();
});

t('a reconnect stops the replaced connection from retrying',async()=>{
 const env=await start(); const {terms,sockets,open,mgr}=env;
 open();
 terms[0].resolveInit(); await sleep(0);
 mgr.reconnectActive();
 expect(sockets.length).toBe(2);
 // The replaced socket closes before any handshake, which would retry.
 sockets[0].onclose({code:1006});
 await sleep(1100);
 expect(sockets.length).toBe(2);
 mgr.closeAll();
});
