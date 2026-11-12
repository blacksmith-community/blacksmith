const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const OAT=fs.readFileSync(path.join(UI,'vendor','oat','oat.min.js'),'utf8');
const KEY='blacksmith.terminal.sessions';
const SCRIPT_URL='/vendor/wterm/0.5.10/wterm.min.js?v=0.5.10';
const STYLE_URL='/vendor/wterm/0.5.10/terminal.css?v=0.5.10';
const FAILED='The terminal could not start. Use the reconnect button to try again.';

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// The page no longer ships wterm, so nothing defines WTermLib until the test
// plays the browser's part and finishes loading the injected script.
const record=(ids,active)=>JSON.stringify({v:1,active,sessions:ids.map(i=>({deploymentType:'blacksmith',deploymentName:'broker',instanceId:i,instanceName:'node',boshDeploymentName:'broker'}))});
const start=async(opts={})=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w}=env;
 if(opts.preset!==undefined) w.sessionStorage.setItem(KEY,opts.preset);
 const terms=[];const sockets=[];
 const WTerm=class{constructor(el){this.cols=80;this.rows=24;el.classList.add('wterm');terms.push(this);}init(){return Promise.resolve(this);}focus(){}write(){}setRenderingPaused(){}destroy(){}};
 if(opts.preloaded) w.WTermLib={WTerm,version:'0.5.10'};
 w.WebSocket=class{static get OPEN(){return 1;}constructor(url){this.url=url;this.readyState=0;this.sent=[];sockets.push(this);}send(m){this.sent.push(m);}close(code){this.readyState=3;this.closedWith=code;}};
 w.eval(OAT);
 await sleep(80);
 const d=w.document;
 const scripts=()=>[...d.querySelectorAll('script[src*="/vendor/wterm/"]')];
 const styles=()=>[...d.querySelectorAll('link[href*="/vendor/wterm/"]')];
 const fire=(el,type)=>el.dispatchEvent(new w.Event(type));
 const loadScript=(define=true)=>{ if(define) w.WTermLib={WTerm,version:'0.5.10'}; fire(scripts()[0],'load'); };
 const loadStyle=()=>fire(styles()[0],'load');
 const open=(i=0)=>w.TerminalManager.openWithContext('blacksmith','broker',`node/${i}`,'node','broker');
 return {...env,d,terms,sockets,scripts,styles,fire,loadScript,loadStyle,open,mgr:w.TerminalManager};
};

t('the page itself references no terminal library asset',()=>{
 const html=fs.readFileSync(path.join(UI,'index.html'),'utf8');
 expect(html).not.toMatch(/\/vendor\/wterm\//);
});

t('a console that opens no terminal never loads the library',async()=>{
 const env=await start();
 expect(env.scripts()).toEqual([]);
 expect(env.styles()).toEqual([]);
 expect(env.w.WTermLib).toBeUndefined();
});

t('the first terminal loads one keyed script and one keyed stylesheet, and a second open adds none',async()=>{
 const env=await start();
 // The harness strips the page's stylesheets, so the ones around the
 // terminal sheet's old place come back for this test.
 for(const href of ['/blacksmith.css?v=k','/console.css?v=k']){ const l=env.d.createElement('link'); l.rel='stylesheet'; l.href=href; env.d.head.appendChild(l); }
 env.open(0);
 expect(env.scripts().map(s=>s.getAttribute('src'))).toEqual([SCRIPT_URL]);
 expect(env.styles().map(l=>[l.getAttribute('rel'),l.getAttribute('href')])).toEqual([['stylesheet',STYLE_URL]]);
 // The stylesheet keeps its old place in the cascade, straight after
 // blacksmith.css and before the sheets that followed it.
 const sheet=env.styles()[0];
 expect(sheet.previousElementSibling.getAttribute('href')).toMatch(/^\/blacksmith\.css\?v=/);
 expect(sheet.compareDocumentPosition(env.d.querySelector('link[href^="/console.css"]'))&env.w.Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
 env.open(1);
 expect(env.scripts().length).toBe(1);
 expect(env.styles().length).toBe(1);
 // Each tab exists, but nothing is built and no SSH session starts until
 // both the script and the stylesheet have arrived.
 expect(env.d.querySelectorAll('#terminal-tabs [role=tab]').length).toBe(2);
 env.loadScript(); await sleep(0);
 expect(env.terms.length).toBe(0);
 expect(env.sockets.length).toBe(0);
 env.loadStyle(); await sleep(0);
 expect(env.terms.length).toBe(2);
 expect(env.sockets.length).toBe(2);
 // A later terminal is built at once from the loaded library.
 env.mgr.closeAll();
 env.open(2);
 expect(env.terms.length).toBe(3);
 expect(env.sockets.length).toBe(3);
 expect(env.scripts().length).toBe(1);
 env.mgr.closeAll();
});

t('the scheme frame is read again once the stylesheet has loaded',async()=>{
 const env=await start();
 env.open(0);
 const modal=env.d.getElementById('terminal-modal');
 expect(modal.dataset.terminalWired).toBe('true');
 const synced=[];
 const sync=env.mgr.syncFrame;
 env.mgr.syncFrame=function(m){ synced.push(m); return sync.call(this,m); };
 env.loadScript(); env.loadStyle(); await sleep(0);
 expect(synced).toEqual([modal]);
 env.mgr.closeAll();
});

t('a script that fails to load shows the fixed failure, opens no socket, and Reconnect loads it again',async()=>{
 const env=await start();
 const raised=[]; env.w.Feedback.error=(message,options)=>raised.push({message,options});
 env.open(0);
 const session=env.mgr.sessions.get('blacksmith-broker-node/0');
 expect(session.status).not.toBe('failed');
 env.fire(env.scripts()[0],'error'); await sleep(0);
 expect(session.status).toBe('failed');
 expect(raised).toEqual([{message:FAILED,options:{toast:true}}]);
 expect(session.element.querySelector('[role=alert]').textContent).toBe(FAILED);
 expect(env.sockets.length).toBe(0);
 // The failed script is gone, and the stylesheet that did load stays.
 expect(env.scripts()).toEqual([]);
 expect(env.styles().length).toBe(1);
 env.loadStyle(); await sleep(0);
 expect(env.terms.length).toBe(0);
 env.d.querySelector('#terminal-tabs button.terminal-reconnect').click();
 expect(env.scripts().length).toBe(1);
 expect(env.styles().length).toBe(1);
 expect(env.sockets.length).toBe(0);
 env.loadScript(); await sleep(0);
 expect(env.terms.length).toBe(1);
 expect(session.failed).toBe(false);
 expect(env.sockets.length).toBe(1);
 env.mgr.closeAll();
});

t('a stylesheet that fails to load fails the terminal, and Reconnect loads only the stylesheet again',async()=>{
 const env=await start();
 env.w.Feedback.error=()=>{};
 env.open(0);
 env.loadScript();
 env.fire(env.styles()[0],'error'); await sleep(0);
 const session=env.mgr.sessions.get('blacksmith-broker-node/0');
 expect(session.status).toBe('failed');
 expect(env.terms.length).toBe(0);
 expect(env.sockets.length).toBe(0);
 expect(env.styles()).toEqual([]);
 env.d.querySelector('#terminal-tabs button.terminal-reconnect').click();
 expect(env.scripts().length).toBe(1);
 expect(env.styles().length).toBe(1);
 env.loadStyle(); await sleep(0);
 expect(env.terms.length).toBe(1);
 expect(env.sockets.length).toBe(1);
 env.mgr.closeAll();
});

t('a script that loads without defining the library fails the terminal and is fetched again on Reconnect',async()=>{
 const env=await start();
 env.w.Feedback.error=()=>{};
 env.open(0);
 env.loadStyle(); env.loadScript(false); await sleep(0);
 const session=env.mgr.sessions.get('blacksmith-broker-node/0');
 expect(session.status).toBe('failed');
 expect(env.sockets.length).toBe(0);
 expect(env.scripts()).toEqual([]);
 env.d.querySelector('#terminal-tabs button.terminal-reconnect').click();
 expect(env.scripts().length).toBe(1);
 env.loadScript(); await sleep(0);
 expect(env.terms.length).toBe(1);
 expect(env.sockets.length).toBe(1);
 env.mgr.closeAll();
});

t('a terminal closed while the library loads is never built and never connects',async()=>{
 const env=await start();
 env.open(0);
 env.mgr.closeAll();
 env.loadScript(); env.loadStyle(); await sleep(0);
 expect(env.terms.length).toBe(0);
 expect(env.sockets.length).toBe(0);
});

t('a library already on the page is used at once and nothing is injected',async()=>{
 const env=await start({preloaded:true});
 env.open(0);
 expect(env.terms.length).toBe(1);
 expect(env.sockets.length).toBe(1);
 expect(env.scripts()).toEqual([]);
 expect(env.styles()).toEqual([]);
 env.mgr.closeAll();
});

t('restored tabs show in the dock before the library has loaded',async()=>{
 const env=await start({preset:record(['node/0','node/1','node/2'],'blacksmith-broker-node/1')});
 const {d}=env;
 expect(d.getElementById('minimized-terminal-bar').classList.contains('active')).toBe(true);
 expect(d.querySelectorAll('#minimized-terminal-tabs .minimized-session-open').length).toBe(3);
 expect(env.terms.length).toBe(0);
 expect(env.sockets.length).toBe(0);
 expect(env.scripts().length).toBe(1);
 env.loadScript(); env.loadStyle(); await sleep(0);
 expect(env.terms.length).toBe(3);
 // A restored tab still waits for the operator before it connects.
 expect(env.sockets.length).toBe(0);
 expect(d.querySelectorAll('#minimized-terminal-tabs .minimized-session-open').length).toBe(3);
 env.mgr.closeAll();
});
