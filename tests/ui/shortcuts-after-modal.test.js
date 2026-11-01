const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const OAT=fs.readFileSync(path.join(UI,'vendor','oat','oat.min.js'),'utf8');
const ROUTES=fs.readFileSync(path.join(UI,'routes.js'),'utf8');
const NAVIGATION=fs.readFileSync(path.join(UI,'navigation.js'),'utf8');

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// A dialog counts as showing when it is open. The harness has no layout, so
// this stands in for the rendered check the page makes.
const start=async()=>{
 const env=boot({noDialogs:true,routes:[['/b/tasks/42',(u,o,j)=>j(200,{task:{id:42,state:'done',description:'a task'}})],['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})]]});envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 w.eval(ROUTES);
 await sleep(100);
 w.Element.prototype.getClientRects=function(){return this.matches('dialog[open]')?[1]:[]};
 w.eval(NAVIGATION);
 if(d.readyState==='loading') d.dispatchEvent(new w.Event('DOMContentLoaded'));
 return env;
};

// The page opens the task details view through the router, so it closes
// through the router too, by going Back in history.
t('the keyboard shortcuts button opens its dialog after the task details view closes with Escape',async()=>{
 const env=await start(); const {w,d,stubs}=env;
 const help=d.getElementById('keyboard-help-dialog');
 expect(help).not.toBeNull();
 w.showTaskDetails(42); await sleep(60);
 const task=d.getElementById('task-details-modal');
 expect(task.open).toBe(true);
 d.activeElement.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
 await sleep(200); await stubs.settle();
 expect(task.open).toBe(false);
 expect(w.location.hash).not.toContain('inspect=');
 d.getElementById('keyboard-help-trigger').click();
 expect(help.open).toBe(true);
 expect(help.textContent).toContain('Quick Navigation');
});
