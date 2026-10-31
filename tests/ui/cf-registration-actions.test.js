const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// Routes for the page's own load, so a console error means something broke.
const QUIET=[[/\/b\/status$/,(u,o,j)=>j(200,{env:'Local fixture',instances:{},plans:{}})],[/\/b\/instance$/,(u,o,j)=>j(200,{deployment:'blacksmith'})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],[/inventory\?summary_only=true/,(u,o,j)=>j(200,{summary:{certificates:0,needs_attention:0}})]];

const envs=[];
afterEach(()=>{while(envs.length){const env=envs.pop();expect(env.alerts).toEqual([]);expect(env.errors).toEqual([]);env.w.close();}});

t('a registration card offers no Sync action',async()=>{
 const env=boot({noDialogs:true,oat:true,routes:QUIET});envs.push(env);
 await sleep(40);
 const manager=env.w.__T.CFRegistrationManager;
 const holder=env.d.createElement('div');
 holder.innerHTML=manager.renderRegistrationCard({id:'reg-1',name:'lab-cf',api_url:'https://api.cf.example.com',username:'admin',status:'active'});
 const actions=[...holder.querySelectorAll('.registration-actions button')].map(b=>b.textContent.trim());
 expect(actions).toEqual(['Re-register','View Progress','Edit','Delete']);
 expect(holder.querySelector('.sync-registration-btn')).toBeNull();
 expect(manager.syncRegistration).toBeUndefined();
});

// The broker has no sync route, so nothing in the page may call one.
t('the page never posts to a registration sync route',()=>{
 const src=fs.readFileSync(path.join(__dirname,'../../ui/blacksmith.js'),'utf8');
 const calls=src.match(/\/b\/cf\/registrations\/[^`'"]*\/sync/g)||[];
 expect(calls).toEqual([]);
});
