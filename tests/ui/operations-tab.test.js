const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const t=JSDOM?test:test.skip;

const envs=[];
afterEach(()=>{ while(envs.length){ envs.pop().w.close(); } });

const rabbitCreds={host:'h',username:'u',password:'p',vhost:'v',service_type:'rabbitmq'};
const redisCreds={host:'h',password:'p'};

async function screen(creds,details={}){
 const env=boot({noDialogs:true}); envs.push(env);
 const {w,d}=env;
 w.__routes.push(
  ['/credentials',(u,o,json)=>json(200,creds)],
  ['/details',(u,o,json)=>json(200,details)],
  ['/config',(u,o,json)=>json(404,{})],
  [/\/rabbitmq\/(rabbitmqctl|plugins)\/categories/,(u,o,json)=>json(200,[])]);
 const host=d.createElement('div'); host.innerHTML=await w.__T.renderServiceTesting('i1'); d.body.appendChild(host);
 return env;
}
const groups=(d)=>[...d.querySelectorAll('.operation-tab-group')].map((g)=>({
 heading:g.querySelector('h4').textContent,
 labelled:g.getAttribute('aria-labelledby')===g.querySelector('h4').id,
 tabs:[...g.querySelectorAll('.operation-tab')].map((b)=>b.dataset.operation)}));

t('the RabbitMQ tab strip separates the connection check from what changes the instance',async()=>{
 const {d}=await screen(rabbitCreds);
 expect(groups(d)).toEqual([
  {heading:'Connection check',labelled:true,tabs:['test']},
  {heading:'Read-only inspection',labelled:true,tabs:['queues']},
  {heading:'Changes the instance',labelled:true,tabs:['publish','consume','queue-ops','management','rabbitmqctl','plugins']}]);
});
t('the Redis tab strip puts every write under the changing heading',async()=>{
 const {d}=await screen(redisCreds,{service_name:'redis'});
 expect(groups(d)).toEqual([
  {heading:'Connection check',labelled:true,tabs:['test']},
  {heading:'Read-only inspection',labelled:true,tabs:['info','get','keys']},
  {heading:'Changes the instance',labelled:true,tabs:['set','delete','command','flush']}]);
});
t('the first tab stays the active connection check',async()=>{
 const {d}=await screen(redisCreds,{service_name:'redis'});
 const active=[...d.querySelectorAll('.operation-tab.active')];
 expect(active.map((b)=>b.dataset.operation)).toEqual(['test']);
});

t('the instance sections, the route label, and the help all say Operations',()=>{
 const {w,d}=boot();try{
 const host=d.createElement('div');host.innerHTML=w.__T.renderServiceDetail('id-1',{service_id:'valkey',plan_id:'cluster'},null);
 expect(host.querySelector('[data-tab="testing"]').textContent).toBe('Operations');
 expect(host.querySelector('select option[value="testing"]').textContent).toBe('Operations');
 w.eval(fs.readFileSync(path.join(__dirname,'../../ui/view-guidance.js'),'utf8'));
 expect(w.BlacksmithViewGuidance.get({screen:'services',section:'testing'}).label).toBe('Operations');
 }finally{w.close();}
});
t('the quick navigation entry reads Operations and still finds the old words',()=>{
 const {w}=boot();try{
 w.eval(fs.readFileSync(path.join(__dirname,'../../ui/navigation.js'),'utf8'));
 const nav=w.BlacksmithNavigation;
 const entries=nav.destinations([],{'cache-1':{instance_name:'Orders cache',service_id:'valkey'}});
 const hit=nav.search(entries,'orders operations')[0];
 expect(hit.section).toBe('testing');
 expect(hit.breadcrumb).toContain('Operations');
 expect(nav.search(entries,'orders testing')[0].section).toBe('testing');
 }finally{w.close();}
});
t('the breadcrumb says Operations and the old route still opens',async()=>{
 const source=fs.readFileSync(path.join(__dirname,'../../ui/routes.js'),'utf8');
 const probe=new JSDOM('',{url:'http://localhost/',runScripts:'outside-only'});
 probe.window.eval(source);
 const hash=probe.window.BlacksmithRoutes.format({screen:'services',instance:'i1',section:'testing'});
 probe.window.close();
 expect(hash).toContain('testing');
 const dom=new JSDOM('<main id="console-main"><h1 id="console-title">Blacksmith</h1><div class="tab-content"></div></main>',{url:'http://localhost/'+hash,runScripts:'outside-only',pretendToBeVisual:true});
 const w=watchUncaught(dom.window);
 w.blacksmithViews={activate:async r=>r};
 w.eval(source);
 await new Promise(r=>setTimeout(r,35));
 const crumbs=[...w.document.querySelectorAll('nav[aria-label="Breadcrumb"] li')].map(li=>li.textContent);
 w.blacksmithRouter.destroy(); w.close();
 expect(crumbs.at(-1)).toBe('Operations');
});
