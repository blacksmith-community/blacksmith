const {test,expect}=require('bun:test');
const fs=require('fs'),path=require('path');
const {main:{boot,sleep,now,day,iso}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const t=JSDOM?test:test.skip;

const cert=(i,attention)=>({id:'c'+i,key:'k'+i,kind:'leaf',common_name:'cn'+i,health:'ok',attention,coverage:{complete:true},
 effective_not_after:iso(now+400*day),days_left:400,expiry:'ok',owners:[{kind:'instance',instance_id:'i'+(i%2),instance_name:'inst'+(i%2)}],observations:[],findings:[]});
const certs=[cert(0,true),cert(1,false),cert(2,true),cert(3,false),cert(4,false)];
const summarize=(c)=>({certificates:c.length,needs_attention:c.filter(x=>x.attention).length,expired:0,critical:0,warning:0,expiry_ok:c.length,expiry_unknown:0,health_errors:0,unconverged:0,stale:0,incomplete_coverage:0});
const inventory=(u,o,json)=>{
 const params=new URL(u,'http://x').searchParams;
 const rows=params.get('attention')==='true'?certs.filter(c=>c.attention):certs;
 return json(200,{generated_at:iso(now),summary:summarize(certs),filtered:summarize(rows),scan:{state:'idle'},capabilities:{},settings:{warning_days:30,critical_days:7},certificates:params.get('summary_only')==='true'?[]:rows});
};
const start=async()=>{
 const env=boot({noDialogs:true,routes:[['/b/certificates/inventory',inventory],['/v2/catalog',(u,o,j)=>j(200,{services:[]})],['/b/status',(u,o,j)=>j(200,{env:'lab',deployment:'blacksmith',instances:{},plans:{}})]]});
 await sleep(80);
 return env;
};
const chip=(d)=>[...d.querySelectorAll('#cert-strip-attention .cert-strip-btn')].find(b=>b.textContent.includes('Needs attention'));
const rows=(d)=>d.querySelectorAll('#cert-tbody tr.cert-row').length;

t('the router parses and writes the attention parameter',()=>{
 const w=watchUncaught(new JSDOM('<body></body>',{url:'http://localhost/',runScripts:'outside-only'}).window);
 w.eval(fs.readFileSync(path.join(__dirname,'../../ui/routes.js'),'utf8'));
 const api=w.BlacksmithRoutes;
 expect(api.parse('#/certificates')).toEqual({screen:'certificates'});
 expect(api.parse('#/certificates?attention=true')).toEqual({screen:'certificates',attention:true});
 expect(api.parse('#/certificates?attention=false')).toEqual({screen:'certificates',attention:false});
 expect(api.parse('#/certificates?attention=maybe')).toEqual({screen:'certificates'});
 expect(api.parse('#/instances?attention=true')).toEqual({screen:'services'});
 expect(api.format({screen:'certificates'})).toBe('#/certificates');
 expect(api.format({screen:'certificates',attention:true})).toBe('#/certificates?attention=true');
 expect(api.format({screen:'certificates',attention:false})).toBe('#/certificates?attention=false');
 expect(api.format({screen:'certificates',attention:true,inspection:{kind:'certificate',id:'c0'}})).toBe('#/certificates?attention=true&inspect=certificate&id=c0');
 expect(api.parse('#/certificates?attention=true&inspect=certificate&id=c0')).toEqual({screen:'certificates',attention:true,inspection:{kind:'certificate',id:'c0'}});
 w.close();
});

t('entering by the attention URL filters the rows and the chip agrees, and toggling follows',async()=>{
 const {w,d}=await start();try{
  let notified=0;
  const notify=w.blacksmithViews.notify;
  w.blacksmithViews.notify=(...a)=>{notified++;return notify(...a);};
  await w.blacksmithViews.activate({screen:'certificates',attention:true},{});
  expect(chip(d).getAttribute('aria-pressed')).toBe('true');
  expect(rows(d)).toBe(2);
  expect(w.blacksmithViews.read('certificates')).toEqual({screen:'certificates',attention:true});
  expect(notified).toBe(0);

  chip(d).click();await sleep(80);
  expect(chip(d).getAttribute('aria-pressed')).toBe('false');
  expect(rows(d)).toBe(5);
  expect(w.blacksmithViews.read('certificates')).toEqual({screen:'certificates',attention:false});
  expect(notified).toBe(1);

  chip(d).click();await sleep(80);
  expect(rows(d)).toBe(2);
  expect(w.blacksmithViews.read('certificates')).toEqual({screen:'certificates',attention:true});
  expect(notified).toBe(2);

  await w.blacksmithViews.activate({screen:'certificates',attention:false},{});
  expect(chip(d).getAttribute('aria-pressed')).toBe('false');
  expect(rows(d)).toBe(5);
  expect(notified).toBe(2);
 }finally{w.close();}
});

t('a bare certificates URL keeps the chip the operator left',async()=>{
 const {w,d}=await start();try{
  await w.blacksmithViews.activate({screen:'certificates',attention:false},{});
  expect(rows(d)).toBe(5);
  await w.blacksmithViews.activate({screen:'certificates'},{});
  expect(chip(d).getAttribute('aria-pressed')).toBe('false');
  expect(rows(d)).toBe(5);
 }finally{w.close();}
});

t('the first bare visit applies the default and leaves the address alone',async()=>{
 const {w,d}=await start();try{
  await w.blacksmithViews.activate({screen:'certificates'},{});
  expect(chip(d).getAttribute('aria-pressed')).toBe('true');
  expect(rows(d)).toBe(2);
  expect(w.blacksmithViews.read('certificates')).toEqual({screen:'certificates'});
 }finally{w.close();}
});

t('the summary link opens the attention list with the other filters cleared',async()=>{
 const {w,d}=await start();try{
  w.blacksmithCerts.state.q='cn1';
  w.blacksmithCerts.state.kind=['leaf'];
  const link=d.createElement('a');link.href='#/certificates?attention=true';link.setAttribute('data-attention-link','');d.body.appendChild(link);
  link.dispatchEvent(new w.MouseEvent('click',{bubbles:true,cancelable:true}));
  await w.blacksmithViews.activate({screen:'certificates',attention:true},{});
  expect(w.blacksmithCerts.state.q).toBe('');
  expect(w.blacksmithCerts.state.kind).toEqual([]);
  expect(rows(d)).toBe(2);
  // An ordinary route to the same address keeps the filters it finds.
  w.blacksmithCerts.state.q='cn0';
  await w.blacksmithViews.activate({screen:'certificates',attention:true},{});
  expect(w.blacksmithCerts.state.q).toBe('cn0');
 }finally{w.close();}
});

t('a navigation that is dropped puts the previous chip back',async()=>{
 const {w,d}=await start();try{
  await w.blacksmithViews.activate({screen:'certificates',attention:false},{});
  const controller=new w.AbortController();controller.abort();
  const result=await w.blacksmithViews.activate({screen:'certificates',attention:true},{signal:controller.signal});
  expect(result).toBeNull();
  expect(w.blacksmithCerts.state.attention).toBe(false);
  expect(w.blacksmithViews.read('certificates')).toEqual({screen:'certificates',attention:false});
 }finally{w.close();}
});

const startRouted=async(hash)=>{
 const env=boot({noDialogs:true,router:true,url:'http://localhost/'+hash,routes:[['/b/certificates/inventory',inventory],['/v2/catalog',(u,o,j)=>j(200,{services:[]})],['/b/status',(u,o,j)=>j(200,{env:'lab',deployment:'blacksmith',instances:{},plans:{}})]]});
 await sleep(200);
 return env;
};

t('with the router running, a chip toggle rewrites the address in place and Back and Forward follow',async()=>{
 const {w,d}=await startRouted('#/certificates?attention=true');try{
  expect(w.location.hash).toBe('#/certificates?attention=true');
  expect(rows(d)).toBe(2);
  const before=w.history.length;
  chip(d).click();await sleep(150);
  expect(chip(d).getAttribute('aria-pressed')).toBe('false');
  expect(rows(d)).toBe(5);
  expect(w.location.hash).toBe('#/certificates?attention=false');
  expect(w.history.length).toBe(before);
  w.location.hash='#/broker/summary';await sleep(250);
  w.history.back();await sleep(300);
  expect(w.location.hash).toBe('#/certificates?attention=false');
  expect(rows(d)).toBe(5);
  w.history.forward();await sleep(600);
  expect(w.location.hash).toBe('#/broker/summary');
 }finally{w.close();}
});
