const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep} = H.main;
const HOSTILE='<img src=x>';
const inst=(id,over={})=>({instance_id:id,instance_name:id,service:'valkey',plan:'small',vms:2,gate:'',selected:[{name:`/b/${id}/valkey_cluster_crt`,days_left:5}],additional:[],blocked:null,impact:'',...over});

t('preview body layout', async () => {
  const env=boot(); const {w,d,errors,alerts}=env; const T=w.__T;
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/certificates/inventory',(u,o,j)=>j(200,{certificates:[],capabilities:{rotation:{enabled:true}}})]);
  await sleep(50);
  const build=(preview,mode,opts)=>T.buildPreviewBody(preview,mode,opts);

  // one instance, mixed badges, hostile names
  let n=build({instances:[inst('a',{gate:'g',selected:[
      {name:`/x/${HOSTILE}/crit_crt`,days_left:0},{name:'/x/warn_crt',days_left:20},{name:'/x/far_crt',days_left:200},
      {name:'/x/exp_crt',days_left:-3},{name:'/x/none_crt'}],additional:[{name:'/x/extra_crt'}]})],warnings:[]},'redeploy');
  const rows=[...n.querySelectorAll('.rotate-preview-cert')];
  const badge=(i)=>rows[i].querySelector('.cert-pill');
  ok(rows[0].querySelector('code').textContent==='crit_crt'&&badge(0).textContent==='Expires today'&&badge(0).classList.contains('cert-pill--critical'),'today is critical: '+rows[0].textContent);
  ok(badge(1).textContent==='20 days left'&&badge(1).classList.contains('cert-pill--warning'),'inside the warning threshold');
  ok(badge(2).textContent==='200 days left'&&badge(2).classList.contains('cert-pill--unknown'),'outside every threshold is neutral');
  ok(badge(3).textContent==='Expired'&&badge(3).classList.contains('cert-pill--expired'),'negative days read Expired');
  ok(badge(4)&&badge(4).textContent==='Expiry unknown'&&badge(4).classList.contains('cert-pill--unknown'),'missing days_left reads Expiry unknown');
  ok(rows[0].querySelector('.rotate-preview-path').textContent===`/x/${HOSTILE}/crit_crt`&&n.querySelectorAll('img').length===0,'hostile path renders as text');
  ok(/not close to expiry/.test(n.textContent),'non-blocking note when a certificate is far from expiry');
  ok(/5 certificates plus 1 additional variable/.test(n.querySelector('.rotate-preview-summary').textContent),'summary counts certificates and additional variables: '+n.querySelector('.rotate-preview-summary').textContent);
  ok(!n.querySelector('details'),'up to 3 instances are plain cards');

  // no note when everything is near expiry; VM total counts included only
  n=build({instances:[inst('a'),inst('b',{vms:4,blocked:{code:'x',message:'Held '+HOSTILE}})],warnings:[]},'redeploy');
  ok(!/not close to expiry/.test(n.textContent),'no note when all are near expiry');
  ok(/1 to change, 1 excluded · 2 VMs/.test(n.querySelector('.rotate-preview-summary').textContent),'VM total counts included instances only');
  ok(/Held <img src=x>/.test(n.querySelector('.rotate-preview-excluded').textContent)&&n.querySelectorAll('img').length===0,'excluded reason is text');

  // more than 3: compact rows and Expand all
  const many=build({instances:['a','b','c','d'].map((id,i)=>inst(id,{selected:[{name:`/p/${id}_crt`,days_left:10+i},{name:`/p/${id}2_crt`,days_left:3}]})),warnings:[]},'redeploy');
  const ds=[...many.querySelectorAll('details')];
  ok(ds.length===4&&ds.every(x=>!x.open),'collapsed rows past 3 instances');
  ok(/Certificates: 2/.test(ds[0].querySelector('summary').textContent)&&/VMs: 2/.test(ds[0].querySelector('summary').textContent)&&/3 days left/.test(ds[0].querySelector('summary').textContent),'summary row: count, VMs, earliest badge');
  ok(ds[0].querySelectorAll('.rotate-preview-path').length===2,'full paths live inside the expanded content');
  const tog=many.querySelector('.rotate-preview-expand'); tog.click();
  ok(ds.every(x=>x.open)&&tog.textContent==='Collapse all','Expand all opens every row'); tog.click();
  ok(ds.every(x=>!x.open),'Collapse all closes them');

  // patch: warnings first, once; policy shown
  n=build({instances:[inst('a')],warnings:['Temporary repair: BOSH can restore it.','Other '+HOSTILE]},'patch',{converge:'immediate'});
  const text=n.textContent;
  ok((text.match(/Temporary repair/g)||[]).length===1,'temporary repair shown once');
  ok(n.querySelector('.rotate-preview-callout').compareDocumentPosition(n.querySelector('.rotate-preview-cards'))&4,'callout precedes the instances');
  ok(/Follow-up: Converge right away/.test(text)&&n.querySelector('.rotate-preview-callout strong').textContent.startsWith('Follow-up')&&/restarts a second time/.test(text)&&/Other <img src=x>/.test(text)&&n.querySelectorAll('img').length===0,'policy and server warning');
  ok(/swap the certificate files over SSH/.test(text),'patch description');

  // rollback risk stated once; unconverged follow-up leads the callout
  n=build({instances:[inst('a')],warnings:[]},'patch',{converge:'pending'});
  ok(n.querySelector('.rotate-preview-callout p').textContent.startsWith('Follow-up: Leave unconverged.')&&(n.textContent.match(/restore the old certificate/g)||[]).length===1,'follow-up first, rollback risk once');

  // compact row exceptions, grid parts, excluded link, pinned summary
  n=build({instances:['a','b','c','d'].map((id)=>inst(id,{selected:[{name:'/p/a_crt',days_left:-2}],additional:[{name:'/p/x_crt'}]})).concat([inst('z',{blocked:{code:'x',message:'No'}})]),warnings:[]},'redeploy');
  const row=n.querySelector('summary');
  ok(/1 expired, 1 additional/.test(row.textContent)&&!!row.querySelector('.rotate-preview-chevron')&&!!row.querySelector('.rotate-preview-row-badge .cert-pill'),'row: chevron, badge, exceptions');
  const lnk=n.querySelector('.rotate-preview-link'); ok(lnk&&/1 excluded/.test(lnk.textContent)&&n.pinnedNode===n.querySelector('.rotate-preview-summary'),'excluded count is a link and the summary is pinned');
  lnk.click(); ok(true,'excluded link click does not throw');

  // converge: no certificate rows
  n=build({instances:[inst('a')],warnings:[]},'converge');
  ok(!n.querySelector('.rotate-preview-cert')&&/redeploy applies the patched values/.test(n.textContent),'converge lists no certificates');

  ok(errors.filter(e=>!/Error loading data/.test(String(e))).length===0&&alerts.length===0,'no script errors or dialogs: '+JSON.stringify(errors));
  expect(failures).toEqual([]);
});
