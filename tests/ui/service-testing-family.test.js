const {test,expect}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// Renders the testing screen for an instance whose credentials and details
// come back as given, and returns the markup with the page it rendered in.
async function screen(creds,details={}){
 const b=boot();
 b.w.__routes.push(
  ['/credentials',(u,o,json)=>json(200,creds)],
  ['/details',(u,o,json)=>json(200,details)],
  ['/config',(u,o,json)=>json(404,{})]);
 try{
  const html=await b.w.__T.renderServiceTesting('i1');
  return {html,w:b.w,close:()=>b.w.close()};
 }catch(e){b.w.close();throw e;}
}
// Credentials as the lab Valkey instances return them: no uri, no service_name.
const valkeyCreds={host:'h',hosts:['h'],port:0,tls_port:6379,username:'u',password:'p',admin_password:'a',ca_certificate:'c',tls_versions:['TLSv1.3'],service_type:'valkey'};

t('a Valkey instance with ACL users gets the Redis-family screen with Valkey copy',async()=>{
 const s=await screen(valkeyCreds);
 try{
  expect(s.html).toContain('executeRedisTest');
  expect(s.html).not.toContain('executeRabbitMQTest');
  expect(s.html).toContain('data-operation="info"');
  expect(s.html).not.toContain('data-operation="rabbitmqctl"');
  expect(s.w.currentTestingInstance.serviceType).toBe('redis');
  expect(s.w.currentTestingInstance.label).toBe('Valkey');
 }finally{s.close();}
});

t('the operation headings and copy say Valkey for Valkey and Redis for Redis',async()=>{
 const s=await screen(valkeyCreds);
 try{
  const info=s.w.__T.renderRedisInfoOperation('i1');
  expect(info).toContain('Valkey INFO');
  expect(info).toContain('Get Valkey server information');
  expect(info).not.toContain('Redis INFO');
 }finally{s.close();}
 const r=await screen({host:'h',password:'p'},{service_name:'redis'});
 try{expect(r.w.__T.renderRedisInfoOperation('i1')).toContain('Redis INFO');}finally{r.close();}
});

t('a Valkey instance with no service name is found by its valkeys:// URI',async()=>{
 const s=await screen({host:'h',uri:'valkeys://u:p@h:6379',username:'u',password:'p'});
 try{
  expect(s.html).toContain('executeRedisTest');
  expect(s.w.currentTestingInstance.label).toBe('Valkey');
 }finally{s.close();}
});

t('a valkey:// URI is also Valkey',()=>{
 const b=boot();
 try{expect(b.w.__T.detectServiceFamily({uri:'valkey://h:6379',username:'u'},null)).toEqual({type:'redis',label:'Valkey'});}finally{b.w.close();}
});

t('a RabbitMQ instance still gets the RabbitMQ screen',async()=>{
 const named=await screen({host:'h',username:'u',password:'p',vhost:'v',service_type:'rabbitmq'});
 try{
  expect(named.html).toContain('executeRabbitMQTest');
  expect(named.html).not.toContain('executeRedisTest');
  expect(named.w.currentTestingInstance.serviceType).toBe('rabbitmq');
 }finally{named.close();}
 const guessed=await screen({host:'h',username:'u',password:'p',vhost:'v'});
 try{expect(guessed.html).toContain('executeRabbitMQTest');}finally{guessed.close();}
 const amqp=await screen({uri:'amqps://u:p@h:5671/v'});
 try{expect(amqp.html).toContain('executeRabbitMQTest');}finally{amqp.close();}
});

t('a Redis instance still gets the Redis screen',async()=>{
 const named=await screen({host:'h',password:'p'},{service_name:'redis'});
 try{
  expect(named.html).toContain('executeRedisTest');
  expect(named.w.currentTestingInstance.label).toBe('Redis');
 }finally{named.close();}
 const guessed=await screen({host:'h',password:'p',port:6379});
 try{expect(guessed.html).toContain('executeRedisTest');}finally{guessed.close();}
});

t('a named service the screen does not support gets the not-available message',async()=>{
 const s=await screen({host:'h',username:'u',password:'p',service_type:'postgresql'});
 try{
  expect(s.html).toContain('Service testing not available for this service type');
  expect(s.html).not.toContain('executeRabbitMQTest');
 }finally{s.close();}
});

t('a named service wins over username, port, and vhost guesses',()=>{
 const b=boot();
 try{
  const f=b.w.__T.detectServiceFamily;
  expect(f({username:'u',vhost:'v',service_type:'valkey'},null).type).toBe('redis');
  expect(f({username:'u',port:6379,service_type:'rabbitmq'},null).type).toBe('rabbitmq');
  expect(f({host:'h',password:'p',port:6379},{service_name:'mysql'}).type).toBeNull();
 }finally{b.w.close();}
});

t('the log selection treats Valkey like Redis',()=>{
 const b=boot();
 try{
  const pick=(files,name)=>b.w.__T.LogSelectionManager.getSmartDefault(files,name,'small',null,null);
  const files=['bpm/pre-start.stdout.log','standalone-3/standalone-3.stderr.log','standalone-3/standalone-3.stdout.log'];
  expect(pick(files,'valkey')).toBe('standalone-3/standalone-3.stdout.log');
  expect(pick(files,'redis')).toBe('standalone-3/standalone-3.stdout.log');
  const both=['standalone-3/standalone-3.stdout.log','other/valkey.stdout.log'];
  expect(pick(both,'valkey')).toBe('standalone-3/standalone-3.stdout.log');
 }finally{b.w.close();}
});

t('an instance name that matches no service falls through to the guesses',async()=>{
 const r=await screen({host:'h',password:'p',port:6379},{service_name:'my-cache'});
 try{
  expect(r.html).toContain('executeRedisTest');
  expect(r.w.currentTestingInstance.label).toBe('Redis');
 }finally{r.close();}
 const p=await screen({host:'h',password:'p',port:6379},{service_name:'postgresql'});
 try{expect(p.html).toContain('Service testing not available for this service type');}finally{p.close();}
 const v=await screen(valkeyCreds,{service_name:'my-cache'});
 try{expect(v.w.currentTestingInstance.label).toBe('Valkey');}finally{v.close();}
});

t('the details service_name cannot override what the credentials show',async()=>{
 const rmq=await screen({uri:'amqps://u:p@h:5671/v',host:'h',username:'u',password:'p',vhost:'v'},{service_name:'redis-notifier'});
 try{
  expect(rmq.html).toContain('executeRabbitMQTest');
  expect(rmq.html).not.toContain('executeRedisTest');
 }finally{rmq.close();}
 const kafka=await screen({host:'h',password:'p',port:6379},{service_name:'kafka-dedupe-cache'});
 try{expect(kafka.html).toContain('executeRedisTest');}finally{kafka.close();}
 const v=await screen(valkeyCreds,{service_name:'postgres'});
 try{expect(v.w.currentTestingInstance.label).toBe('Valkey');}finally{v.close();}
 const exact=await screen({host:'h',password:'p',port:6379},{service_name:'postgres'});
 try{expect(exact.html).toContain('Service testing not available for this service type');}finally{exact.close();}
});
