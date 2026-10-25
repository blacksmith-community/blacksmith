const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL,day,now,iso} = H.instance;
const bad=(d)=>d.querySelectorAll('img,script,[onerror]').length;
const probeObs=(name,extra)=>Object.assign({source:'probe',owner:{kind:'platform',instance_name:name},locator:name+':443',state:'ok',observed_at:iso(now)},extra);
function certs(){
  const base={kind:'leaf',health:'ok',attention:false,coverage:{complete:true},effective_not_after:iso(now+400*day),days_left:400,expiry:'ok'};
  return [
    Object.assign({},base,{id:'b1',key:'sha256:1',common_name:'blacksmith.services'+EVIL,expiry:'warning',days_left:21,health:'ok',owners:[{kind:'broker'}],observations:[{source:'broker',owner:{kind:'broker'},locator:'config:broker.tls.certificate',state:'ok'}]}),
    Object.assign({},base,{id:'b2',key:'sha256:2',kind:'ca',common_name:'ws-blacksmith-ca'+EVIL,health:'error',days_left:3000,owners:[{kind:'broker'}],
      findings:[{code:'services_ca_drift',severity:'error',message:'The broker file and CredHub hold different CAs'+EVIL}],
      observations:[{source:'broker',owner:{kind:'broker'},locator:'/var/vcap/jobs/blacksmith/config/tls/blacksmith_services_ca',state:'ok'},{source:'credhub',owner:{kind:'broker'},locator:'/d/x/blacksmith_services_ca',state:'ok'},{source:'manifest',owner:{kind:'instance'},locator:'options.ca',state:'ok'}]}),
    Object.assign({},base,{id:'b3',key:'sha256:3',common_name:'10.0.0.6'+EVIL,owners:[{kind:'platform',instance_name:'director'}],observations:[probeObs('director')]}),
    Object.assign({},base,{id:'b4',key:'sha256:4',common_name:'uaa',owners:[{kind:'platform',instance_name:'uaa'}],observations:[probeObs('uaa')]}),
    Object.assign({},base,{id:'b5',key:'sha256:5',common_name:'credhub',health:'error',owners:[{kind:'platform',instance_name:'credhub'}],observations:[probeObs('credhub',{state:'failed',error:'pinned CA does not verify'+EVIL})],findings:[{code:'pinned_ca_mismatch',severity:'error',message:'pinned CA does not verify'}]}),
    Object.assign({},base,{id:'b6',key:'sha256:6',expiry:'unknown',expiry_reason:'x'+EVIL,effective_not_after:undefined,days_left:undefined,common_name:'',owners:[{kind:'broker'}],observations:[]})
  ];
}
const inv=(c,over)=>Object.assign({generated_at:iso(now),summary:{certificates:c.length},scan:{state:'idle'},capabilities:{},certificates:c},over||{});
t('broker certificate overview', async () => {
  let {w,d,errors,calls,json}=boot();
  w.blacksmithData={env:'ws'};
  w.__routes=[[/certificates\/inventory\?owner=broker&owner=platform/,()=>json(200,inv(certs()))]];
  const host=d.createElement('div'); d.body.appendChild(host); host.innerHTML=w.__T.renderCertificatesTab();
  w.__T.initializeCertificatesTab();
  const ov=d.getElementById('cert-broker-overview');
  ok(/Reading the broker/.test(ov.textContent),'loading shown');
  await sleep(150);
  expect(ov.querySelector('.cert-ov-rescan').getAttribute('aria-label')).toBe('Rescan broker and platform certificates');
  expect(!!ov.querySelector('.cert-ov-rescan svg[aria-hidden="true"]')).toBe(true);
  const tabs=[...host.querySelectorAll('#cert-main-tablist [role=tab]')].map(t=>t.textContent);
  ok(tabs.join(',')==='Overview,Trusted,Config,Endpoint','sub-tabs: '+tabs);
  ok(host.querySelector('.manifest-tab-pane.active').dataset.tab==='overview','Overview default');
  ok(calls.some(c=>c==='GET /b/certificates/inventory?owner=broker&owner=platform'),'inventory request uses owner filters');
  const labels=[...ov.querySelectorAll('.cert-ov-label')].map(x=>x.textContent);
  ok(labels.includes('Broker HTTPS listener'),'broker HTTPS listener row');
  ok(labels.includes('Services CA (3 sources)'),'services CA with source count');
  ok(['BOSH director (served)','UAA (served)','CredHub (served)'].every(l=>labels.includes(l)),'director, UAA, CredHub rows');
  ok(ov.querySelectorAll('tbody tr').length===6&&ov.querySelectorAll('.cert-pill').length===6&&ov.querySelectorAll('.health-pill').length===6,'six rows, both axes');
  ok(/Warning, 21 days/.test(ov.textContent),'warning pill');
  const drift=ov.querySelector('.cert-notice--error'); ok(drift&&drift.getAttribute('role')==='alert'&&/different CAs/.test(drift.textContent),'drift error card');
  ok(drift.compareDocumentPosition(ov.querySelector('.cert-ov-runbooks'))&4,'drift card is above the runbooks');
  const rbs=ov.querySelectorAll('details.cert-ov-runbook'); ok(rbs.length===3,'three <details> runbooks');
  ok(rbs[0].querySelector('summary').textContent==='Runbook: renew the broker HTTPS certificate','runbook 1 title');
  ok(/genesis ws rotate-secrets broker\/server/.test(rbs[0].textContent)&&/genesis ws deploy/.test(rbs[0].textContent),'runbook 1 commands with env ws');
  ok(/genesis ws rotate-secrets broker\/ca/.test(rbs[1].textContent)&&/rekey/i.test(rbs[1].textContent)&&/external procedure/.test(rbs[1].textContent),'runbook 2 renewal and rekey');
  const t3=rbs[2].textContent;
  ok(/blacksmith_credhub/.test(t3)&&/Redeploy Blacksmith/.test(t3)&&/certificates\.rotation\.enabled/.test(t3),'runbook 3 steps');
  ok(t3.indexOf('blacksmith_credhub')<t3.indexOf('Redeploy Blacksmith')&&t3.indexOf('Redeploy Blacksmith')<t3.indexOf('certificates.rotation.enabled'),'runbook 3 order');
  const a=rbs[2].querySelector('a'); ok(a&&/blacksmith-genesis-kit/.test(a.href)&&a.rel.includes('noopener'),'kit manual link');
  const copies=ov.querySelectorAll('.cert-copy-btn'); ok(copies.length===4,'copy buttons: '+copies.length);
  copies[0].click(); await sleep(10); ok(w.__copied[0]==='genesis ws rotate-secrets broker/server','copy puts ws commands on clipboard: '+JSON.stringify(w.__copied[0]));
  ok(!/<env>/.test(rbs[0].textContent),'no placeholder when env known');
  ok(bad(d)===0,'no injected elements');
  // What to do
  ov.querySelector('.cert-ov-drift-help').click(); ok(rbs[1].open,'What to do opens the CA runbook');
  // config pills
  w.__routes.unshift(['/b/internal/certificates/blacksmith',()=>json(200,{success:true,data:{certificates:[
    {name:'Broker TLS Certificate',details:{subject:{commonName:'b'+EVIL},issuer:EVIL,notAfter:iso(now+21*day),fingerprints:{sha256:'ab'}}},
    {name:'BOSH CA Certificate',path:'config:bosh.cacert',details:{subject:{commonName:'ca'},issuer:'ca',notAfter:iso(now+900*day)}},
    {name:'Vault CA Certificate',details:{subject:{commonName:'v'},issuer:'v',notAfter:iso(now-2*day)}}]}})]);
  await w.fetchBlacksmithCertificates();
  const cl=d.getElementById('blacksmith-certificates-list');
  const bosh=[...cl.querySelectorAll('.cert-list-item')].find(x=>/BOSH CA Certificate/.test(x.textContent));
  ok(!!bosh&&bosh.querySelector('.cert-pill')&&/Expiry OK/.test(bosh.textContent),'Config lists BOSH CA Certificate with an expiry pill');
  ok(cl.querySelectorAll('.cert-pill').length===3&&/Warning, 20 days|Warning, 21 days/.test(cl.textContent)&&/Expired/.test(cl.textContent),'every config entry has a pill');
  ok(bad(d)===0,'config list safe');
  // env missing
  ({w,d,errors,calls,json}=boot());
  w.__routes=[[/certificates\/inventory\?owner/,()=>json(200,inv(certs()))]];
  let h2=d.createElement('div'); d.body.appendChild(h2); h2.innerHTML=w.__T.renderCertificatesTab(); w.__T.initializeCertificatesTab(); await sleep(150);
  let o2=d.getElementById('cert-broker-overview');
  ok(/genesis <env> rotate-secrets broker\/server/.test(o2.textContent)&&/check the command form against our Genesis version/.test(o2.textContent),'env missing: placeholder and version note');
  w.blacksmithData={env:'Unknown'}; o2.replaceChildren(); await w.__T.CertOverview.mountBroker(o2);
  ok(/<env>/.test(o2.textContent),'env Unknown treated as missing');
  w.blacksmithData={env:EVIL}; await w.__T.CertOverview.mountBroker(o2);
  ok(bad(d)===0&&o2.querySelectorAll('.cert-runbook-pre')[0].textContent==='genesis <env> rotate-secrets broker/server','hostile env falls back to <env>');
  // 403
  w.__routes=[[/certificates\/inventory/,()=>json(403,{success:false,error:'Certificate inventory is off on this broker (certificates.inventory.enabled).',code:'inventory_disabled'})]];
  await w.__T.CertOverview.mountBroker(o2);
  ok(/inventory is off/.test(o2.textContent)&&o2.querySelector('.cert-retry-button')&&o2.querySelectorAll('details').length===3,'403: message, Retry, runbooks still shown');
  // 500
  w.__routes=[[/certificates\/inventory/,()=>json(500,{success:false,error:'boom'+EVIL,code:'internal'})]];
  o2.querySelector('.cert-retry-button').click(); await sleep(30);
  ok(/boom/.test(o2.textContent)&&bad(d)===0,'500 text-only');
  // empty / running
  w.__routes=[[/certificates\/inventory/,()=>json(200,inv([],{scan:{state:'running'}}))]];
  await w.__T.CertOverview.mountBroker(o2); ok(/first scan is still running/.test(o2.textContent),'empty while first scan runs');
  w.__routes=[[/certificates\/inventory/,()=>json(200,inv([]))]];
  await w.__T.CertOverview.mountBroker(o2); ok(/No broker or platform certificates/.test(o2.textContent)&&!o2.querySelector('.cert-notice--error'),'empty state, no drift card');
  // degraded: no credhub: unknown expiry rows
  w.__routes=[[/certificates\/inventory/,()=>json(200,inv(certs().map(c=>Object.assign({},c,{findings:[]}))))]];
  await w.__T.CertOverview.mountBroker(o2); ok(o2.querySelectorAll('tbody tr').length===6&&/Expiry unknown/.test(o2.textContent)&&!o2.querySelector('.cert-notice--error'),'degraded: unknown row renders');
  // null body fields
  w.__routes=[[/certificates\/inventory/,()=>json(200,{certificates:[{id:'z',observations:null,findings:null}],scan:null})]];
  await w.__T.CertOverview.mountBroker(o2); ok(o2.querySelectorAll('tbody tr').length===1,'sparse certificate does not throw');
  // rescan
  w.__routes=[[/certificates\/scan/,(u,o,j)=>j(202,{scan_id:'s',state:'running'})],[/summary_only/,()=>json(200,{scan:{state:'idle'}})],[/certificates\/inventory/,()=>json(200,inv(certs()))]];
  const callsBefore=calls.length;
  o2.querySelector('.cert-ov-rescan').click();
  await sleep(1700); await sleep(100);
  const sc=calls.slice(callsBefore);
  ok(sc.some(c=>c==='POST /b/certificates/scan {"instance_ids":["_broker"]}'),'rescan posts the broker scan: '+sc.join(' | '));
  ok(o2.querySelectorAll('tbody tr').length===6,'rescan reloads the table');
  // rescan refused
  w.__routes=[[/certificates\/scan/,(u,o,j)=>j(403,{success:false,error:'off'+EVIL})],[/certificates\/inventory/,()=>json(200,inv(certs()))]];
  o2.querySelector('.cert-ov-rescan').click(); await sleep(40);
  ok(/off/.test(o2.textContent)&&o2.querySelector('.cert-retry-button')&&bad(d)===0,'rescan 403 shows message');
  ok(errors.filter(e=>!/Error loading data/.test(e)).length===0,'no unexpected console errors '+errors.join('|').slice(0,200));
  expect(failures).toEqual([]);
}, 120000);
