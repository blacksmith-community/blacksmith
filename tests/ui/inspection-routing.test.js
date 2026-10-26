const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
test('inspection open and close use router without issuing direct requests',async()=>{
 const {w,calls}=boot();const seen=[];
 w.blacksmithRouter={applying:false,current:{inspection:{kind:'task',id:'42'}},inspect:async x=>{seen.push(x);return true;},closeInspection:()=>seen.push('close')};
 await w.showTaskDetails(42);
 expect(seen[0]).toEqual({kind:'task',id:'42'});
 w.hideTaskDetailsModal();expect(seen[1]).toBe('close');
 await w.showConfigVersions('cloud','a/b','7');expect(seen[2]).toEqual({kind:'config',id:'7',type:'cloud',name:'a/b'});
 w.blacksmithRouter.current.inspection.kind='config';w.hideConfigVersionsModal();expect(seen[3]).toBe('close');
 expect(calls.some(x=>/\/b\/(tasks|configs)\//.test(x))).toBe(false);w.close();
});
test('a closed task ignores delayed details and does not restart polling',async()=>{
 const {w,d}=boot();let finish;
 w.__routes.push(['/b/tasks/42',(u,o,j)=>new Promise(resolve=>{finish=()=>resolve(j(200,{task:{id:42,state:'processing',description:'late response'}}));})]);
 const pending=w.showTaskDetails(42);await sleep(10);w.hideTaskDetailsModal();finish();await pending;
 expect(d.querySelector('#task-details-modal').getAttribute('aria-hidden')).toBe('true');
 expect(d.querySelector('#task-description').textContent).not.toBe('late response');
 expect(w.taskModalAutoRefresh).toBeFalsy();w.close();
});
test('a closed config ignores delayed versions',async()=>{
 const {w,d}=boot();let finish;
 w.__routes.push([/\/b\/configs\/cloud\/a%2Fb\/versions/,(u,o,j)=>new Promise(resolve=>{finish=()=>resolve(j(200,{configs:[{id:'7',content:'late config'}]}));})]);
 const pending=w.showConfigVersions('cloud','a/b','7');await sleep(10);
 expect(typeof finish).toBe('function');w.hideConfigVersionsModal();finish();await pending;
 expect(d.querySelector('#config-details-modal').getAttribute('aria-hidden')).toBe('true');
 expect(w.currentConfigInfo).toBeNull();w.close();
});

test('config inspection restores version, tab and diff pair per config without mutations',async()=>{
 const {w,d,calls}=boot();try{
 w.__routes.push([/\/b\/configs\/cloud\/[^/]+\/versions/,(u,o,j)=>j(200,{configs:[{id:'3'},{id:'2'},{id:'1'}]})]);
 w.__routes.push([/\/b\/configs\/cloud\/[^/]+\/diff/,(u,o,j)=>j(200,{diff:'restored comparison'})]);
 w.__routes.push([/\/b\/configs\/[123]$/,(u,o,j)=>j(200,{content:'version '+u.split('/').pop()})]);
 await w.showConfigVersions('cloud','alpha','3');await sleep(30);
 const old=d.querySelector('.version-item[data-version-id="1"]');await w.loadConfigVersion('1',old);
 d.querySelector('.manifest-tab-btn[data-group="config-tabs"][data-tab="diffs"]').click();await sleep(25);
 const select=d.querySelector('#diff-version-selector');select.value='1|2';select.dispatchEvent(new w.Event('change'));await sleep(20);
 w.hideConfigVersionsModal();await w.showConfigVersions('cloud','beta','3');await sleep(30);
 expect(d.querySelector('.version-item.selected').dataset.versionId).toBe('3');
 w.hideConfigVersionsModal();await w.showConfigVersions('cloud','alpha','3');await sleep(40);
 expect(d.querySelector('.version-item.selected').dataset.versionId).toBe('1');
 expect(d.querySelector('.manifest-tab-btn[data-group="config-tabs"].active').dataset.tab).toBe('diffs');
 expect(d.querySelector('#diff-version-selector').value).toBe('1|2');
 expect(calls.some(x=>/^(POST|PUT|DELETE) /.test(x))).toBe(false);
 }finally{w.close()}
});

test('a delayed comparison cannot write into another config inspection',async()=>{
 const {w,d}=boot();try{
 w.__routes.push([/\/b\/configs\/cloud\/[^/]+\/versions/,(u,o,j)=>j(200,{configs:[{id:'2'},{id:'1'}]})]);
 w.__routes.push([/\/b\/configs\/[12]$/,(u,o,j)=>j(200,{content:'content'})]);
 let finish;
 w.__routes.push([/\/b\/configs\/cloud\/alpha\/diff/,(u,o,j)=>new Promise(resolve=>{finish=()=>resolve(j(200,{diff:'late alpha'}));})]);
 await w.showConfigVersions('cloud','alpha','2');await sleep(25);
 const pending=w.loadConfigDiffById('1','2');await sleep(5);
 w.hideConfigVersionsModal();await w.showConfigVersions('cloud','beta','2');await sleep(25);
 finish();await pending;
 expect(d.querySelector('#diff-output').textContent).not.toContain('late alpha');
 expect(w.currentConfigInfo.name).toBe('beta');
 }finally{w.close()}
});
