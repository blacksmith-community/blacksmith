const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL} = H.main;
const EV2='<img/src=x/onerror=alert(1)>';
const probe=(locator,matches,state='ok')=>({source:'probe',locator,matches,state});
const dcert=(over={})=>({id:'c1',key:'/s/a_crt',variable:'a_crt',kind:'leaf',days_left:50,expiry:'ok',health:'ok',owners:[{kind:'instance',instance_id:'inst-a',instance_name:'a'}],rotation:{allowed:true,modes:['redeploy','patch'],reasons:[]},findings:[],observations:[],...over});
const vm=(name,phase,over={})=>({vm:name,cid:'cid-'+name,group:name.split('/')[0],index:0,phase,files:[{path:'/var/vcap/jobs/x/crt.pem',job:'x',role:'certificate',variable:'a_crt',state:'swapped'}],processes:['x','y'],targets:[{variable:'a_crt',port:6379,method:'tls_probe',ok:true,detail:'served '+EVIL}],gate_after:'passed',...over});
const mkTask=(over={},inst={})=>({id:'task-1',name:'N',kind:'certificates',status:'running',paused:false,certificates:{mode:'patch',converge:'pending'},total_count:1,completed_count:0,failed_count:0,created_at:'2026-10-21T14:00:00Z',
  instances:[{instance_id:'i-1',instance_name:'one',status:'running',certificates:{phase:'patch',variables:[],vms:[vm('web/0','done'),vm(EVIL+'/1','swapping',{files:[{path:EVIL,role:EVIL,state:EVIL}],error:'boom '+EVIL,gate_after:''})]},...inst}],...over});
t('partly patched instances', async () => {
  const env=boot(); const {w,d,errors,alerts,calls}=env; const T=w.__T;
  let divergent=true;
  const certs=()=>[dcert({divergence:divergent?{state:'unconverged',task_id:'t9',since:'2026-10-21T14:00:00Z'}:undefined,observations:[probe('web/0 10.0.0.5:6379','latest'),probe(EV2+'/1 [fe80::1]:6379','deployed'),probe('web/3 port 6379','latest'),probe('web/2 10.0.0.7:6379','latest','failed'),probe('','latest')]})];
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],
    ['/b/certificates/inventory',(u,o,j)=>j(200,{certificates:certs(),capabilities:{rotation:{enabled:true},patch_in_place:{enabled:true}}})],
    [/certificates\/instances\/inst-a$/,(u,o,j)=>j(200,{instance:{id:'inst-a',name:'a'},scan:{},certificates:certs(),capabilities:{rotation:{enabled:true},patch_in_place:{enabled:true}}})],
    [/rotation\/preview$/,(u,o,j)=>j(200,{preview_id:'pv1',instances:[{instance_id:'inst-a',instance_name:'a',vms:2,gate:'g',selected:[],additional:[],blocked:null,impact:'Deploys.'}],warnings:[]})],
    [/\/b\/upgrade\/tasks$/,(u,o,j)=>o.method==='POST'?j(201,{id:'t-new'}):j(200,[])]);
  w.serviceInstances={}; await sleep(50); w.plansData={services:[]};
  d.getElementById('upgrade').classList.add('active'); await T.initUpgradeTab(); await sleep(80);
  const posts=(re)=>calls.filter(c=>c.startsWith('POST')&&re.test(c)).map(c=>JSON.parse(c.slice(c.indexOf(' {')+1)));
  const waitDialog=async()=>{ for(let i=0;i<50;i++){ if(d.querySelector('#dialog-modal[open]')) return true; await sleep(10);} return false; };
  const confirmBtn=()=>d.querySelector('#dialog-modal [data-dialog-role="confirm"]');
  const cancelBtn=()=>d.querySelector('#dialog-modal [data-dialog-role="cancel"]');
  const panel=()=>d.querySelector('#upgrade .upgrade-tasks');

  // banner
  const host=d.createElement('div'); d.body.appendChild(host);
  await T.CertOverview.mountInstance(host,'inst-a'); await sleep(60);
  const ban=host.querySelector('.cert-ov-divergence');
  ok(!!ban,'banner shown for an unconverged instance');
  const bt=ban.textContent;
  ok(/web\/0 holds the patched certificate/.test(bt),'banner names the VM that holds the patched version');
  ok(bt.includes(EV2+'/1 holds the old certificate')&&ban.querySelectorAll('img,script').length===0,'banner names the VM with the old version; hostile locator inert');
  ok(!/web\/2/.test(bt),'failed probe not counted as a holder');
  ok(/A listener holds the patched certificate/.test(bt),'a probe without a locator reads as a listener, not as the letter a');
  ok(/web\/0 holds the patched certificate/.test(bt)&&!/10\.0\.0\.5/.test(bt)&&!/\[fe80/.test(bt)&&/web\/3 holds the patched certificate/.test(bt)&&!/port 6379/.test(bt),'probe fallback parses the real locator format: IPv4, bracketed IPv6, no address');
  ok(/recreate or a resurrection restores the old certificate/.test(bt),'banner says a recreate or resurrection restores the old certificate');
  const cn=ban.querySelector('.cert-ov-converge');
  ok(cn&&cn.textContent==='Converge now','Converge now button');
  cn.click(); ok(await waitDialog(),'Converge now opens the confirm dialog');
  const pb=posts(/rotation\/preview/).pop();
  ok(JSON.stringify(pb)===JSON.stringify({mode:'converge',selections:[{instance_id:'inst-a',variables:[]}]}),'Converge now previews mode converge: '+JSON.stringify(pb));
  confirmBtn().click(); await sleep(100);
  const cr=posts(/"kind":"certificates"/).pop();
  ok(cr&&cr.preview_id==='pv1','Converge now creates after confirm');
  ok(host.querySelector('.cert-ov-rotate-host').textContent.includes('t-new'),'result panel shows in the instance host');
  // no probes fallback
  w.__routes.unshift([/certificates\/instances\/inst-a$/,(u,o,j)=>j(200,{instance:{id:'inst-a'},scan:{},certificates:[dcert({divergence:{state:'reverted',task_id:'t9',since:'2026-10-21T14:00:00Z'}})],capabilities:{rotation:{enabled:true}}})]);
  await T.CertOverview.mountInstance(host,'inst-a'); await sleep(60);
  const b2=host.querySelector('.cert-ov-divergence');
  ok(b2&&/went back to the old certificate/.test(b2.textContent)&&/could not read which VMs/.test(b2.textContent),'reverted banner with no probes uses the fallback text');
  w.__routes.shift();
  divergent=false;
  w.__routes.unshift([/certificates\/instances\/inst-a$/,(u,o,j)=>j(200,{instance:{id:'inst-a'},scan:{},certificates:certs(),capabilities:{rotation:{enabled:true}}})]);
  await T.CertOverview.mountInstance(host,'inst-a'); await sleep(60);
  ok(!host.querySelector('.cert-ov-divergence'),'no banner for a converged instance');
  w.__routes.shift();

  // banner from the record's vms (preferred over probes)
  w.__routes.unshift([/certificates\/instances\/inst-a$/,(u,o,j)=>j(200,{instance:{id:'inst-a'},scan:{},certificates:[dcert({divergence:{state:'unconverged',task_id:'t9',since:'2026-10-21T14:00:00Z',vms:[{group:'web',index:0,state:'applied'},{group:'web',index:1,state:'old'},{group:'db',index:2,state:'reverted'},{group:'db',index:3,state:'unknown'},{group:EVIL,index:4,state:EVIL}]},observations:[probe('zzz/9 10.0.0.9:1','latest')]})],capabilities:{rotation:{enabled:true}}})]);
  await T.CertOverview.mountInstance(host,'inst-a'); await sleep(60);
  { const t=host.querySelector('.cert-ov-divergence').textContent;
    ok(/web\/0 holds the patched certificate/.test(t)&&/web\/1 holds the old certificate/.test(t)&&/db\/2 went back to the old certificate/.test(t)&&/db\/3 is not known to hold either/.test(t),'banner lists VMs from the record, by state');
    ok(!/zzz/.test(t)&&host.querySelectorAll('.cert-ov-divergence img,.cert-ov-divergence script').length===0,'record wins over probes; hostile group and state inert'); }
  w.__routes.shift();
  // an unknown state from the API shows no banner at all
  w.__routes.unshift([/certificates\/instances\/inst-a$/,(u,o,j)=>j(200,{instance:{id:'inst-a'},scan:{},certificates:[dcert({divergence:{state:'converging',task_id:'t9',since:'2026-10-21T14:00:00Z'}})],capabilities:{rotation:{enabled:true}}})]);
  await T.CertOverview.mountInstance(host,'inst-a'); await sleep(60);
  ok(!host.querySelector('.cert-ov-divergence'),'a state the API never sends draws no banner');
  w.__routes.shift();
  // another task holds the instance: Converge now is off, with the holder as visible text
  w.__routes.unshift([/certificates\/instances\/inst-a$/,(u,o,j)=>j(200,{instance:{id:'inst-a'},scan:{},certificates:[dcert({divergence:{state:'unconverged',task_id:'t9',since:'2026-10-21T14:00:00Z'}})],capabilities:{rotation:{enabled:true}}})],
    [/\/b\/upgrade\/tasks$/,(u,o,j)=>j(200,[{id:'tk',name:'Spring',kind:'certificates',status:'running'}])],
    [/\/b\/upgrade\/tasks\/tk$/,(u,o,j)=>j(200,{id:'tk',name:'Spring',kind:'certificates',status:'running',instances:[{instance_id:'inst-a',status:'running'}]})]);
  await T.CertOverview.mountInstance(host,'inst-a'); await sleep(80);
  { const b=host.querySelector('.cert-ov-divergence'); const c=b&&b.querySelector('.cert-ov-converge'); const why=b&&b.querySelector('.cert-ov-converge-reason');
    ok(c&&c.disabled&&why&&/^Busy: rotation "Spring"/.test(why.textContent)&&c.getAttribute('aria-describedby')===why.id,'Converge now is disabled while a task holds the instance, with the reason shown'); }
  w.__routes.shift(); w.__routes.shift(); w.__routes.shift();

  // task detail VM list
  T.renderTaskDetail(mkTask());
  const list=panel().querySelector('.cert-task-vms');
  const dets=[...panel().querySelectorAll('details.cert-task-vm')];
  ok(!!list&&dets.length===2&&dets.every(x=>x.dataset.keepKey&&!x.open),'two collapsed VM details with keep keys');
  ok(/done/.test(dets[0].querySelector('summary').textContent)&&/web\/0/.test(dets[0].querySelector('summary').textContent),'summary shows VM and phase');
  const t0=dets[0].textContent;
  ok(dets[0].querySelectorAll('table').length>=3&&!dets[0].querySelector('ul'),'VM evidence uses tables instead of bullets');
  ok(/crt\.pem/.test(t0)&&/certificate/.test(t0)&&/swapped/.test(t0)&&/Processes/.test(t0)&&/x/.test(t0)&&/a_crt/.test(t0)&&/6379/.test(t0)&&/tls probe/.test(t0)&&/Health gate/.test(t0)&&/passed/.test(t0),'details show files, processes, verification method and result, gate');
  ok(dets[1].textContent.includes(EVIL)&&panel().querySelectorAll('img,script').length===0,'hostile VM name, file, and error are inert text');
  dets[0].open=true;
  T.renderTaskDetail(mkTask({completed_count:0}));
  const again=[...panel().querySelectorAll('details.cert-task-vm')];
  ok(again[0].open&&!again[1].open,'expanded VM stays open across a poll');
  // remediation for a partly patched instance
  T.renderTaskDetail(mkTask({status:'cancelled'},{status:'cancelled'}));
  const rem=[...panel().querySelectorAll('[data-decision-action="remediate"]')];
  ok(rem.length===1&&rem[0].textContent==='Converge this instance'&&!rem[0].disabled,'partly patched cancelled instance offers Converge this instance');
  T.renderTaskDetail(mkTask({status:'cancelled'},{status:'cancelled',certificates:{phase:'patch',variables:[],vms:[vm('web/0','staged')]}}));
  ok(panel().querySelectorAll('[data-decision-action="remediate"]').length===0,'cancelled instance with nothing swapped offers no remediation');
  T.renderTaskDetail(mkTask({certificates:{mode:'redeploy'}},{status:'failed',certificates:{phase:'deploy',variables:[{name:'/x/a',old_serial:'01',new_serial:'02'}]}}));
  ok(panel().querySelector('[data-decision-action="remediate"]').textContent==='Redeploy this instance','redeploy failure offers Redeploy this instance');

  ok(alerts.length===0,'no alerts');
  ok(!errors.some(e=>/Reference|TypeError|LOAD/.test(e)),'no script errors: '+errors.join('|'));
  expect(failures).toEqual([]);
}, 120000);
