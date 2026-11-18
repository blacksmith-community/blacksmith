const {test,expect}=require('bun:test');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const {main:{idle,until,trackTimers,TEST_LIMIT}}=require('./certificates/harness.js');
const t=(n,f,l)=>test(n,f,l===undefined?TEST_LIMIT:l);
const fs=require('fs'),path=require('path');
const source=()=>fs.readFileSync(path.join(__dirname,'../../ui/routes.js'),'utf8');
function boot(){const dom=new JSDOM('<main id="console-main"><h1 id="console-title">Blacksmith</h1></main>',{url:'http://localhost/',runScripts:'outside-only',pretendToBeVisual:true}); const w=watchUncaught(dom.window);trackTimers(w);w.eval(source());return {w,api:w.BlacksmithRoutes};}
t('routes preserve encoded stable IDs and reject unknown paths',()=>{const {api,w}=boot();const route={screen:'blacksmith',section:'plans',service:'service/a',plan:'cluster #2'};expect(api.parse(api.format(route))).toEqual(route);expect(api.parse('#/broker/nope')).toBeNull();expect(api.parse('#/instances/%ZZ/logs')).toBeNull();expect(api.parse('#/batch/new/rotate/execute')).toBeNull();w.close();});
t('history restores destinations without replaying navigation or actions',async()=>{const {api,w}=boot();const calls=[];const router=api.create(w,{activate:async r=>{calls.push(r);return r;}});await router.start();await router.navigate({screen:'blacksmith',section:'plans'});await router.navigate({screen:'services'});const count=w.history.length;await router.navigate({screen:'services'});expect(w.history.length).toBe(count);w.history.back();await idle(w);expect(router.current.section).toBe('plans');expect(w.history.length).toBe(count);expect(calls.map(c=>c.section||c.screen)).toEqual(['details','plans','services','plans']);router.destroy();w.close();});
t('late activation cannot replace a newer route; failures are recoverable',async()=>{const {api,w}=boot();let release;const router=api.create(w,{activate:async(r,{signal})=>{if(r.section==='plans')await new Promise(resolve=>release=resolve);return signal.aborted?null:r;}});await router.start();const first=router.navigate({screen:'blacksmith',section:'plans'});await router.navigate({screen:'certificates'});release();await first;expect(router.current.screen).toBe('certificates');expect(w.location.hash).toBe('#/certificates');router.destroy();w.close();});
t('direct entry has a parent fallback instead of guessing history length',async()=>{const {api,w}=boot();w.history.replaceState(null,'','#/broker/plans');const router=api.create(w,{activate:async r=>r});await router.start();await router.back();expect(router.current).toEqual({screen:'blacksmith',section:'details'});router.destroy();w.close();});
t('Back and Forward restore the destination entry focus and scroll',async()=>{
 const {api,w}=boot(); const d=w.document;
 d.body.innerHTML='<div class="tab-panel"><button id="summary">Summary action</button><button id="plans">Plans action</button><button id="services">Instances action</button></div>';
 w.HTMLElement.prototype.getClientRects=function(){return [1]};
 const panel=d.querySelector('.tab-panel');
 const router=api.create(w,{activate:async r=>r,changed:r=>d.getElementById(r.section==='details'?'summary':r.section==='plans'?'plans':'services').focus()});
 await router.start();panel.scrollTop=14;
 await router.navigate({screen:'blacksmith',section:'plans'});panel.scrollTop=41;
 await router.navigate({screen:'services'});panel.scrollTop=87;
 // jsdom delivers popstate on its own clock, so each step waits for the focus and scroll it should restore.
 w.history.back();await until(()=>d.activeElement.id==='plans'&&panel.scrollTop===41,'the previous view to be restored');
 expect(d.activeElement.id).toBe('plans');expect(panel.scrollTop).toBe(41);
 w.history.back();await until(()=>d.activeElement.id==='summary'&&panel.scrollTop===14,'the first view to be restored');
 expect(d.activeElement.id).toBe('summary');expect(panel.scrollTop).toBe(14);
 w.history.forward();await until(()=>d.activeElement.id==='plans'&&panel.scrollTop===41,'the next view to be restored');
 expect(d.activeElement.id).toBe('plans');expect(panel.scrollTop).toBe(41);
 router.destroy();w.close();
});
t('failed destination restores the last usable view and reports the error',async()=>{
 const {api,w}=boot();let shown, reported;
 const router=api.create(w,{activate:async r=>{shown=r;if(r.plan==='removed')throw new Error('Plan no longer exists');return r;},error:e=>reported=e.message});
 await router.start();await router.navigate({screen:'services'});
 expect(await router.navigate({screen:'blacksmith',section:'plans',service:'a',plan:'removed'})).toBe(false);
 expect(shown).toEqual({screen:'services'});expect(router.current).toEqual(shown);expect(w.location.hash).toBe('#/instances');expect(reported).toBe('Plan no longer exists');
 router.destroy();w.close();
});
t('failed direct entry has a usable fallback and never silently selects another resource',async()=>{
 const {api,w}=boot();let shown,reported;
 w.history.replaceState(null,'','#/instances/missing/logs');
 const router=api.create(w,{activate:async r=>{shown=r;if(r.instance)throw new Error('Instance no longer exists');return r;},error:e=>reported=e.message});
 expect(await router.start()).toBe(false);
 expect(shown).toEqual({screen:'blacksmith',section:'details'});expect(router.current).toEqual(shown);expect(w.location.hash).toBe('#/broker/summary');expect(reported).toBe('Instance no longer exists');
 router.destroy();w.close();
});

t('canceling pending navigation restores source without history or late redirect',async()=>{
 const {api,w}=boot();let finish,shown;
 const router=api.create(w,{activate:async(r,{signal})=>{shown=r;if(r.section==='plans')await new Promise(resolve=>finish=resolve);if(signal.aborted)return null;shown=r;return r;}});
 await router.start();const length=w.history.length, source=w.location.hash;
 const cancel=new w.AbortController();const pending=router.navigate({screen:'blacksmith',section:'plans'},{signal:cancel.signal});
 cancel.abort();await idle(w);
 expect(shown.section).toBe('details');expect(router.applying).toBe(false);
 finish();expect(await pending).toBe(false);expect(w.location.hash).toBe(source);expect(w.history.length).toBe(length);expect(router.current.section).toBe('details');
 router.destroy();w.close();
});
t('aborting an older navigation cannot cancel a newer destination',async()=>{
 const {api,w}=boot();let finish;
 const router=api.create(w,{activate:async(r,{signal})=>{if(r.section==='plans')await new Promise(resolve=>finish=resolve);return signal.aborted?null:r;}});
 await router.start();const cancel=new w.AbortController();const pending=router.navigate({screen:'blacksmith',section:'plans'},{signal:cancel.signal});
 await router.navigate({screen:'certificates'});cancel.abort();finish();await pending;
 expect(router.current.screen).toBe('certificates');expect(w.location.hash).toBe('#/certificates');router.destroy();w.close();
});

t('closing an inspection returns to its known source without creating another entry',async()=>{
 const {api,w}=boot();const router=api.create(w,{activate:async r=>r});await router.start();await router.navigate({screen:'blacksmith',section:'tasks'});
 await router.inspect({kind:'task',id:'42'});router.closeInspection();await until(()=>!router.current.inspection,'the inspection to close');await idle(w);
 expect(router.current).toEqual({screen:'blacksmith',section:'tasks'});
 w.history.back();await until(()=>router.current.section==='details','the history to go back');expect(router.current.section).toBe('details');
 w.history.forward();await until(()=>router.current.section==='tasks','the history to go forward');expect(router.current.section).toBe('tasks');expect(router.current.inspection).toBeUndefined();
 router.destroy();w.close();
});
t('direct inspection closes by replacing its entry and nested inspection returns to its opener',async()=>{
 const {api,w}=boot();w.history.replaceState(null,'','#/broker/tasks?inspect=task&id=42');const router=api.create(w,{activate:async r=>r});await router.start();const count=w.history.length;
 await router.closeInspection();expect(router.current.inspection).toBeUndefined();expect(w.history.length).toBe(count);expect(w.location.hash).toBe('#/broker/tasks');
 await router.inspect({kind:'task',id:'42'});await router.inspect({kind:'task',id:'43'});router.closeInspection();await until(()=>router.current.inspection&&router.current.inspection.id==='42','the earlier inspection to return');expect(router.current.inspection.id).toBe('42');
 router.destroy();w.close();
});

t('an instance section shows in the breadcrumb and title before its data loads',async()=>{
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
t('the console renders the chosen instance section while the section loads',async()=>{
 const {w}=boot();const d=w.document;let release;
 d.body.insertAdjacentHTML('beforeend','<div class="tab-content"></div>');
 w.blacksmithViews={activate:async r=>{if(r.section==='instance-logs')await new Promise(resolve=>release=resolve);return r;}};
 d.dispatchEvent(new w.Event('blacksmith:ready'));
 await idle(w);
 await w.blacksmithRouter.navigate({screen:'services',instance:'abc',section:'certificates'});
 const last=()=>d.querySelector('#route-context nav li:last-child').textContent;
 expect(last()).toBe('Certificates');expect(d.title).toContain('Certificates');
 const moved=w.blacksmithRouter.navigate({screen:'services',instance:'abc',section:'instance-logs'});
 expect(last()).toBe('Logs');expect(d.title.startsWith('Logs')).toBe(true);
 release();await moved;
 expect(last()).toBe('Logs');
 w.blacksmithRouter.destroy();w.close();
});
t('the breadcrumb bar is the first row of the content column',async()=>{
 const {w}=boot();const d=w.document;
 d.body.insertAdjacentHTML('beforeend','<main id="console-main"><div class="wrap" id="shell"><div class="tab-content"></div></div></main>');
 w.blacksmithViews={activate:async r=>r};
 d.dispatchEvent(new w.Event('blacksmith:ready'));
 await idle(w);
 await w.blacksmithRouter.navigate({screen:'services',instance:'abc',section:'certificates'});
 const bar=d.getElementById('route-context');
 expect(bar.nextElementSibling.classList.contains('tab-content')).toBe(true);
 expect(bar.parentElement.firstElementChild).toBe(bar);
 expect(d.querySelector('#console-main .wrap').firstElementChild).toBe(bar);
 // A second render keeps the bar where it is.
 await w.blacksmithRouter.navigate({screen:'services',instance:'abc',section:'instance-logs'});
 expect(d.getElementById('route-context')).toBe(bar);
 expect(bar.nextElementSibling.classList.contains('tab-content')).toBe(true);
 w.blacksmithRouter.destroy();w.close();
});
t('operations is the canonical instance route and testing stays an alias',async()=>{
 const {api,w}=boot();
 const route={screen:'services',instance:'inst-1',section:'testing'};
 expect(api.format(route)).toBe('#/instances/inst-1/operations');
 expect(api.parse('#/instances/inst-1/operations')).toEqual(route);
 expect(api.parse('#/instances/inst-1/testing')).toEqual(route);
 w.history.replaceState(null,'','#/instances/inst-1/testing');
 const router=api.create(w,{activate:async r=>r});await router.start();
 expect(w.location.hash).toBe('#/instances/inst-1/operations');
 router.destroy();w.close();
});
