const {test,expect}=require('bun:test');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const fs=require('fs'),path=require('path');
const source=()=>fs.readFileSync(path.join(__dirname,'../../ui/routes.js'),'utf8');
function boot(){const dom=new JSDOM('<main id="console-main"><h1 id="console-title">Blacksmith</h1></main>',{url:'http://localhost/',runScripts:'outside-only',pretendToBeVisual:true}); const w=watchUncaught(dom.window);w.eval(source());return {w,api:w.BlacksmithRoutes};}
test('routes preserve encoded stable IDs and reject unknown paths',()=>{const {api,w}=boot();const route={screen:'blacksmith',section:'plans',service:'service/a',plan:'cluster #2'};expect(api.parse(api.format(route))).toEqual(route);expect(api.parse('#/broker/nope')).toBeNull();expect(api.parse('#/instances/%ZZ/logs')).toBeNull();expect(api.parse('#/batch/new/rotate/execute')).toBeNull();w.close();});
test('history restores destinations without replaying navigation or actions',async()=>{const {api,w}=boot();const calls=[];const router=api.create(w,{activate:async r=>{calls.push(r);return r;}});await router.start();await router.navigate({screen:'blacksmith',section:'plans'});await router.navigate({screen:'services'});const count=w.history.length;await router.navigate({screen:'services'});expect(w.history.length).toBe(count);w.history.back();await new Promise(r=>setTimeout(r,35));expect(router.current.section).toBe('plans');expect(w.history.length).toBe(count);expect(calls.map(c=>c.section||c.screen)).toEqual(['details','plans','services','plans']);router.destroy();w.close();});
test('late activation cannot replace a newer route; failures are recoverable',async()=>{const {api,w}=boot();let release;const router=api.create(w,{activate:async(r,{signal})=>{if(r.section==='plans')await new Promise(resolve=>release=resolve);return signal.aborted?null:r;}});await router.start();const first=router.navigate({screen:'blacksmith',section:'plans'});await router.navigate({screen:'certificates'});release();await first;expect(router.current.screen).toBe('certificates');expect(w.location.hash).toBe('#/certificates');router.destroy();w.close();});
test('direct entry has a parent fallback instead of guessing history length',async()=>{const {api,w}=boot();w.history.replaceState(null,'','#/broker/plans');const router=api.create(w,{activate:async r=>r});await router.start();await router.back();expect(router.current).toEqual({screen:'blacksmith',section:'details'});router.destroy();w.close();});
test('Back and Forward restore the destination entry focus and scroll',async()=>{
 const {api,w}=boot(); const d=w.document;
 d.body.innerHTML='<div class="tab-panel"><button id="summary">Summary action</button><button id="plans">Plans action</button><button id="services">Instances action</button></div>';
 w.HTMLElement.prototype.getClientRects=function(){return [1]};
 const panel=d.querySelector('.tab-panel');
 const router=api.create(w,{activate:async r=>r,changed:r=>d.getElementById(r.section==='details'?'summary':r.section==='plans'?'plans':'services').focus()});
 await router.start();panel.scrollTop=14;
 await router.navigate({screen:'blacksmith',section:'plans'});panel.scrollTop=41;
 await router.navigate({screen:'services'});panel.scrollTop=87;
 w.history.back();await new Promise(r=>setTimeout(r,35));
 expect(d.activeElement.id).toBe('plans');expect(panel.scrollTop).toBe(41);
 w.history.back();await new Promise(r=>setTimeout(r,35));
 expect(d.activeElement.id).toBe('summary');expect(panel.scrollTop).toBe(14);
 w.history.forward();await new Promise(r=>setTimeout(r,35));
 expect(d.activeElement.id).toBe('plans');expect(panel.scrollTop).toBe(41);
 router.destroy();w.close();
});
test('failed destination restores the last usable view and reports the error',async()=>{
 const {api,w}=boot();let shown, reported;
 const router=api.create(w,{activate:async r=>{shown=r;if(r.plan==='removed')throw new Error('Plan no longer exists');return r;},error:e=>reported=e.message});
 await router.start();await router.navigate({screen:'services'});
 expect(await router.navigate({screen:'blacksmith',section:'plans',service:'a',plan:'removed'})).toBe(false);
 expect(shown).toEqual({screen:'services'});expect(router.current).toEqual(shown);expect(w.location.hash).toBe('#/instances');expect(reported).toBe('Plan no longer exists');
 router.destroy();w.close();
});
test('failed direct entry has a usable fallback and never silently selects another resource',async()=>{
 const {api,w}=boot();let shown,reported;
 w.history.replaceState(null,'','#/instances/missing/logs');
 const router=api.create(w,{activate:async r=>{shown=r;if(r.instance)throw new Error('Instance no longer exists');return r;},error:e=>reported=e.message});
 expect(await router.start()).toBe(false);
 expect(shown).toEqual({screen:'blacksmith',section:'details'});expect(router.current).toEqual(shown);expect(w.location.hash).toBe('#/broker/summary');expect(reported).toBe('Instance no longer exists');
 router.destroy();w.close();
});

test('canceling pending navigation restores source without history or late redirect',async()=>{
 const {api,w}=boot();let finish,shown;
 const router=api.create(w,{activate:async(r,{signal})=>{shown=r;if(r.section==='plans')await new Promise(resolve=>finish=resolve);if(signal.aborted)return null;shown=r;return r;}});
 await router.start();const length=w.history.length, source=w.location.hash;
 const cancel=new w.AbortController();const pending=router.navigate({screen:'blacksmith',section:'plans'},{signal:cancel.signal});
 cancel.abort();await new Promise(resolve=>setTimeout(resolve,0));
 expect(shown.section).toBe('details');expect(router.applying).toBe(false);
 finish();expect(await pending).toBe(false);expect(w.location.hash).toBe(source);expect(w.history.length).toBe(length);expect(router.current.section).toBe('details');
 router.destroy();w.close();
});
test('aborting an older navigation cannot cancel a newer destination',async()=>{
 const {api,w}=boot();let finish;
 const router=api.create(w,{activate:async(r,{signal})=>{if(r.section==='plans')await new Promise(resolve=>finish=resolve);return signal.aborted?null:r;}});
 await router.start();const cancel=new w.AbortController();const pending=router.navigate({screen:'blacksmith',section:'plans'},{signal:cancel.signal});
 await router.navigate({screen:'certificates'});cancel.abort();finish();await pending;
 expect(router.current.screen).toBe('certificates');expect(w.location.hash).toBe('#/certificates');router.destroy();w.close();
});

test('closing an inspection returns to its known source without creating another entry',async()=>{
 const {api,w}=boot();const router=api.create(w,{activate:async r=>r});await router.start();await router.navigate({screen:'blacksmith',section:'tasks'});
 await router.inspect({kind:'task',id:'42'});router.closeInspection();await new Promise(r=>setTimeout(r,35));
 expect(router.current).toEqual({screen:'blacksmith',section:'tasks'});
 w.history.back();await new Promise(r=>setTimeout(r,35));expect(router.current.section).toBe('details');
 w.history.forward();await new Promise(r=>setTimeout(r,35));expect(router.current.section).toBe('tasks');expect(router.current.inspection).toBeUndefined();
 router.destroy();w.close();
});
test('direct inspection closes by replacing its entry and nested inspection returns to its opener',async()=>{
 const {api,w}=boot();w.history.replaceState(null,'','#/broker/tasks?inspect=task&id=42');const router=api.create(w,{activate:async r=>r});await router.start();const count=w.history.length;
 await router.closeInspection();expect(router.current.inspection).toBeUndefined();expect(w.history.length).toBe(count);expect(w.location.hash).toBe('#/broker/tasks');
 await router.inspect({kind:'task',id:'42'});await router.inspect({kind:'task',id:'43'});router.closeInspection();await new Promise(r=>setTimeout(r,35));expect(router.current.inspection.id).toBe('42');
 router.destroy();w.close();
});

test('an instance section shows in the breadcrumb and title before its data loads',async()=>{
 const {api,w}=boot();const d=w.document;let release;
 const pending=[];
 const router=api.create(w,{activate:async r=>{if(r.section==='instance-logs')await new Promise(resolve=>release=resolve);return r;},pending:r=>pending.push(r.section)});
 await router.start();
 const moved=router.navigate({screen:'services',instance:'abc',section:'instance-logs'});
 expect(pending).toEqual(['instance-logs']);
 release();await moved;
 await router.navigate({screen:'blacksmith',section:'plans'});
 expect(pending).toEqual(['instance-logs']);
 router.destroy();w.close();
});
test('the console renders the chosen instance section while the section loads',async()=>{
 const {w}=boot();const d=w.document;let release;
 d.body.insertAdjacentHTML('beforeend','<div class="tab-content"></div>');
 w.blacksmithViews={activate:async r=>{if(r.section==='instance-logs')await new Promise(resolve=>release=resolve);return r;}};
 d.dispatchEvent(new w.Event('blacksmith:ready'));
 await new Promise(resolve=>setTimeout(resolve,10));
 await w.blacksmithRouter.navigate({screen:'services',instance:'abc',section:'certificates'});
 const last=()=>d.querySelector('#route-context nav li:last-child').textContent;
 expect(last()).toBe('Certificates');expect(d.title).toContain('Certificates');
 const moved=w.blacksmithRouter.navigate({screen:'services',instance:'abc',section:'instance-logs'});
 expect(last()).toBe('Logs');expect(d.title.startsWith('Logs')).toBe(true);
 release();await moved;
 expect(last()).toBe('Logs');
 w.blacksmithRouter.destroy();w.close();
});
