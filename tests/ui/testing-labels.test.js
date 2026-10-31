const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const t=JSDOM?test:test.skip;

// The Testing tabs read in sentence case, tool names keep their own
// spelling, form labels carry no trailing colon, each form's main action is
// a primary button, and both Connection tabs carry the status dot.

const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); }finally{ env.w.close(); } } });

const rabbitCreds={host:'h',username:'u',password:'p',vhost:'v',service_type:'rabbitmq'};
const redisCreds={host:'h',password:'p'};
const OPERATIONS={
 rabbitmq:['test','publish','consume','queues','queue-ops','management'],
 redis:['test','info','set','get','delete','keys','command','flush']
};

async function screen(creds,details={}){
 const env=boot({noDialogs:true}); envs.push(env);
 const {w,d}=env;
 w.__routes.push(
  ['/credentials',(u,o,json)=>json(200,creds)],
  ['/details',(u,o,json)=>json(200,details)],
  ['/config',(u,o,json)=>json(404,{})],
  [/\/rabbitmq\/(rabbitmqctl|plugins)\/categories/,(u,o,json)=>json(200,[])],
  [/\/b\/i1\/(redis|rabbitmq)\/test/,(u,o,json)=>json(200,{success:true})]);
 const html=await w.__T.renderServiceTesting('i1');
 const host=d.createElement('div'); host.className='detail-content'; host.innerHTML=html; d.body.appendChild(host);
 return {...env,content:()=>d.getElementById('operation-content-i1')};
}
const tabs=(s)=>[...s.d.querySelectorAll('.operation-tab[data-instance="i1"]')];
const label=(tab)=>tab.textContent.replace(/\s+/g,' ').trim();

t('the RabbitMQ Testing tabs read in sentence case',async()=>{
 const s=await screen(rabbitCreds);
 expect(tabs(s).map(label)).toEqual(['Connection','Publish','Consume','Queue info','Queue ops','Management','rabbitmqctl','Plugins']);
});

t('the Valkey and Redis Testing tabs read in sentence case',async()=>{
 const s=await screen(redisCreds,{service_name:'redis'});
 expect(tabs(s).map(label)).toEqual(['Connection','Info','Set','Get','Delete','Keys','Command','Flush']);
});

for(const [service,creds,details,connect] of [['RabbitMQ',rabbitCreds,{},'executeRabbitMQTest'],['Redis',redisCreds,{service_name:'redis'},'executeRedisTest']]){
 t(`the ${service} Connection tab carries the status dot before and after a connection`,async()=>{
  const s=await screen(creds,details);
  const connection=tabs(s)[0];
  const dot=()=>connection.querySelectorAll('.connection-status-indicator');
  expect(dot().length).toBe(1);
  expect(dot()[0].dataset.state).toBe('idle');
  expect(connection.title).toBe('Not connected');
  await s.w[connect]('i1'); await sleep(10);
  expect(dot().length).toBe(1);
  expect(dot()[0].dataset.state).toBe('connected');
  expect(dot()[0].getAttribute('style')).toBeNull();
  expect(connection.title).toBe('Connected');
  await s.w[connect]('i1'); await sleep(10);
  expect(dot()[0].dataset.state).toBe('disconnected');
  expect(label(connection)).toBe('Connection');
 });
}

for(const [service,creds,details] of [['rabbitmq',rabbitCreds,{}],['redis',redisCreds,{service_name:'redis'}]]){
 t(`every ${service} form drops trailing colons and leads with a primary action`,async()=>{
  const s=await screen(creds,details);
  for(const operation of OPERATIONS[service]){
   if(operation!=='test'){ s.w.switchTestingOperation('i1',operation); await sleep(10); }
   const root=s.content();
   [...root.querySelectorAll('label')].forEach(l=>expect([operation,l.textContent.trim()]).not.toEqual([operation,expect.stringMatching(/:$/)]));
   const actions=[...root.querySelectorAll('button[onclick]')];
   expect(actions.length).toBe(1);
   expect([operation,actions[0].className]).toEqual([operation,'btn btn-primary']);
   expect(actions[0].textContent).not.toMatch(/^[A-Z ]{3,}$/);
   expect(root.querySelector('.execute-btn')).toBeNull();
  }
 });
}

t('Flush database is a danger primary button',async()=>{
 const s=await screen(redisCreds,{service_name:'redis'});
 s.w.switchTestingOperation('i1','flush'); await sleep(10);
 const button=s.content().querySelector('button[onclick*="executeRedisFlush"]');
 expect(button.textContent).toBe('Flush database');
 expect(button.getAttribute('data-variant')).toBe('danger');
});

t('the RabbitMQ forms use sentence-case titles and labels',async()=>{
 const s=await screen(rabbitCreds);
 const seen=[];
 for(const operation of ['publish','consume','queues','queue-ops']){
  s.w.switchTestingOperation('i1',operation); await sleep(10);
  seen.push(s.content().querySelector('h5').textContent);
  seen.push(...[...s.content().querySelectorAll('label')].map(l=>l.textContent.trim()));
 }
 expect(seen).toEqual(['Publish message','Queue name','Exchange (optional)','Message','Persistent message',
  'Consume messages','Queue name','Message count','Timeout (ms)','Auto acknowledge',
  'Queue information',
  'Queue operations','Queue name','Operation','Durable (for create operation)']);
});

t('the shared Response History clears with a secondary button',async()=>{
 const s=await screen(rabbitCreds);
 const history=s.d.querySelector('.service-testing-container > .testing-history');
 expect(history.querySelector('h5').textContent).toBe('Response history');
 const clear=history.querySelector('button[onclick*="clearTestingHistory"]');
 expect(clear.textContent).toBe('Clear history');
 expect(clear.className).toBe('btn btn-secondary');
 expect(clear.getAttribute('data-variant')).toBe('secondary');
});

test('the info button names each Testing tab the way its tab does',()=>{
 const {w}=boot();
 try{
  w.eval(fs.readFileSync(path.join(__dirname,'../../ui/view-guidance.js'),'utf8'));
  const api=w.BlacksmithViewGuidance;
  const got=Object.fromEntries(['info','set','get','delete','keys','command','flush','publish','consume','queues','queue-ops','management','rabbitmqctl','plugins']
   .map(subview=>[subview,api.get({screen:'services',section:'testing',subview}).label]));
  expect(got).toEqual({info:'Info',set:'Set',get:'Get',delete:'Delete',keys:'Keys',command:'Command',flush:'Flush',publish:'Publish',consume:'Consume',queues:'Queue info','queue-ops':'Queue ops',management:'Management',rabbitmqctl:'rabbitmqctl',plugins:'Plugins'});
 }finally{ w.close(); }
});

const crumbsFor=async(subview)=>{
 const source=fs.readFileSync(path.join(__dirname,'../../ui/routes.js'),'utf8');
 const probe=new JSDOM('',{url:'http://localhost/',runScripts:'outside-only'});
 probe.window.eval(source);
 const hash=probe.window.BlacksmithRoutes.format({screen:'services',instance:'i1',section:'testing',subview});
 probe.window.close();
 const dom=new JSDOM('<main id="console-main"><h1 id="console-title">Blacksmith</h1><div class="tab-content"></div></main>',{url:'http://localhost/'+hash,runScripts:'outside-only',pretendToBeVisual:true});
 const w=watchUncaught(dom.window);
 w.blacksmithViews={activate:async r=>r};
 w.eval(source);
 await new Promise(r=>setTimeout(r,35));
 const crumbs=[...w.document.querySelectorAll('nav[aria-label="Breadcrumb"] li')].map(li=>li.textContent);
 w.blacksmithRouter.destroy(); w.close();
 return crumbs;
};

t('the breadcrumb names the Testing tab the way the tab does',async()=>{
 expect((await crumbsFor('rabbitmqctl')).at(-1)).toBe('rabbitmqctl');
 expect((await crumbsFor('queue-ops')).at(-1)).toBe('Queue ops');
 expect((await crumbsFor('queues')).at(-1)).toBe('Queue info');
 expect((await crumbsFor('test')).at(-1)).toBe('Connection');
 expect((await crumbsFor('plugins')).at(-1)).toBe('Plugins');
});
