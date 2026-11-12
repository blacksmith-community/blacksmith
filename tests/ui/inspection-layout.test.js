const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
test('config status is explicit while APIs keep the clean config ID',()=>{
 const {w,d}=boot();try{
 d.body.innerHTML=w.__T.formatConfigs({configs:[{id:'33*',type:'cloud',name:'broker',created_at:'2026-10-25T12:00:00Z'},{id:'34',type:'cloud',name:'old',created_at:'2026-10-25T12:00:00Z'}]});
 const ids=[...d.querySelectorAll('.config-link')];expect(ids[0].textContent).toBe('33');expect(ids[0].dataset.configId).toBe('33');
 expect(d.querySelectorAll('.config-active-label').length).toBe(1);expect(d.querySelector('.config-active-label').textContent).toBe('Active');
 expect(d.querySelector('.configs-help').textContent).not.toContain('*');
 }finally{w.close()}
});
test('task metadata is one table row under seven headers, with the deployment in the title bar',async()=>{
 const {w,d}=boot();try{
 await w.showTaskDetails('1');
 const table=d.querySelector('#task-details-content .task-summary-table');expect(table?.tagName).toBe('TABLE');
 expect([...table.querySelectorAll('thead th')].map(e=>e.textContent)).toEqual(['State','Description','Started','Finished','Duration','User','Context ID']);
 expect(table.querySelectorAll('tbody tr').length).toBe(1);
 expect([...table.querySelectorAll('tbody td')].map(e=>e.dataset.label)).toEqual(['State','Description','Started','Finished','Duration','User','Context ID']);
 expect(table.querySelector('#task-description')?.tagName).toBe('TD');expect(d.querySelector('.task-details-table')).toBeNull();expect(d.querySelector('#task-details-content .task-tabs-nav')).not.toBeNull();
 expect(d.querySelector('#task-details-modal .modal-header #task-deployment')).not.toBeNull();
 }finally{w.close()}
});
test('settings describe fields and apply actions, reject fractional limits without writing',async()=>{
 const {w,d,calls}=boot();try{
 await sleep(40);d.body.innerHTML=w.__T.renderBlacksmithSettings({vault_max_versions:5,reconciler_interval:'30s'},{});w.__T.setupBlacksmithSettingsHandlers();
 for(const key of ['max-versions','reconciler-interval']){
 const field=d.getElementById('settings-'+key);expect(field.getAttribute('aria-describedby')).toContain('help-'+key);
 expect(d.getElementById('apply-'+key).getAttribute('aria-label')).toContain('Apply ');
 expect(d.getElementById('feedback-'+key).getAttribute('role')).toBe('status');
 }
 const input=d.getElementById('settings-max-versions');input.value='1.5';d.getElementById('apply-max-versions').click();await sleep(0);
 expect(input.getAttribute('aria-invalid')).toBe('true');expect(d.activeElement).toBe(input);expect(calls.filter(c=>c.startsWith('POST '))).toEqual([]);
 }finally{w.close()}
});
test('settings retain failed edits and do not overwrite edits made during a save',async()=>{
 const {w,d}=boot();try{
 await sleep(40);d.body.innerHTML=w.__T.renderBlacksmithSettings({vault_max_versions:5,reconciler_interval:'30s'},{});w.__T.setupBlacksmithSettingsHandlers();
 let complete;w.__routes.push(['/b/blacksmith/settings',()=>new Promise(resolve=>{complete=resolve})]);
 const input=d.getElementById('settings-reconciler-interval'),button=d.getElementById('apply-reconciler-interval');input.value='1m';button.click();input.value='2m';input.dispatchEvent(new w.Event('input',{bubbles:true}));
 complete({ok:true,status:200,json:async()=>({success:true,reconciler_interval:'1m0s'})});await sleep(0);
 expect(input.value).toBe('2m');expect(d.getElementById('feedback-reconciler-interval').textContent).toContain('unapplied');
 button.click();complete({ok:false,status:400,json:async()=>({error:'Invalid interval'})});await sleep(0);
 expect(input.value).toBe('2m');expect(input.getAttribute('aria-invalid')).toBe('true');expect(d.getElementById('feedback-reconciler-interval').textContent).toBe('Invalid interval');
 }finally{w.close()}
});
test('the task facts table is not sortable',async()=>{
 const {w,d}=boot();try{
 await w.showTaskDetails('1');
 expect(d.querySelector('#task-details-content .task-summary-table').dataset.sortable).toBe('false');
 }finally{w.close()}
});
