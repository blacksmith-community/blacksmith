
const {test,expect}=require('bun:test');
const fs=require('fs');
const {main:{boot,sleep}}=require('./certificates/harness.js');
test('keyboard help shows current scope and restores focus',async()=>{
 const {w,d}=boot();try{
 await sleep(50);
 w.Element.prototype.getClientRects=function(){return this.matches('dialog[open]')?[1]:[]};
 const dialog=d.getElementById('keyboard-help-dialog');
 dialog.showModal=()=>dialog.setAttribute('open','');
 dialog.close=()=>{dialog.removeAttribute('open');dialog.dispatchEvent(new w.Event('close'));};
 w.eval(fs.readFileSync('ui/navigation.js','utf8'));
 if(d.readyState==='loading') d.dispatchEvent(new w.Event('DOMContentLoaded'));
 const trigger=d.getElementById('keyboard-help-trigger');trigger.focus();trigger.click();
 expect(dialog.hasAttribute('open')).toBe(true);
 expect(dialog.textContent).toContain('Quick Navigation');
 expect(dialog.textContent).toContain('Global');
 dialog.querySelector('[data-close]').click();
 expect(d.activeElement.id).toBe('keyboard-help-trigger');
 d.querySelectorAll('.tab-panel').forEach(e=>e.classList.remove('active'));
 const panel=d.getElementById('upgrade');panel.classList.add('active');
 d.getElementById('batch-new').setAttribute('aria-keyshortcuts','Alt+Shift+N');
 trigger.click();expect(dialog.textContent).toContain('New Operation (keeps your draft)');
 expect(dialog.textContent).toContain('Alt/Option+Shift+N');
 }finally{w.close();}
});
