const {test,expect}=require('bun:test');
const {JSDOM}=require('./certificates/support.js');
const {setupRotate,cert}=require('./rotate-flow-support.js');
const t=JSDOM?test:test.skip;
const certs=[cert('/s/valkey_crt','valkey_crt',10,'inst-a','cache-a'),cert('/s/redis_crt','redis_crt',200,'inst-b','cache-b')];
const key=(w,el,k)=>el.dispatchEvent(new w.KeyboardEvent('keydown',{key:k,bubbles:true,cancelable:true}));
t('mode tabs follow the ARIA tabs pattern with arrow keys',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 const list=d.querySelector('#rotate-mode [role="tablist"]');
 const [redeploy,patch]=[...list.querySelectorAll('[role="tab"]')];
 expect(list.getAttribute('aria-label')).toBe('Mode');
 expect(redeploy.textContent).toBe('Redeploy');expect(patch.textContent).toBe('Patch in place');
 expect(redeploy.getAttribute('aria-selected')).toBe('true');expect(redeploy.tabIndex).toBe(0);expect(patch.tabIndex).toBe(-1);
 const panel=(tab)=>d.getElementById(tab.getAttribute('aria-controls'));
 expect(panel(redeploy).getAttribute('role')).toBe('tabpanel');
 expect(panel(redeploy).getAttribute('aria-labelledby')).toBe(redeploy.id);
 expect(panel(redeploy).hidden).toBe(false);expect(panel(patch).hidden).toBe(true);
 // the description is the first line of each panel
 expect(panel(redeploy).firstElementChild.className).toContain('rotate-mode-description');
 expect(panel(redeploy).firstElementChild.textContent).toContain('redeploys once for all of its selected certificates');
 expect(panel(patch).firstElementChild.className).toContain('rotate-mode-description');
 expect(panel(patch).firstElementChild.textContent).toContain('Temporary repair');
 expect(d.querySelector('.rotate-op-hint:not(.rotate-mode-reason)')).toBeNull();
 key(w,redeploy,'ArrowRight');
 expect(T.rotateState.mode).toBe('patch');expect(patch.getAttribute('aria-selected')).toBe('true');expect(d.activeElement).toBe(patch);
 expect(panel(patch).hidden).toBe(false);expect(panel(redeploy).hidden).toBe(true);
 expect(redeploy.tabIndex).toBe(-1);expect(patch.tabIndex).toBe(0);
 key(w,patch,'ArrowRight');expect(T.rotateState.mode).toBe('redeploy');expect(d.activeElement).toBe(redeploy);
 key(w,redeploy,'End');expect(T.rotateState.mode).toBe('patch');
 key(w,patch,'Home');expect(T.rotateState.mode).toBe('redeploy');
 key(w,redeploy,'ArrowLeft');expect(T.rotateState.mode).toBe('patch');
 expect(d.getElementById('batch-options-summary').textContent).toBe('Certificate options');
 }finally{w.close();}
});
t('an unavailable Patch tab stays visible, says why, and cannot be chosen',async()=>{
 const {w,d,T,sleep}=await setupRotate({certs,caps:{rotation:{enabled:true},patch_in_place:{enabled:false,reason:'the switch is off'}}});try{
 const patch=d.getElementById('rotate-mode-patch');
 expect(patch.hidden).toBe(false);expect(patch.getAttribute('aria-disabled')).toBe('true');
 const reason=d.getElementById('rotate-mode-patch-reason');
 expect(reason.hidden).toBe(false);expect(reason.textContent).toContain('the switch is off');
 expect(patch.getAttribute('aria-describedby')).toBe(reason.id);
 patch.click();expect(T.rotateState.mode).toBe('redeploy');
 key(w,d.getElementById('rotate-mode-redeploy'),'ArrowRight');expect(T.rotateState.mode).toBe('redeploy');
 // a Patch that closes while chosen falls back to Redeploy and says so
 T.rotateState.mode='patch';T.rotateState.converge='pending';T.updateOperationFooter();await sleep(80);
 expect(T.rotateState.mode).toBe('redeploy');
 expect(d.getElementById('rotate-mode-redeploy').getAttribute('aria-selected')).toBe('true');
 expect(d.getElementById('blacksmith-live-region').textContent).toContain('mode is back to Redeploy');
 }finally{w.close();}
});
t('After patching reads as one required choice of two cards',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 d.getElementById('rotate-mode-patch').click();
 const group=d.getElementById('rotate-mode-converge');
 expect(group.closest('[role="tabpanel"]').id).toBe('rotate-mode-patch-panel');
 expect(group.hidden).toBe(false);
 const options=[...group.querySelectorAll('.rotate-converge-option')];
 expect(options.length).toBe(2);
 options.forEach(o=>{expect(o.querySelector('.rotate-converge-title').textContent.length).toBeGreaterThan(0);expect(o.querySelector('.rotate-converge-hint').textContent.length).toBeGreaterThan(20);});
 expect(options.some(o=>o.querySelector('input').checked)).toBe(false);
 expect(group.getAttribute('aria-describedby')).toBe('rotate-mode-converge-lead');
 expect(d.getElementById('rotate-mode-converge-lead').textContent).toContain('Choose one');
 d.getElementById('rotate-mode-converge-pending').click();
 expect(T.rotateState.converge).toBe('pending');
 d.getElementById('rotate-mode-redeploy').click();
 expect(group.hidden).toBe(true);expect(T.rotateState.converge).toBe('');
 }finally{w.close();}
});
t('the Service line holds its label and select on one row',async()=>{
 const {w,d}=await setupRotate({certs});try{
 const row=d.getElementById('upgrade-service-filter').closest('.filter-row');
 expect(row.parentElement.id).toBe('batch-flow-options');
 expect([...row.children].map(n=>n.tagName)).toEqual(['LABEL','SELECT']);
 expect(d.querySelector('label[for="upgrade-service-filter"]').textContent).toBe('Service');
 }finally{w.close();}
});
