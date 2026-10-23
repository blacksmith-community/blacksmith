const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep} = H.main;
t('fleet export menu', async () => {
  const env=boot(); const {w,d,errors}=env; const T=w.__T;
  const mk=(i,svc)=>({id:'c'+i,key:'/s/a'+i,variable:'a'+i,kind:'leaf',days_left:3,expiry:'critical',health:'ok',owners:[{kind:'instance',instance_id:'i'+i,instance_name:'n'+i}],rotation:{allowed:true,reasons:[]},findings:[]});
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])],
   [/certificates\/inventory/,(u,o,j)=>j(200,{certificates:[mk(1),mk(2)],capabilities:{},settings:{warning_days:30,critical_days:7},scan:{state:'idle'},summary:{certificates:2,needs_attention:0},filtered:{certificates:2}})]);
  w.serviceInstances={}; await sleep(50); w.plansData={services:[]};
  d.getElementById('certificates').classList.add('active');
  await T.CertsFleet.init(); await sleep(200);
  const st=T.CertsFleet.state; st.expiry=['critical']; st.service=['valkey'];
  T.CertsFleet.view.refs.search.dispatchEvent(new w.Event('input'));
  const btn=d.getElementById('cert-export-btn'), menu=d.getElementById('cert-export-menu');
  // re-render links through a filter change
  const sel=T.CertsFleet.view.refs.service; 
  const q=T.CertsFleet.toQuery(st);
  btn.click(); await sleep(20);
  ok(!menu.hidden&&btn.getAttribute('aria-expanded')==='true','click opens menu');
  const csv=d.getElementById('cert-export-csv'), json=d.getElementById('cert-export-json');
  const mkEv=(k)=>new w.KeyboardEvent('keydown',{key:k,bubbles:true,cancelable:true});
  ok(d.activeElement===csv,'focus moves to first link');
  csv.dispatchEvent(mkEv('ArrowDown')); ok(d.activeElement===json,'ArrowDown moves');
  json.dispatchEvent(mkEv('ArrowDown')); ok(d.activeElement===csv,'wraps');
  let docEsc=false; d.addEventListener('keydown',()=>{docEsc=true});
  csv.dispatchEvent(mkEv('Escape')); await sleep(10);
  ok(menu.hidden&&btn.getAttribute('aria-expanded')==='false'&&d.activeElement===btn,'Escape closes and refocuses button');
  ok(!docEsc,'Escape does not reach other surfaces');
  btn.click(); await sleep(10); ok(!menu.hidden,'reopens');
  d.body.click(); ok(menu.hidden,'outside click closes');
  btn.click(); csv.click(); ok(menu.hidden&&d.activeElement===btn,'choosing a link closes, focus to button');
  { const s=T.CertsFleet.view.refs.service; const o=d.createElement("option"); o.value="valkey"; o.textContent="valkey"; s.appendChild(o); s.value="valkey"; s.dispatchEvent(new w.Event("change")); await sleep(300); }
  st.expiry=['critical']; st.service=['valkey'];
  const exp=T.CertsFleet.toQuery(st,{format:'csv'});
  ok(/expiry=critical/.test(exp)&&/service=valkey/.test(exp)&&/format=csv/.test(exp),'toQuery carries filters + format: '+exp);
  ok(/^\/b\/certificates\/inventory\?/.test(csv.getAttribute('href')),'csv href: '+csv.getAttribute('href'));
  ok(/format=json/.test(json.getAttribute('href')),'json href: '+json.getAttribute('href'));
  ok(csv.hasAttribute('download'),'download attr');
  ok(!errors.some(e=>/Reference|TypeError|LOAD/.test(e)),'no errors '+errors.join('|'));
  expect(failures).toEqual([]);
}, 120000);
