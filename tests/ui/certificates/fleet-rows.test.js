const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL,now,iso,day} = H.fleet;
const bad=(d)=>d.querySelectorAll('#certificates img,#certificates script,#certificates [onerror],[data-evil]').length;
const base=(id,o)=>Object.assign({id,key:'/ws/'+id,variable:id+EVIL,kind:'leaf',common_name:'cn-'+id+EVIL,issuer:'CN=X',owners:[{kind:'instance',instance_id:'i1',instance_name:'one'+EVIL,service_name:'valkey',plan_name:'s'}],affected_instances:1,effective_not_after:iso(now+400*day),days_left:400,expiry:'ok',health:'ok',attention:false,findings:[],observations:[],coverage:{expected_listeners:1,observed_listeners:1,failed_listeners:0,complete:true},rotation:{allowed:true},versions:{}},o||{});
const fix=()=>[
  base('A',{attention:true}),
  base('B',{health:'error'}),
  base('C',{findings:[{code:'unconverged'}],finding_codes:['unconverged']}),
  base('D',{attention:true,health:'error'}),
  base('E'),
  base('M',{attention:true,owners:[{kind:'instance',instance_id:'i1',instance_name:'one'+EVIL},{kind:'instance',instance_id:'i2',instance_name:'two'+EVIL}],affected_instances:2})
];
const hasFinding=(c,code)=>(c.findings||[]).some(f=>f.code===code);
function wrap(w,server){
  const orig=w.fetch;
  w.fetch=async(u,o={})=>{
    u=String(u); if(!u.startsWith('/b/certificates/inventory')) return orig(u,o);
    const p=new URL(u,'http://x').searchParams;
    const res=await orig(u,o); const body=await res.json();
    if(Array.isArray(body.certificates)&&body.certificates.length>=0){
      let c=body.certificates;
      if(p.get('attention')==='true') c=c.filter(x=>x.attention);
      const h=p.getAll('health'); if(h.length) c=c.filter(x=>h.includes(x.health));
      const f=p.getAll('finding'); if(f.length) c=c.filter(x=>f.some(code=>hasFinding(x,code)));
      body.certificates=c; if(body.filtered) body.filtered.certificates=c.length;
    }
    return {ok:res.ok,status:res.status,json:()=>Promise.resolve(body),text:()=>Promise.resolve(JSON.stringify(body))};
  };
}
const btn=(d,label)=>[...d.querySelectorAll('#cert-strips button')].find(b=>b.textContent.startsWith(label));
const rowsIds=(d)=>[...d.querySelectorAll('#cert-tbody tr.cert-row')].map(r=>r.dataset.certId);
const invCalls=(log,from)=>log.slice(from).filter(l=>/^GET \/b\/certificates\/inventory\?/.test(l)&&!/summary_only/.test(l));
t('fleet rows and grouping', async () => {
  let {w,d,server,log}=await boot();
  server.certs=fix(); wrap(w,server);
  await w.blacksmithNav.switchTab('certificates'); await sleep(200);
  ok(btn(d,'Needs attention').getAttribute('aria-pressed')==='true','default attention pressed');
  ok(rowsIds(d).sort().join()==='A,D,M','one toggle: server result A,D,M');
  let m=log.length; btn(d,'Health errors').click(); await sleep(250);
  let calls=invCalls(log,m); 
  const filteredCalls=calls.filter(c=>/attention|health/.test(c));
  ok(filteredCalls.length===2,'two requests for two toggles');
  ok(filteredCalls.some(c=>/attention=true/.test(c)&&!/health=/.test(c))&&filteredCalls.some(c=>/health=error/.test(c)&&!/attention=/.test(c)),'one param per request');
  ok(rowsIds(d).sort().join()==='A,B,D,M','union A,B,D,M without duplicate D');
  ok(/4 shown\./.test(d.getElementById('cert-fleet-main').textContent),'count from union: 4 shown');
  ok(d.getElementById('blacksmith-live-region')&&/4 certificates shown/.test(d.getElementById('blacksmith-live-region').textContent),'announcement from union: '+(d.getElementById('blacksmith-live-region')||{}).textContent);
  // merged rows follow ascending effective_not_after, then id
  {
    const bb=await boot(); const t=[
      base('A',{attention:true,effective_not_after:iso(now+30*day)}),
      base('D',{attention:true,health:'error',effective_not_after:iso(now+10*day)}),
      base('M',{attention:true,effective_not_after:iso(now+50*day)}),
      base('B',{health:'error',effective_not_after:iso(now+20*day)}),
      base('Z',{health:'error',effective_not_after:iso(now+30*day)})];
    bb.server.certs=t; wrap(bb.w,bb.server);
    await bb.w.blacksmithNav.switchTab('certificates'); await sleep(200);
    btn(bb.d,'Health errors').click(); await sleep(250);
    ok(rowsIds(bb.d).join()==='D,B,A,Z,M','merged rows sorted by expiry then id: '+rowsIds(bb.d).join());
  }
  // export blocked
  const csv=d.getElementById('cert-export-csv'), js=d.getElementById('cert-export-json'), note=d.getElementById('cert-export-note');
  ok(!csv.hasAttribute('href')&&!js.hasAttribute('href')&&csv.getAttribute('aria-disabled')==='true'&&!note.hidden&&/unavailable/.test(note.textContent),'export disabled with visible reason');
  m=log.length; btn(d,'Unconverged').click(); await sleep(250);
  calls=invCalls(log,m).filter(c=>/attention|health|finding/.test(c));
  ok(calls.length===3,'three requests for three toggles');
  ok(rowsIds(d).sort().join()==='A,B,C,D,M','union of three: A,B,C,D,M');
  ok(!csv.hasAttribute('href'),'export still disabled');
  // other filter held constant
  m=log.length; d.getElementById('cert-search').value='cn'; d.getElementById('cert-search').dispatchEvent(new w.Event('input',{bubbles:true})); await sleep(600);
  calls=invCalls(log,m).filter(c=>/attention|health|finding/.test(c));
  ok(calls.length>=3&&calls.every(c=>/q=cn/.test(c)),'search constant across variants ('+calls.length+')');
  // back to one toggle
  btn(d,'Health errors').click(); await sleep(250); btn(d,'Unconverged').click(); await sleep(250);
  m=log.length; btn(d,'Needs attention').click(); await sleep(10); btn(d,'Health errors').click(); await sleep(250);
  ok(!!csv.getAttribute('href')&&csv.getAttribute('href').includes('format=csv')&&!csv.hasAttribute('aria-disabled')&&note.hidden,'export enabled with one toggle');
  // zero toggles
  btn(d,'Health errors').click(); await sleep(250);
  ok(!!js.getAttribute('href'),'export enabled with zero toggles');
  // ---- owner groups ----
  d.getElementById('cert-search').value=''; d.getElementById('cert-search').dispatchEvent(new w.Event('input',{bubbles:true})); await sleep(600);
  const gb=[...d.querySelectorAll('#cert-groupby button, #cert-groupby [role=radio], #cert-groupby input')].find(x=>/owner/i.test(x.textContent||x.value||x.getAttribute('aria-label')||''));
  gb.click(); await sleep(100);
  const groups=[...d.querySelectorAll('tr.cert-group-row')];
  const label=(g)=>g.querySelector('.cert-group-name').textContent;
  ok(groups.length===2,'two owner groups');
  const rowsUnder=(g)=>{const out=[];let n=g.nextElementSibling;while(n&&!n.classList.contains('cert-group-row')){if(n.dataset.certId)out.push(n.dataset.certId);n=n.nextElementSibling;}return out;};
  const g1=groups.find(g=>g.dataset.groupId==='owner:instance:i1'), g2=groups.find(g=>g.dataset.groupId==='owner:instance:i2');
  ok(rowsUnder(g1).includes('M')&&rowsUnder(g2).join()==='M','M appears in both owner groups');
  const sel=w.blacksmithCerts.selection; sel.clear();
  g2.querySelector('input').click(); await sleep(50);
  ok(sel.size===1&&sel.has('i2')&&sel.get('i2').has('/ws/M')&&!sel.has('i1'),'group i2 checkbox selects only i2 pair: '+JSON.stringify([...sel].map(([k,v])=>[k,[...v]])));
  sel.clear(); await sleep(10);
  d.querySelector('tr.cert-group-row[data-group-id="owner:instance:i1"] input').click(); await sleep(50);
  ok(sel.has('i1')&&!sel.has('i2'),'group i1 checkbox selects only i1');
  ok(bad(d)===0,'no injected elements (groups, hostile owner names)');
  // ---- reassurance ----
  const calm=(withObs,coverageSum)=>[1,2,3].map(i=>base('R'+i,{observations:withObs?[{source:'probe',locator:'10.0.0.9:6379',state:'failed',owner:{kind:'instance'}},{source:'probe',locator:'10.0.0.1:1',state:'ok'},{source:'credhub',locator:'10.0.0.9:6379',state:'failed'}]:[],coverage:{expected_listeners:2,observed_listeners:1,failed_listeners:1,complete:false}}));
  for(const [withObs,expect] of [[true,/1 listener did not answer/],[false,/3 listeners did not answer/]]){
    const b=await boot(); b.server.certs=calm(withObs); wrap(b.w,b.server);
    await b.w.blacksmithNav.switchTab('certificates'); await sleep(200);
    const r=b.d.getElementById('cert-reassurance');
    ok(r&&expect.test(r.textContent),(withObs?'3 certs one locator':'no observations fallback')+': '+(r&&r.textContent));
  }
  expect(failures).toEqual([]);
}, 120000);
