const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');
const { pinZone } = require('../fixed-zone.js');
pinZone('UTC');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,openInstanceTab,sleep,EVIL,day,now,iso} = H.instance;
const bad=(d)=>d.querySelectorAll('img,script,[onerror]').length;
const obs=(o)=>Object.assign({source:'probe',owner:{kind:'instance'},locator:'valkey/0:6379',state:'ok',matches:'deployed',observed_at:iso(now)},o);
function body(over){
  return Object.assign({
    instance:{id:'i1',name:'valkey-1'+EVIL,deployment:'d',service_name:'valkey',plan_name:'small'},
    scan:{scanned_at:iso(now),last_success_at:iso(now),stale:false},
    certificates:[
      {id:'c1',key:'/x/valkey_tls'+EVIL,variable:'valkey_tls'+EVIL,kind:'leaf',common_name:'cn'+EVIL,effective_not_after:iso(now+5*day),days_left:5,expiry:'critical',health:'warning',attention:true,
       versions:{deployed_version_id:'a',latest_version_id:'b',transitional:false},
       findings:[{code:'probe_failed',severity:'warning',message:'node/2 did not answer'+EVIL}],
       coverage:{expected_listeners:3,observed_listeners:2,failed_listeners:1,complete:false,stale:false},
       observations:[obs({locator:'valkey/0:6379'}),obs({locator:'valkey/1:6379'}),obs({locator:'valkey/2:6379'+EVIL,state:'failed',error:'timed out'+EVIL,matches:''}),
         obs({source:'manifest',locator:'instance_groups/valkey/jobs/valkey/properties/tls/cert'+EVIL,not_after:iso(now+5*day)})]},
      {id:'c2',key:'sha256:abc',kind:'ca',common_name:'Services CA'+EVIL,effective_not_after:iso(now+3000*day),days_left:3000,expiry:'ok',health:'ok',attention:false,
       coverage:{expected_listeners:0,observed_listeners:0,failed_listeners:0,complete:true,stale:false},
       observations:[obs({source:'manifest',locator:'options.ca'+EVIL,not_after:iso(now+3000*day)})]},
      {id:'c3',key:'k3',kind:'leaf',common_name:'noexpiry',expiry:'unknown',expiry_reason:'CredHub not configured'+EVIL,health:'unknown',attention:false,coverage:{complete:true},observations:[]}
    ],rotatable_variables:[],summary:{certificates:3},capabilities:{}},over||{});
}
t('instance certificates tab', async () => {
  // ---- normal ----
  let {w,d,errors,json}=boot({oat:true});
  w.__routes=[[/certificates\/instances\//,()=>json(200,body())]];
  const h=await openInstanceTab(w,d,'i1');
  const ov=d.getElementById('service-cert-overview-i1');
  ok(/Scanning this instance\.\.\./.test(ov.textContent),'loading text shown first');
  ok(ov.querySelector('.cert-ov-rescan').disabled,'Rescan disabled while scanning');
  await sleep(30);
  const tabs=[...h.querySelectorAll('#service-cert-tablist-i1 [role=tab]')].map(t=>t.textContent);
  ok(tabs.join(',')==='Overview,Manifest,Trusted,Endpoint','four sub-tabs in order: '+tabs.join(','));
  ok(h.querySelector('[data-tab=overview].active')&&h.querySelector('.manifest-tab-pane.active').dataset.tab==='overview','Overview is the default');
  const rows=ov.querySelectorAll('tbody tr'); ok(rows.length===3,'3 table rows: '+rows.length);
  ok(ov.querySelectorAll('.cert-pill').length===3 && ov.querySelectorAll('.health-pill').length===3,'expiry and health pills on both axes');
  ok(/Critical, 5 days/.test(ov.textContent)&&/Expiry OK/.test(ov.textContent)&&/Expiry unknown/.test(ov.textContent),'pill wording');
  ok(/Health warning/.test(ov.textContent),'health wording');
  const lis=ov.querySelectorAll('.cert-ov-listeners');
  ok(lis.length===1 && lis[0].querySelectorAll('li').length===3 && /valkey\/0:6379/.test(lis[0].textContent)&&/6379/.test(lis[0].textContent)&&/Listeners/.test(lis[0].textContent),'Listeners line names 3 VMs on 6379');
  ok(/failed: timed out/.test(lis[0].textContent),'failed listener shows error');
  ok(/Scanned \d{4}-\d\d-\d\d \d\d:\d\d:\d\d UTC, 2 of 3 listeners observed/.test(ov.querySelector('.cert-ov-footer').textContent),'scan time and coverage: '+ov.querySelector('.cert-ov-footer').textContent);
  const rot=ov.querySelector('.cert-ov-rotate'); ok(rot&&rot.disabled&&rot.textContent==='Rotate...'&&d.getElementById(rot.getAttribute('aria-describedby')).textContent.length>0,'Rotate... disabled with visible reason');
  ok(/Newer version not deployed yet/.test(ov.textContent)&&ov.querySelector('td .empty-value')!==null,'deployed vs latest column');
  ok(bad(d)===0,'no injected elements');
  ok(errors.filter(e=>!/Error loading data/.test(e)).length===0,'no console errors beyond page-load 404 '+errors.join('|'));
  // arrow keys
  const t0=h.querySelector('[data-tab=overview]'); t0.focus();
  const key=(el,k)=>el.dispatchEvent(new w.KeyboardEvent('keydown',{key:k,bubbles:true,cancelable:true}));
  const seq=[]; for(let i=0;i<4;i++){ key(d.activeElement,'ArrowRight'); seq.push(d.activeElement.textContent);} 
  ok(seq.join(',')==='Manifest,Trusted,Endpoint,Overview','Right moves through four tabs and wraps: '+seq);
  key(d.activeElement,'ArrowLeft'); ok(d.activeElement.textContent==='Endpoint','Left wraps back');
  // rescan
  let count=0; w.__routes=[[/certificates\/instances\//,()=>{count++;return json(200,body())}]];
  ov.querySelector('.cert-ov-rescan').click(); await sleep(30); ok(count===1,'Rescan refetches');
  // ---- manifest sub-tab ----
  d.querySelector('[data-cert-action=fetch-manifest]').click(); await sleep(30);
  const ml=d.getElementById('manifest-certificates-list-i1');
  const items=ml.querySelectorAll('.cert-list-item'); ok(items.length===2,'manifest sub-tab lists manifest observations: '+items.length);
  ok(/Services CA/.test(ml.textContent)&&/options\.ca/.test(ml.textContent),'manifest shows services CA with its manifest path');
  ok(!/valkey\/0:6379/.test(ml.textContent),'manifest list excludes probe observations');
  ok(bad(d)===0,'no injected in manifest list');
  // ---- hostile everything ----
  // ---- 403 ----
  w.__routes=[[/certificates\/instances\//,()=>json(403,{success:false,error:'Certificate inventory is off on this broker (certificates.inventory.enabled).',code:'inventory_disabled'})]];
  ov.querySelector('.cert-ov-rescan').click(); await sleep(30);
  ok(/inventory is off/.test(ov.textContent)&&ov.querySelector('.cert-retry-button'),'403 shows message and Retry');
  d.querySelector('[data-cert-action=fetch-manifest]').click(); await sleep(30);
  ok(/inventory is off/.test(ml.textContent)&&ml.querySelector('.cert-retry-button'),'403 on manifest shows message and Retry');
  // ---- 404 / 500 / network ----
  w.__routes=[[/certificates\/instances\//,()=>json(404,{success:false,error:'unknown'+EVIL,code:'instance_not_found'})]];
  ov.querySelector('.cert-retry-button').click(); await sleep(30);
  ok(/unknown/.test(ov.textContent)&&bad(d)===0,'404 error text-only');
  w.__routes=[[/certificates\/instances\//,()=>Promise.reject(new Error('net'+EVIL))]];
  ov.querySelector('.cert-retry-button').click(); await sleep(30);
  ok(/could not be reached/.test(ov.textContent)&&bad(d)===0,'network failure handled');
  // ---- first-time failure ----
  w.__routes=[[/certificates\/instances\//,()=>json(200,body({certificates:[],scan:{scanned_at:iso(now),stale:false,error:'director unreachable'+EVIL}}))]];
  ov.querySelector('.cert-retry-button').click(); await sleep(30);
  ok(ov.querySelectorAll('table').length===0&&/director unreachable/.test(ov.textContent)&&ov.querySelector('.cert-retry-button'),'first-time failure: only error and Retry');
  // ---- stale failure with last observations ----
  w.__routes=[[/certificates\/instances\//,()=>json(200,body({scan:{scanned_at:iso(now),last_success_at:iso(now-3600e3),stale:true,error:'scan timed out after 60s'}}))]];
  ov.querySelector('.cert-retry-button').click(); await sleep(30);
  ok(ov.querySelectorAll('table').length===1&&ov.querySelectorAll('.cert-ov-row--stale').length===3&&/marked stale/.test(ov.textContent)&&/timed out/.test(ov.textContent)&&ov.querySelectorAll('.cert-notice .cert-action-btn').length===1,'stale: observations marked stale with time, error, Retry');
  // ---- empty ----
  w.__routes=[[/certificates\/instances\//,()=>json(200,body({certificates:[],scan:{scanned_at:iso(now)}}))]];
  ov.querySelector('.cert-ov-rescan').click(); await sleep(30);
  ok(/no certificates in the inventory/.test(ov.textContent),'empty state');
  // zero-time scan
  w.__routes=[[/certificates\/instances\//,()=>json(200,body({scan:{scanned_at:'0001-01-01T00:00:00Z'}}))]];
  ov.querySelector('.cert-ov-rescan').click(); await sleep(30);
  ok(!/Scanned 0001/.test(ov.textContent),'zero time not shown');
  // malformed
  w.__routes=[[/certificates\/instances\//,()=>json(200,{certificates:null,scan:null,instance:null})]];
  ov.querySelector('.cert-ov-rescan').click(); await sleep(30);
  ok(!!ov.textContent,'null fields do not throw');
  // stale response after instance switch: detach container then resolve
  let resolve; w.__routes=[[/certificates\/instances\//,()=>new Promise(r=>{resolve=()=>r(json(200,body()))})]];
  ov.querySelector('.cert-ov-rescan').click(); await sleep(5); h.remove(); resolve(); await sleep(20);
  ok(true,'detached container: no throw');
  ok(errors.filter(e=>!/Error loading data/.test(e)).length===0,'no unexpected console errors: '+errors.join('|').slice(0,200));
  expect(failures).toEqual([]);
}, 120000);
