const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
test('generic sortable columns work with keyboard and announce direction',()=>{
 const {w,d}=boot();try{
 const table=d.createElement('table');table.innerHTML='<thead><tr><th>Name</th><th>Count</th></tr></thead><tbody><tr><td>Z</td><td>2</td></tr><tr><td>A</td><td>1</td></tr></tbody>';d.body.append(table);
 w.__T.initializeAllTables();const header=table.querySelector('th');
 expect(header.tabIndex).toBe(0);
 header.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));
 expect(table.tBodies[0].rows[0].cells[0].textContent).toBe('A');
 expect(header.getAttribute('aria-sort')).toBe('ascending');
 header.dispatchEvent(new w.KeyboardEvent('keydown',{key:' ',bubbles:true,cancelable:true}));
 expect(table.tBodies[0].rows[0].cells[0].textContent).toBe('Z');
 expect(header.getAttribute('aria-sort')).toBe('descending');
 }finally{w.close();}
});
test('managed dialogs isolate background and handle an empty focus list',()=>{
 const {w,d}=boot();try{
 const opener=d.createElement('button');d.body.append(opener);opener.focus();
 const previouslyInert=d.createElement('div');previouslyInert.setAttribute('inert','');d.body.append(previouslyInert);
 const overlay=d.createElement('dialog');overlay.setAttribute('aria-label','Test details');overlay.textContent='Details';d.body.append(overlay);
 w.ModalManager.openModal(overlay);
 expect(opener.hasAttribute('inert')).toBe(true);
 expect(overlay.contains(d.activeElement)).toBe(true);
 const event=new w.KeyboardEvent('keydown',{key:'Tab',bubbles:true,cancelable:true});d.activeElement.dispatchEvent(event);
 expect(event.defaultPrevented).toBe(true);
 w.ModalManager.closeModal(overlay);
 expect(opener.hasAttribute('inert')).toBe(false);
 expect(previouslyInert.hasAttribute('inert')).toBe(true);
 expect(d.activeElement).toBe(opener);
 }finally{w.close();}
});

test('enhanced sort replaces generic handlers and reports one keyboard sort',()=>{
 const {w,d}=boot();try{
 const table=d.createElement('table');table.className='tasks-table';table.innerHTML='<thead><tr><th>ID</th><th>State</th></tr></thead><tbody><tr><td>2</td><td>done</td></tr><tr><td>1</td><td>done</td></tr></tbody>';d.body.append(table);
 w.__T.tableOriginalData.set('tasks',[{id:2,state:'done'},{id:1,state:'done'}]);
 w.__T.initializeAllTables();w.__T.initializeSorting('tasks-table');w.__T.initializeSorting('tasks-table');
 const header=table.querySelector('th');header.focus();
 header.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));
 expect(header.getAttribute('aria-sort')).toBe('ascending');
 expect(d.activeElement).toBe(header);
 expect(table.tBodies[0].rows[0].cells[0].textContent.trim()).toBe('1');
 }finally{w.close();}
});
test('registration Escape closes and restores focus to its opener',async()=>{
 const {w,d,stubs}=boot();try{
 const opener=d.createElement('button');d.body.append(opener);opener.focus();
 w.__T.CFRegistrationManager.showRegistrationModal();
 const modal=d.getElementById('registration-modal');
 expect(modal.contains(d.activeElement)).toBe(true);
 expect(opener.hasAttribute('inert')).toBe(true);
 d.activeElement.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
 await stubs.settle();
 expect(modal.open).toBe(false);
 expect(d.activeElement).toBe(opener);
 expect(opener.hasAttribute('inert')).toBe(false);
 }finally{w.close();}
});

test('terminal tabs have separate named selection and close buttons',async()=>{
 const {w,d}=boot();try{
 w.eval(require('fs').readFileSync(require('path').resolve(__dirname,'..','..','ui','vendor','oat','oat.min.js'),'utf8'));
 await sleep(60);
 const manager=w.TerminalManager;
 manager.createTab('fixture',{deploymentType:'blacksmith',deploymentName:'broker',instanceId:'node/0'},d.createElement('div'));
 const tab=d.getElementById('tab-fixture');
 expect(tab.getAttribute('role')).toBe('presentation');
 expect(tab.querySelector('[role=tab]')).not.toBeNull();
 expect(tab.querySelectorAll('button').length).toBe(2);
 expect(tab.querySelector('button button')).toBeNull();
 expect(tab.querySelector('.tab-close').getAttribute('aria-label')).toBe('Close terminal broker/node/0');
 }finally{w.close();}
});

test('reopening a managed dialog cancels stale delayed removal',async()=>{
 const {w,d}=boot();try{
 const overlay=d.createElement('dialog');overlay.setAttribute('aria-label','Reopened');overlay.innerHTML='<button>Close</button>';d.body.append(overlay);
 w.ModalManager.openModal(overlay);w.ModalManager.closeModal(overlay);w.ModalManager.openModal(overlay);
 await sleep(330);
 expect(overlay.isConnected).toBe(true);
 expect(w.ModalManager.currentModal).toBe(overlay);
 expect(overlay.contains(d.activeElement)).toBe(true);
 }finally{w.close();}
});
