const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep} = H.main;
const K='/s/valkey_crt', K2='/s/redis_crt', KX='/s/x_crt';
const cert=(key,variable,id,name,days)=>({key,variable,kind:'leaf',days_left:days,owners:[{kind:'instance',instance_id:id,instance_name:name}],rotation:{allowed:true,reasons:[]},findings:[]});
const inv={certificates:[cert(K,'valkey_crt','inst-a','a',10),cert(K2,'redis_crt','inst-a','a',12),cert(KX,'x_crt','inst-x','x',9)],capabilities:{rotation:{enabled:true}},settings:{warning_days:30,critical_days:7}};
const pvInst=(id,over={})=>({instance_id:id,instance_name:id,service:'valkey',plan:'small',vms:3,gate:'g',selected:[{name:K,days_left:10}],additional:[],blocked:null,impact:'x',...over});
t('rotate dialog focus and clearing', async () => {
  const env=boot(); const {w,d,errors,alerts,calls}=env; const T=w.__T;
  let preview=()=>({status:200,body:{preview_id:'pv',instances:[pvInst('inst-a'),pvInst('inst-x',{blocked:{code:'instance_busy',message:'Held.'}})],warnings:[]}});
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/certificates/inventory',(u,o,j)=>j(200,inv)],
    ['/b/certificates/rotation/preview',(u,o,j)=>{const r=preview(JSON.parse(o.body));return j(r.status,r.body);}],
    [/\/b\/upgrade\/tasks$/,(u,o,j)=>o.method==='POST'?j(201,{id:'task-1'}):j(200,[])]);
  w.serviceInstances={'inst-a':{service_id:'s',plan_id:'p',plan:{id:'p',name:'small'}},'inst-x':{service_id:'s',plan_id:'p',plan:{id:'p',name:'small'}}};
  await sleep(50); w.plansData={services:[{id:'s',name:'valkey'}]};
  d.getElementById('upgrade').classList.add('active'); await T.initUpgradeTab(); await sleep(100);
  const waitDialog=async()=>{ for(let i=0;i<50;i++){ if(d.querySelector('#dialog-modal[open]')) return true; await sleep(10);} return false; };
  const r=d.getElementById('rotate-op-rotate'); r.checked=true; r.dispatchEvent(new w.Event('change',{bubbles:true})); await sleep(120);
  const sel=w.blacksmithCerts.selection;
  const btn=()=>d.getElementById('rotate-preview');
  const posts=()=>calls.filter(c=>c.startsWith('POST')&&/"kind":"certificates"/.test(c)).length;
  // ---- I2 focus after Cancel
  sel.clear(); sel.set('inst-a',new Set([K])); T.rotateRefresh&&T.rotateRefresh();
  d.getElementById('rotate-select-visible').click(); await sleep(20);
  btn().focus(); ok(d.activeElement===btn(),'setup: preview button focused');
  btn().click();
  // while the preview is in flight the button announces busy without being disabled
  ok(btn().getAttribute('aria-disabled')==='true'&&!btn().disabled&&btn().textContent==='Previewing...','busy: aria-disabled and Previewing..., not disabled');
  await waitDialog();
  const before=calls.length;
  btn().click(); await sleep(30);
  ok(calls.filter(c=>/rotation\/preview/.test(c)).length===1,'busy: a second click starts no second preview');
  d.querySelector('#dialog-modal [data-dialog-role="cancel"]').click(); await sleep(60);
  ok(d.activeElement===btn(),'Cancel returns focus to the preview button: '+(d.activeElement&&(d.activeElement.id||d.activeElement.tagName)));
  ok(!btn().hasAttribute('aria-disabled')&&!btn().disabled,'button usable again after Cancel');
  // ---- I2 focus after an error
  preview=()=>({status:500,body:{success:false,error:'Boom.',code:'internal'}});
  btn().focus(); btn().click(); await sleep(80);
  const rh=d.getElementById('rotate-result'); const live=d.getElementById('blacksmith-live-region');
  ok(!d.querySelector('#dialog-modal[open]')&&rh.contains(d.activeElement)&&d.activeElement!==btn(),'N1: focus stays inside the result panel after an error: '+(d.activeElement&&(d.activeElement.id||d.activeElement.tagName)));
  ok(!!live&&live.textContent.includes('The preview failed')&&live.textContent.includes('Boom.'),'N1: the error message is written to the live region: '+(live&&live.textContent));
  ok(!!rh.querySelector('.result-panel-retry'),'N1: Retry is in the panel');
  // ---- M2/M3 clear only submitted pairs; callback shape
  preview=()=>({status:200,body:{preview_id:'pv',instances:[pvInst('inst-a'),pvInst('inst-x',{blocked:{code:'instance_busy',message:'Held.'}})],warnings:[]}});
  sel.clear(); sel.set('inst-a',new Set([K,K2])); sel.set('inst-x',new Set([KX]));
  btn().click(); await waitDialog();
  d.querySelector('#dialog-modal [data-dialog-role="confirm"]').click(); await sleep(100);
  ok(posts()===1,'create posted once');
  ok(!sel.has('inst-a')&&sel.has('inst-x')&&sel.get('inst-x').has(KX),'M2: the excluded instance stays selected after the create: '+JSON.stringify([...sel].map(([k,v])=>[k,[...v]])));
  // pairs the preview did not submit stay too
  sel.clear(); sel.set('inst-a',new Set([K])); 
  let got=null;
  w.blacksmithRotate.previewAndCreate({mode:'redeploy',selections:[{instance_id:'inst-a',variables:[K]}],origin:'test-entry',onCreated:(t,inc)=>{got=[t,inc];}}); 
  await waitDialog(); d.querySelector('#dialog-modal [data-dialog-role="confirm"]').click(); await sleep(100);
  ok(got&&got[0].id==='task-1'&&Array.isArray(got[1])&&got[1].length===1&&got[1][0].instance_id==='inst-a','M3: onCreated(task, included) receives the task and only included instances');
  ok(sel.has('inst-a'),'M3: another entry point with the origin label does not clear the batch selection');
  const hostPanel=d.querySelector('[data-origin="test-entry"]');
  ok(!!hostPanel,'origin kept as a label');
  ok(alerts.length===0&&!errors.some(e=>/Reference|TypeError/.test(e)),'no alerts or script errors: '+errors.join('|'));
  expect(failures).toEqual([]);
}, 120000);
