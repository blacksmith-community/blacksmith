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
 expect(list.getAttribute('aria-label')).toBe('Certificate rotation method');
 expect(redeploy.textContent).toBe('Redeploy');expect(patch.textContent).toBe('Patch in place');
 expect(redeploy.getAttribute('aria-selected')).toBe('true');expect(redeploy.tabIndex).toBe(0);expect(patch.tabIndex).toBe(-1);
 // each method has its own panel, labelled by its tab, and only the selected one shows
 const panel=d.getElementById(redeploy.getAttribute('aria-controls'));
 const patchPanel=d.getElementById(patch.getAttribute('aria-controls'));
 expect(patchPanel.id).not.toBe(panel.id);
 expect(panel.getAttribute('role')).toBe('tabpanel');
 expect(panel.getAttribute('aria-labelledby')).toBe(redeploy.id);
 expect(patchPanel.getAttribute('aria-labelledby')).toBe(patch.id);
 expect([panel.hidden,patchPanel.hidden]).toEqual([false,true]);
 // the description leads each panel and follows its method
 expect(panel.firstElementChild.className).toContain('batch-mode-description');
 expect(panel.querySelector('.batch-mode-description h3').textContent).toBe('Rotate certificates and redeploy');
 expect(patchPanel.querySelector('.batch-mode-description h3').textContent).toBe('Replace certificates on the existing VMs');
 expect(d.querySelector('.rotate-op-hint:not(.rotate-mode-reason)').textContent).toContain('Choose how certificates will be applied');
 key(w,redeploy,'ArrowRight');
 expect(T.rotateState.mode).toBe('patch');expect(patch.getAttribute('aria-selected')).toBe('true');expect(d.activeElement).toBe(patch);
 expect([panel.hidden,patchPanel.hidden]).toEqual([true,false]);
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
 const patch=d.getElementById('rotate-mode-patch-tab');
 expect(patch.hidden).toBe(false);expect(patch.disabled).toBe(true);
 const reason=d.getElementById('rotate-mode-patch-reason');
 expect(reason.hidden).toBe(false);expect(reason.textContent).toContain('the switch is off');
 patch.click();expect(T.rotateState.mode).toBe('redeploy');
 key(w,d.getElementById('rotate-mode-redeploy-tab'),'ArrowRight');expect(T.rotateState.mode).toBe('redeploy');
 // a Patch that closes while chosen falls back to Redeploy and says so
 T.rotateState.mode='patch';T.rotateState.converge='pending';T.updateOperationFooter();await sleep(80);
 expect(T.rotateState.mode).toBe('redeploy');
 expect(d.getElementById('rotate-mode-redeploy-tab').getAttribute('aria-selected')).toBe('true');
 expect(d.getElementById('blacksmith-live-region').textContent).toContain('mode is back to Redeploy');
 }finally{w.close();}
});
t('After patching reads as one required choice of two options',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 d.getElementById('rotate-mode-patch-tab').click();
 const group=d.getElementById('rotate-mode-converge');
 expect(group.closest('[role="tabpanel"]').id).toBe('rotate-mode-patch-panel');
 expect(group.hidden).toBe(false);
 const options=[...group.querySelectorAll('.rotate-converge-option')];
 expect(options.length).toBe(2);
 options.forEach(o=>{expect(o.querySelector('.rotate-converge-hint').textContent.length).toBeGreaterThan(20);});
 expect(options.some(o=>o.querySelector('input').checked)).toBe(false);
 d.getElementById('rotate-mode-converge-pending').click();
 expect(T.rotateState.converge).toBe('pending');
 d.getElementById('rotate-mode-redeploy-tab').click();
 expect(group.hidden).toBe(true);expect(T.rotateState.converge).toBe('');
 }finally{w.close();}
});
t('the Service line holds its label and select on one row',async()=>{
 const {w,d,T}=await setupRotate({certs});try{
 const row=()=>d.getElementById('upgrade-service-filter').closest('.filter-row');
 // rotation filters by service on the Instances step, upgrade on Options
 T.setBatchStep(2,false);
 expect(row().parentElement.classList.contains('upgrade-services')).toBe(true);
 await T.requestOperation('upgrade');T.setBatchStep(1,false);
 expect(row().parentElement.id).toBe('batch-flow-options');
 expect([...row().children].map(n=>n.tagName)).toEqual(['LABEL','SELECT']);
 expect(d.querySelector('label[for="upgrade-service-filter"]').textContent).toBe('Service');
 }finally{w.close();}
});
