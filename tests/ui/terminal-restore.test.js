const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const OAT=fs.readFileSync(path.join(UI,'vendor','oat','oat.min.js'),'utf8');
const KEY='blacksmith.terminal.sessions';

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// Boots the console as a page load does. Storage given in preset is in place
// before Oat defines the tab set, which is when saved tabs come back.
const start=async(preset,opts={})=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w}=env;
 if(preset!==undefined) w.sessionStorage.setItem(KEY,preset);
 const terms=[];const sockets=[];
 w.WTermLib={WTerm:class{constructor(el,o){this.cols=80;this.rows=24;this.written=[];el.classList.add('wterm');terms.push(this);}init(){return opts.failInit?Promise.reject(new Error('wterm: failed to initialize: synthetic')):Promise.resolve(this);}focus(){}write(x){this.written.push(x);}setRenderingPaused(){}destroy(){}},version:'0.5.10'};
 w.WebSocket=class{static get OPEN(){return 1;}constructor(url){this.url=url;this.readyState=0;this.sent=[];sockets.push(this);}send(m){this.sent.push(m);}close(){this.readyState=3;}};
 w.eval(OAT);
 await sleep(80);
 const d=w.document;
 const tabs=()=>[...d.querySelectorAll('#terminal-tabs [role=tab]')].map(x=>x.textContent);
 const stored=()=>{ const raw=w.sessionStorage.getItem(KEY); return raw===null?null:JSON.parse(raw); };
 return {...env,terms,sockets,tabs,stored,mgr:w.TerminalManager};
};
const open=(env,i)=>env.mgr.openWithContext('blacksmith','broker',`node/${i}`,'node','broker');

t('opening and switching tabs stores both in order with the active one',async()=>{
 const env=await start();
 open(env,0); open(env,1);
 expect(env.stored().v).toBe(1);
 expect(env.stored().sessions.map(s=>s.instanceId)).toEqual(['node/0','node/1']);
 expect(env.stored().active).toBe('blacksmith-broker-node/1');
 env.d.querySelectorAll('#terminal-tabs [role=tab]')[0].click();
 expect(env.stored().active).toBe('blacksmith-broker-node/0');
});

t('closing a tab removes it and closing the last clears the key',async()=>{
 const env=await start();
 open(env,0); open(env,1);
 env.d.querySelectorAll('#terminal-tabs .tab-close')[0].click();
 expect(env.stored().sessions.map(s=>s.instanceId)).toEqual(['node/1']);
 env.d.querySelectorAll('#terminal-tabs .tab-close')[0].click();
 expect(env.w.sessionStorage.getItem(KEY)).toBeNull();
});

t('the stored record holds only the allowed fields and no output',async()=>{
 const env=await start();
 open(env,0);
 env.sockets[0].readyState=1; env.sockets[0].onopen();
 env.sockets[0].onmessage({data:JSON.stringify({type:'output',data:'SECRET-OUTPUT-MARKER'})});
 await sleep(30);
 const raw=env.w.sessionStorage.getItem(KEY);
 expect(raw).not.toContain('SECRET-OUTPUT-MARKER');
 const rec=JSON.parse(raw);
 expect(Object.keys(rec).sort()).toEqual(['active','sessions','v']);
 expect(Object.keys(rec.sessions[0]).sort()).toEqual(['boshDeploymentName','deploymentName','deploymentType','instanceId','instanceName']);
});

const record=(ids,active)=>JSON.stringify({v:1,active,sessions:ids.map(i=>({deploymentType:'blacksmith',deploymentName:'broker',instanceId:i,instanceName:'node',boshDeploymentName:'broker'}))});

t('a reload restores the tabs disconnected, in order, in the bar and the dialog, with no connection',async()=>{
 const env=await start(record(['node/0','node/1','node/2'],'blacksmith-broker-node/1'));
 const {d,w,mgr}=env;
 expect(env.tabs().length).toBe(3);
 expect(mgr.activeSession).toBe('blacksmith-broker-node/1');
 expect(d.getElementById('terminal-modal').open).toBe(false);
 expect(d.getElementById('minimized-terminal-bar').classList.contains('active')).toBe(true);
 expect(d.querySelectorAll('#minimized-terminal-tabs .minimized-terminal-tab').length).toBe(3);
 expect(d.querySelectorAll('#minimized-terminal-tabs button.terminal-reconnect').length).toBe(3);
 expect(d.querySelectorAll('#terminal-tabs button.terminal-reconnect').length).toBe(3);
 expect(d.querySelector('#minimized-terminal-tabs .minimized-terminal-tab.active .minimized-session-label').textContent).toContain('node/1');
 expect(env.sockets.length).toBe(0);
 expect(env.calls.filter(c=>c.includes('ssh')&&!c.includes('ui-terminal-status'))).toEqual([]);
 // The panel is a real, empty terminal with a short note.
 expect(env.terms.length).toBe(3);
 await sleep(30);
 expect(env.terms[0].written.join('')).toContain('Select reconnect to start a new session.');
 // Restoring does not rewrite a different record.
 expect(env.stored().sessions.length).toBe(3);
});

t('reconnect on one restored tab connects only that tab and makes it active',async()=>{
 const env=await start(record(['node/0','node/1'],'blacksmith-broker-node/0'));
 const {d,mgr}=env;
 d.querySelectorAll('#terminal-tabs button.terminal-reconnect')[1].click();
 expect(env.sockets.length).toBe(1);
 expect(env.sockets[0].url).toContain('index=1');
 expect(mgr.activeSession).toBe('blacksmith-broker-node/1');
 expect(mgr.sessions.get('blacksmith-broker-node/0').status).toBe('disconnected');
 expect(env.stored().active).toBe('blacksmith-broker-node/1');
});

t('a restored tab whose terminal cannot start is failed, offers reconnect, and connects nothing by itself',async()=>{
 const env=await start(record(['node/0'],'blacksmith-broker-node/0'),{failInit:true});
 await sleep(30);
 const {d,w,mgr,sockets}=env;
 const key='blacksmith-broker-node/0';
 expect(mgr.sessions.get(key).status).toBe('failed');
 expect(sockets.length).toBe(0);
 expect(d.querySelector('#terminal-tabs button.terminal-reconnect')).not.toBeNull();
 expect(d.querySelector('#minimized-terminal-tabs button.terminal-reconnect')).not.toBeNull();
 // A working library lets the next click connect that tab.
 w.WTermLib={WTerm:class{constructor(el){this.cols=80;this.rows=24;el.classList.add('wterm');}init(){return Promise.resolve(this);}focus(){}write(){}setRenderingPaused(){}destroy(){}},version:'0.5.10'};
 d.querySelector('#terminal-tabs button.terminal-reconnect').click();
 expect(sockets.length).toBe(1);
 expect(mgr.sessions.get(key).status).toBe('connecting');
 expect(mgr.sessions.get(key).failed).toBe(false);
 env.errors.length=0; env.alerts.length=0;
});

t('a reconnect to a target that is gone fails through the close path without throwing',async()=>{
 const env=await start(record(['node/0'],'blacksmith-broker-node/0'));
 env.d.querySelector('#terminal-tabs button.terminal-reconnect').click();
 env.sockets[0].onerror({message:'x'});
 env.sockets[0].onclose({code:1006});
 await sleep(30);
 expect(env.mgr.sessions.get('blacksmith-broker-node/0').status).toBe('disconnected');
});

for(const [name,raw] of [['malformed','{not json'],['an unknown version',JSON.stringify({v:2,active:'x',sessions:[{deploymentType:'blacksmith',deploymentName:'b',instanceId:'n/0'}]})],['a non-list',JSON.stringify({v:1,sessions:'x'})],['bad entries',JSON.stringify({v:1,sessions:[{deploymentType:'other',deploymentName:'b',instanceId:'n/0'},null,{}]})]]){
 t(`${name} storage restores no tabs and raises no error`,async()=>{
  const env=await start(raw);
  expect(env.tabs()).toEqual([]);
  expect(env.mgr.sessions.size).toBe(0);
  expect(env.d.getElementById('minimized-terminal-bar').classList.contains('active')).toBe(false);
 });
}

t('blocked storage restores no tabs and does not break opening a session',async()=>{
 const env=boot({noDialogs:true,routes:[['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w}=env;
 Object.defineProperty(w,'sessionStorage',{get(){throw new w.DOMException('blocked','SecurityError');},configurable:true});
 w.WTermLib={WTerm:class{constructor(el){this.cols=80;this.rows=24;el.classList.add('wterm');}init(){return Promise.resolve(this);}focus(){}write(){}setRenderingPaused(){}destroy(){}},version:'0.5.10'};
 w.WebSocket=class{static get OPEN(){return 1;}constructor(){this.readyState=0;}send(){}close(){}};
 w.eval(OAT);
 await sleep(80);
 expect(w.TerminalManager.sessions.size).toBe(0);
 w.TerminalManager.openWithContext('blacksmith','broker','node/0','node','broker');
 expect(w.TerminalManager.sessions.size).toBe(1);
});

const entry=(name,id)=>({deploymentType:'service-instance',deploymentName:name,instanceId:id,instanceName:'x',boshDeploymentName:'x'});
const rec=(...entries)=>JSON.stringify({v:1,active:'',sessions:entries});

for(const bad of ['../evil','a?b','a#b','a/b','..','.','a b','a%2fb']){
 t(`a stored deployment name ${JSON.stringify(bad)} is dropped`,async()=>{
  const env=await start(rec(entry(bad,'node/0')));
  expect(env.mgr.sessions.size).toBe(0);
 });
}

for(const bad of ['../0','node/../0','node/0/1','node?x/0','node/a','node/0#','/0']){
 t(`a stored instance id ${JSON.stringify(bad)} is dropped`,async()=>{
  const env=await start(rec(entry('abc-123',bad)));
  expect(env.mgr.sessions.size).toBe(0);
 });
}

t('a valid entry next to a bad one still restores',async()=>{
 const env=await start(rec(entry('../evil','node/0'),entry('0f3c9a-aa.bb_cc','node/2'),entry('x?y','node/1')));
 expect([...env.mgr.sessions.keys()]).toEqual(['service-instance-0f3c9a-aa.bb_cc-node/2']);
});

t('buildWebSocketUrl encodes the deployment segment and leaves real names alone',async()=>{
 const env=await start();
 const url=(name,id)=>env.mgr.buildWebSocketUrl({deploymentType:'service-instance',deploymentName:name,instanceId:id});
 expect(url('a/b?c#d','node/0')).toContain('/b/a%2Fb%3Fc%23d/ssh/stream?');
 expect(url('0f3c9a12-aa.bb_cc','node/2')).toContain('/b/0f3c9a12-aa.bb_cc/ssh/stream?instance=node&index=2');
});
