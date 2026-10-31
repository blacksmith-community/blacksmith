const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// The broker answers 415 to a POST under /b/cf whose Content-Type is not
// application/json, so every caller in the page must declare it.

// Routes for the page's own load, so a console error means something broke.
const QUIET=[[/\/b\/status$/,(u,o,j)=>j(200,{env:'Local fixture',instances:{},plans:{}})],[/\/b\/instance$/,(u,o,j)=>j(200,{deployment:'blacksmith'})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],[/inventory\?summary_only=true/,(u,o,j)=>j(200,{summary:{certificates:0,needs_attention:0}})]];

const envs=[];
afterEach(()=>{while(envs.length){const env=envs.pop();expect(env.alerts).toEqual([]);expect(env.errors).toEqual([]);env.w.close();}});

async function page(){
 const env=boot({noDialogs:true,oat:true,routes:QUIET});envs.push(env);
 // Every POST under /b/cf, with the Content-Type it declared.
 const posts=[];
 env.w.__routes.unshift([/\/b\/cf\//,(u,o)=>{
  if((o.method||'GET')==='POST'){
   const header=Object.entries(o.headers||{}).find(([name])=>name.toLowerCase()==='content-type');
   posts.push({url:u,type:header?header[1]:null});
  }
  return undefined;
 }]);
 await sleep(40);
 return {...env,posts};
}

t('connecting a saved endpoint posts JSON',async()=>{
 const {w,d,posts}=await page();
 w.__routes.push(['/b/cf/endpoints/lab/connect',(u,o,j)=>j(200,{success:true,endpoint:'lab',name:'lab',message:'Connected to lab'})]);
 d.getElementById('blacksmith').innerHTML=w.__T.renderBrokerTab();
 const manager=w.CFEndpointManager;
 manager.endpoints={lab:{name:'lab',endpoint:'https://api.lab.example'}};
 manager.renderEndpointsList();manager.selectEndpoint('lab');
 await manager.toggleConnection();await sleep(20);
 expect(posts).toEqual([{url:'/b/cf/endpoints/lab/connect',type:'application/json'}]);
 expect(manager.connectionStates.lab.connected).toBe(true);
});

t('saving a registration posts JSON',async()=>{
 const {w,d,posts}=await page();
 w.__routes.push([/\/b\/cf\/registrations$/,(u,o,j)=>(o.method||'GET')==='POST'?j(200,{message:'ok'}):j(200,{registrations:[],count:0})]);
 w.__T.CFRegistrationManager.showRegistrationModal();
 d.getElementById('reg-name').value='lab-cf';d.getElementById('reg-api-url').value='https://api.cf.example.com';
 d.getElementById('reg-username').value='admin';d.getElementById('reg-password').value='s3cret';
 d.getElementById('save-registration-btn').click();await sleep(30);
 expect(posts).toEqual([{url:'/b/cf/registrations',type:'application/json'}]);
});

// A guard for callers the tests above don't drive: every fetch whose URL is
// written as a /b/cf literal and whose method is POST declares JSON.
t('every POST to a /b/cf literal declares a JSON body',()=>{
 const src=fs.readFileSync(path.join(__dirname,'../../ui/blacksmith.js'),'utf8');
 const found=/fetch\(\s*[`'"]\/b\/cf\//g;
 const posts=[];
 let match;
 while((match=found.exec(src))){
  let depth=0,end=match.index;
  for(let i=match.index+'fetch'.length;i<src.length;i++){
   if(src[i]==='(') depth++;
   else if(src[i]===')'&&--depth===0){end=i;break;}
  }
  const call=src.slice(match.index,end+1);
  if(/method:\s*['"]POST['"]/.test(call)) posts.push(call);
 }
 expect(posts.length).toBeGreaterThanOrEqual(4);
 for(const call of posts) expect({call,json:/['"]Content-Type['"]:\s*['"]application\/json['"]/.test(call)}).toEqual({call,json:true});
});
