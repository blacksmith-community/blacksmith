const {test,expect}=require('bun:test');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const fs=require('fs'),path=require('path');
const source=()=>fs.readFileSync(path.join(__dirname,'../../ui/routes.js'),'utf8');
function boot(hash='',activate){
 const dom=new JSDOM('<main id="console-main"><h1 id="console-title">Blacksmith</h1><div class="tab-content"></div></main>',{url:'http://localhost/'+hash,runScripts:'outside-only',pretendToBeVisual:true});
 const w=watchUncaught(dom.window);
 if(activate)w.blacksmithViews={activate};
 w.eval(source());
 return {w,api:w.BlacksmithRoutes};
}

test('routes without a tab parse to the default tab of their screen',()=>{
 const {api,w}=boot();
 expect(api.parse('#/instances/abc')).toEqual({screen:'services',instance:'abc',section:'details'});
 expect(api.parse('#/instances/abc?view=x')).toEqual({screen:'services',instance:'abc',section:'details',subview:'x'});
 expect(api.parse('#/broker')).toEqual({screen:'blacksmith',section:'details'});
 expect(api.parse('#/batch')).toEqual({screen:'upgrade',view:'new',operation:'upgrade',step:0});
 expect(api.parse('#/batch/new')).toEqual({screen:'upgrade',view:'new',operation:'upgrade',step:0});
 expect(api.parse('#/batch/new/rotate')).toEqual({screen:'upgrade',view:'new',operation:'rotate',step:0});
 expect(api.parse('#/certificates')).toEqual({screen:'certificates'});
 expect(api.parse('#/instances/abc/nope')).toBeNull();
 expect(api.parse('#/batch/new/nope')).toBeNull();
 w.close();
});

test('a bare instance route is replaced by its Summary route without a new entry',async()=>{
 const {api,w}=boot('#/instances/abc');
 const length=w.history.length;
 const router=api.create(w,{activate:async r=>r});
 expect(await router.start()).toBe(true);
 expect(w.location.hash).toBe('#/instances/abc/summary');
 expect(w.history.length).toBe(length);
 router.destroy();w.close();
});

test('the navigation error clears once another route renders',async()=>{
 const {w}=boot('#/instances/missing/logs',async r=>{if(r.instance==='missing')throw new Error('Service instance missing was not found.');return r;});
 await new Promise(r=>setTimeout(r,20));
 expect(w.document.getElementById('route-error').textContent).toContain('Service instance missing was not found.');
 // The recovered view is already current, so selecting it again is a success.
 await w.blacksmithRouter.navigate({screen:'blacksmith',section:'details'});
 expect(w.document.getElementById('route-error')).toBeNull();
 w.blacksmithRouter.destroy();w.close();
});

test('the navigation error clears when the user changes view in place',async()=>{
 const {w}=boot('#/instances/missing/logs',async r=>{if(r.instance==='missing')throw new Error('gone');return r;});
 await new Promise(r=>setTimeout(r,20));
 expect(w.document.getElementById('route-error')).not.toBeNull();
 w.blacksmithRouter.record({screen:'blacksmith',section:'details'});
 expect(w.document.getElementById('route-error')).toBeNull();
 w.blacksmithRouter.destroy();w.close();
});

test('an unknown instance error names the instance that was not found',()=>{
 const src=fs.readFileSync(path.join(__dirname,'../../ui/blacksmith.js'),'utf8');
 expect(src).toContain('Service instance ${route.instance} was not found.');
});
