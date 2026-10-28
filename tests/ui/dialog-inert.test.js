const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const OAT=fs.readFileSync(path.resolve(__dirname,'..','..','ui','vendor','oat','oat.min.js'),'utf8');

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// A modal makes the other page children inert while it is open, and a dialog that opens over it clears its own inert and
// restores it on close. When the outer modal closes first, nothing is left to isolate, so no closed dialog may keep inert.
const start=async()=>{
 const env=boot({noDialogs:true,routes:[
  ['/b/configs/42',(u,o,j)=>j(200,{id:'42',name:'default',type:'cloud',team:'t',created_at:'2026-10-24T10:00:00Z',content:'azs: []',metadata:{}})],
  ['/b/configs/cloud/default/versions',(u,o,j)=>j(200,{configs:[{id:'42*',name:'default',type:'cloud'}],count:1})],
  ['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],
  ['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 w.Terminal=class{constructor(){this.cols=80;this.rows=24;}attachCustomKeyEventHandler(){}open(){}focus(){}resize(){}writeln(){}write(){}onData(){}onResize(){}dispose(){}};
 w.WebSocket=class{static get OPEN(){return 1;}constructor(){this.readyState=0;}send(){}close(){}};
 await sleep(60);
 const inertClosed=()=>[...d.querySelectorAll('dialog:not([open])')].filter(n=>n.hasAttribute('inert')).map(n=>n.id);
 return {...env,inertClosed};
};
// The confirmation dialog is created on first use, so a call that comes and goes first puts it in the page
// before the outer dialog isolates the background, as it is in a session that has already confirmed something.
const warm=async(env)=>{
 const done=env.w.showConfirmDialog({title:'First',message:'Warm up'});
 await sleep(10);
 env.d.querySelector('[data-dialog-role="cancel"]').click(); await done;
 await env.stubs.settle();
};
const openConfig=async(w)=>{ const p=w.showConfigVersions('cloud','default','42'); await sleep(60); return p; };

t('a confirmation that outlives the dialog under it leaves no closed dialog inert',async()=>{
 const env=await start(); const {w,d,stubs,inertClosed}=env;
 await warm(env); await openConfig(w);
 expect(d.getElementById('dialog-modal').hasAttribute('inert')).toBe(true);
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 expect(d.getElementById('dialog-modal').open).toBe(true);
 expect(d.getElementById('dialog-modal').hasAttribute('inert')).toBe(false);
 w.hideConfigVersionsModal(); await stubs.settle();
 expect(d.getElementById('config-details-modal').open).toBe(false);
 d.querySelector('[data-dialog-role="cancel"]').click(); expect(await done).toBe(false);
 await stubs.settle();
 expect(inertClosed()).toEqual([]);
 expect(d.querySelectorAll('[inert]').length).toBe(0);
});

t('a confirmation closed over a dialog that stays open keeps its inert, and clears it with the dialog',async()=>{
 const env=await start(); const {w,d,stubs,inertClosed}=env;
 await warm(env); await openConfig(w);
 const done=w.showConfirmDialog({title:'Sure',message:'Go on?'});
 await sleep(10);
 d.querySelector('[data-dialog-role="cancel"]').click(); expect(await done).toBe(false);
 await stubs.settle();
 expect(d.getElementById('dialog-modal').hasAttribute('inert')).toBe(true);
 w.hideConfigVersionsModal(); await stubs.settle();
 expect(inertClosed()).toEqual([]);
 expect(d.querySelectorAll('[inert]').length).toBe(0);
});

t('the terminal opened over a dialog is not inert, and no closed dialog is inert after both close',async()=>{
 const env=await start(); const {w,d,stubs,inertClosed}=env;
 await openConfig(w);
 const terminal=()=>d.getElementById('terminal-modal');
 expect(terminal().hasAttribute('inert')).toBe(true);
 w.TerminalManager.openWithContext('blacksmith','broker','node/0','node','broker');
 expect(terminal().open).toBe(true);
 expect(terminal().hasAttribute('inert')).toBe(false);
 w.hideConfigVersionsModal(); await stubs.settle();
 w.TerminalManager.closeAll(); await stubs.settle();
 expect(inertClosed()).toEqual([]);
 expect(d.querySelectorAll('[inert]').length).toBe(0);
});
