const {test,expect}=require('bun:test');
const fs=require('fs');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

async function setup(){
 const env=boot({oat:true}),{w,d}=env;
 w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[{operating_system:'ubuntu',version:'1'}])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})]);
 await sleep(30);w.serviceInstances={cache:{service_id:'valkey',plan_id:'small'}};
 await w.__T.initUpgradeTab();return env;
}
const press=(w,code,target)=>target.dispatchEvent(new w.KeyboardEvent('keydown',{code,key:code.replace('Key',''),altKey:true,shiftKey:true,bubbles:true,cancelable:true}));

t('Alt/Option+Shift+X opens the Run command options',async()=>{
 const {w,d}=await setup();try{
 const panel=d.getElementById('upgrade');panel.classList.add('active');
 expect(d.getElementById('rotate-op-command').getAttribute('aria-keyshortcuts')).toBe('Alt+Shift+X');
 press(w,'KeyX',panel);await sleep(0);
 expect(d.getElementById('rotate-op-command').checked).toBe(true);
 expect(w.__T.batchFlow.step).toBe(1);
 // The draft of the operation left behind comes back the same way as for U, R, and C.
 press(w,'KeyU',panel);await sleep(0);
 expect(d.getElementById('rotate-op-upgrade').checked).toBe(true);
 }finally{w.close();}
});
t('the shortcut pauses in text fields and does nothing when commands are off',async()=>{
 const {w,d}=await setup();try{
 const panel=d.getElementById('upgrade');panel.classList.add('active');
 press(w,'KeyX',d.getElementById('batch-search'));await sleep(0);
 expect(d.getElementById('rotate-op-command').checked).toBe(false);
 const radio=d.getElementById('rotate-op-command');radio.disabled=true;
 w.__T.commandState.unavailable='Commands are turned off.';
 press(w,'KeyX',panel);await sleep(0);
 expect(radio.checked).toBe(false);expect(w.__T.batchFlow.step).toBe(0);
 }finally{w.close();}
});
t('the Keyboard shortcuts dialog and the popover list X',async()=>{
 const {w,d}=await setup();try{
 const panel=d.getElementById('upgrade');
 w.Element.prototype.getClientRects=function(){return this.matches('dialog[open]')?[1]:[]};
 const dialog=d.getElementById('keyboard-help-dialog');
 dialog.showModal=()=>dialog.setAttribute('open','');
 w.eval(fs.readFileSync('ui/navigation.js','utf8'));
 if(d.readyState==='loading') d.dispatchEvent(new w.Event('DOMContentLoaded'));
 d.querySelectorAll('.tab-panel').forEach(e=>e.classList.remove('active'));panel.classList.add('active');
 d.getElementById('keyboard-help-trigger').click();
 expect(dialog.textContent).toContain('Alt/Option+Shift+X');
 expect(dialog.textContent).toContain('Run command options');
 }finally{w.close();}
});
