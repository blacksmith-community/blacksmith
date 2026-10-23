const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep} = H.main;
t('fleet finding toggles', async () => {
  const env=boot(); const {w,d,errors,calls}=env; const T=w.__T;
  const cert={id:'c1',key:'/s/a_crt',variable:'a_crt',kind:'leaf',days_left:50,expiry:'ok',health:'ok',owners:[{kind:'instance',instance_id:'inst-a',instance_name:'a'}],rotation:{allowed:true,reasons:[]},findings:[]};
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/upgrade/tasks',(u,o,j)=>j(200,[])],
    [/certificates\/inventory/,(u,o,j)=>j(200,{certificates:[cert],capabilities:{rotation:{enabled:true}},settings:{warning_days:30,critical_days:7},scan:{state:'idle'},summary:{certificates:1,needs_attention:3,unconverged:2,reverted:1,health_errors:0,expired:0,critical:0,warning:0,expiry_ok:1},filtered:{certificates:1}})]);
  w.serviceInstances={}; await sleep(50); w.plansData={services:[]};
  d.getElementById('certificates').classList.add('active');
  await T.CertsFleet.init(); await sleep(200);
  const btn=()=>[...d.querySelectorAll('#cert-strip-attention button')].find(b=>/Unconverged/.test(b.textContent));
  ok(btn()&&/3/.test(btn().textContent),'Unconverged toggle counts unconverged plus reverted: '+(btn()&&btn().textContent));
  const n0=calls.length; btn().click(); await sleep(150);
  const reqs=calls.slice(n0).filter(c=>/certificates\/inventory/.test(c));
  ok(reqs.some(c=>/finding=unconverged/.test(c)&&/finding=patch_reverted/.test(c)),'pressing the toggle filters to both findings: '+reqs.join(' | '));
  ok(btn().getAttribute('aria-pressed')==='true','toggle shows pressed');
  const n1=calls.length; btn().click(); await sleep(150);
  ok(!calls.slice(n1).filter(c=>/certificates\/inventory/.test(c)).some(c=>/finding=/.test(c)),'releasing the toggle clears both findings');
  ok(!errors.some(e=>/Reference|TypeError|LOAD/.test(e)),'no script errors: '+errors.join('|'));
  expect(failures).toEqual([]);
}, 120000);
