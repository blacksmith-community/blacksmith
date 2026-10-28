const {test,expect}=require('bun:test');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const fs=require('fs'),path=require('path');
const source=()=>fs.readFileSync(path.join(__dirname,'../../ui/routes.js'),'utf8');

function boot(hash){
 const dom=new JSDOM('<main id="console-main"><h1 id="console-title">Blacksmith</h1><div class="tab-content"></div></main>',{url:'http://localhost/'+(hash||''),runScripts:'outside-only',pretendToBeVisual:true});
 const w=watchUncaught(dom.window);
 if(hash)w.blacksmithViews={activate:async r=>r};
 w.eval(source());
 return {w,api:w.BlacksmithRoutes};
}
const settle=()=>new Promise(r=>setTimeout(r,35));

test('a Run command deep link parses back to the command operation',()=>{
 const {w,api}=boot();
 const route={screen:'upgrade',view:'new',operation:'command',step:1};
 expect(api.parse('#/batch/new/command/options')).toEqual(route);
 expect(api.parse(api.format(route))).toEqual(route);
 expect(api.parse('#/batch/new/command/execute')===null).toBe(true);
 w.close();
});

test('the breadcrumb names Run command on a command deep link',async()=>{
 const {w}=boot('#/batch/new/command/options');
 await settle();
 const crumbs=[...w.document.querySelectorAll('nav[aria-label="Breadcrumb"] li')].map(li=>li.textContent);
 expect(crumbs).toEqual(['Batch Operations','Run command','Options']);
 expect(w.document.title.startsWith('Options · Run command')).toBe(true);
 w.blacksmithRouter.destroy();w.close();
});
