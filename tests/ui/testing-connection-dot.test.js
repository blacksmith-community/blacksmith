const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const envs=[];
afterEach(()=>{ while(envs.length) envs.pop().w.close(); });

const rabbitCreds={host:'10.0.0.6',port:5672,username:'app',password:'s3cr3t-test',vhost:'orders',service_type:'rabbitmq',
 protocols:{amqp:{host:'10.0.0.6',port:5672},amqps:{host:'10.0.0.6',port:5671}}};

async function screen(creds,routes=[]){
 const env=boot({noDialogs:true}); envs.push(env);
 const {w,d}=env;
 w.__routes.push(...routes,
  ['/credentials',(u,o,json)=>json(200,creds)],
  ['/details',(u,o,json)=>json(200,{})],
  ['/config',(u,o,json)=>json(404,{})]);
 const html=await w.__T.renderServiceTesting('i1');
 const host=d.createElement('div'); host.className='detail-content'; host.innerHTML=html; d.body.appendChild(host);
 return env;
}
const dot=(d)=>d.querySelector('.operation-tab[data-operation="test"] .connection-status-indicator').getAttribute('data-state');

t('the Connection dot stays neutral after switching tabs before any attempt',async()=>{
 const {w,d}=await screen(rabbitCreds);
 expect(dot(d)).toBe('idle');
 w.switchTestingOperation('i1','queues'); await sleep(80);
 w.switchTestingOperation('i1','rabbitmqctl'); await sleep(80);
 expect(dot(d)).toBe('idle');
 expect(d.querySelector('.operation-tab[data-operation="test"]').title).toBe('Not connected');
});

t('the Connection dot is red after a failed attempt and stays so across tab switches',async()=>{
 const {w,d}=await screen(rabbitCreds,[[/rabbitmq\/test/,(u,o,json)=>json(502,{error:'connection refused'})]]);
 w.executeRabbitMQTest('i1'); await sleep(50);
 expect(dot(d)).toBe('disconnected');
 w.switchTestingOperation('i1','queues'); await sleep(80);
 expect(dot(d)).toBe('disconnected');
});
