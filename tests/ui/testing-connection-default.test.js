const {test,expect}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

async function screen(creds,details={}){
 const b=boot();
 b.w.__routes.push(
  ['/credentials',(u,o,json)=>json(200,creds)],
  ['/details',(u,o,json)=>json(200,details)],
  ['/config',(u,o,json)=>json(404,{})]);
 try{
  const html=await b.w.__T.renderServiceTesting('i1');
  const doc=new b.w.DOMParser().parseFromString(html,'text/html');
  return {html,doc,w:b.w,close:()=>b.w.close()};
 }catch(e){b.w.close();throw e;}
}
const NOTE="Standard connections aren't available for this instance.";
const options=(doc)=>[...doc.querySelectorAll('#connection-type-i1 option')].map(o=>({value:o.value,selected:o.hasAttribute('selected'),disabled:o.disabled}));
const target=(doc)=>Object.fromEntries([...doc.querySelectorAll('#connection-target-i1 > div')].map(d=>[d.querySelector('dt').textContent,d.querySelector('dd').textContent.trim()]));

const valkeyTlsOnly={host:'h',hosts:['h'],port:0,tls_port:6379,username:'u',password:'p',ca_certificate:'c',service_type:'valkey'};
const rabbitTlsOnly={host:'h',port:0,tls_port:5671,username:'u',password:'p',vhost:'v',service_type:'rabbitmq'};

t('a Valkey instance with no standard port defaults to TLS and disables Standard',async()=>{
 const s=await screen(valkeyTlsOnly);
 try{
  expect(options(s.doc)).toEqual([{value:'standard',selected:false,disabled:true},{value:'tls',selected:true,disabled:false}]);
  expect(s.doc.querySelector('.connection-type-note').textContent).toBe(NOTE);
  expect(target(s.doc).Port).toBe('6379');
 }finally{s.close();}
});

t('a RabbitMQ instance with no AMQP port defaults to AMQPS and disables AMQP',async()=>{
 const s=await screen(rabbitTlsOnly);
 try{
  expect(options(s.doc)).toEqual([{value:'amqp',selected:false,disabled:true},{value:'amqps',selected:true,disabled:false}]);
  expect(s.doc.querySelector('.connection-type-note').textContent).toBe(NOTE);
  expect(target(s.doc).Port).toBe('5671');
 }finally{s.close();}
});

t('an instance with both ports keeps Standard as its default',async()=>{
 const v=await screen({...valkeyTlsOnly,port:6379});
 try{
  expect(options(v.doc)).toEqual([{value:'standard',selected:false,disabled:false},{value:'tls',selected:false,disabled:false}]);
  expect(v.doc.querySelector('.connection-type-note')).toBeNull();
  expect(target(v.doc).Port).toBe('6379');
 }finally{v.close();}
 const r=await screen({...rabbitTlsOnly,port:5672});
 try{
  expect(options(r.doc).map(o=>o.disabled)).toEqual([false,false]);
  expect(options(r.doc).some(o=>o.selected)).toBe(false);
  expect(r.doc.querySelector('.connection-type-note')).toBeNull();
 }finally{r.close();}
});

t('an instance with only a standard port keeps Standard selectable',async()=>{
 const s=await screen({host:'h',port:6379,password:'p',service_type:'redis'});
 try{
  expect(options(s.doc).map(o=>o.disabled)).toEqual([false,false]);
  expect(s.doc.querySelector('.connection-type-note')).toBeNull();
 }finally{s.close();}
});
