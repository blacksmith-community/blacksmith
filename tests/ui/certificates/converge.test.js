const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');
const { pinZone } = require('../fixed-zone.js');
pinZone('UTC');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL} = H.main;
t('converge preview and create', async () => {
  const env=boot(); const {w,d,errors,alerts,calls}=env; const T=w.__T;
  let previews=[], creates=[];
  const J=(j,r)=>j(r.status,r.body);
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/certificates/inventory',(u,o,j)=>j(200,{certificates:[],capabilities:{}})],
    [/\/b\/upgrade\/preview$/,(u,o,j)=>{ const a=previews.shift(); return J(j,a||{status:200,body:{id:'pv-0',expires_at:'x',instance_ids:['inst-1'],diverged:[]}}); }],
    [/\/b\/upgrade\/tasks$/,(u,o,j)=>{ if(o.method==='POST'){ const a=creates.shift(); return J(j,a||{status:201,body:{id:'task-1'}}); } return j(200,[]); }]);
  w.serviceInstances={'inst-1':{instance_name:'orders '+EVIL}}; await sleep(50); w.plansData={services:[]};
  d.getElementById('upgrade').classList.add('active'); await T.initUpgradeTab(); await sleep(80);
  const sel=d.getElementById('upgrade-stemcell-filter'); const opt=d.createElement('option'); opt.value='ubuntu-noble/1.2'; opt.textContent='x'; sel.appendChild(opt); sel.value='ubuntu-noble/1.2';
  const posts=(re)=>calls.filter(c=>c.startsWith('POST')&&re.test(c)).map(c=>JSON.parse(c.slice(c.indexOf(' {')+1)));
  const waitDialog=async()=>{ for(let i=0;i<60;i++){ if(d.querySelector('#dialog-modal[open]')) return true; await sleep(10);} return false; };
  // A diverged upgrade asks for the typed word, so the helper types it when the field exists.
  const confirmBtn=()=>{ const ack=d.querySelector('#dialog-modal .dialog-modal-ack-input'); if(ack) ack.value='upgrade'; return d.querySelector('#dialog-modal [data-dialog-role="confirm"]'); };
  const cancelBtn=()=>d.querySelector('#dialog-modal [data-dialog-role="cancel"]');
  const dlgText=()=>d.querySelector('#dialog-modal').textContent;
  const run=async(n)=>{ T.selectedUpgradeInstances.clear(); T.selectedUpgradeInstances.add('inst-1'); const p=T.startUpgrade(); await waitDialog(); confirmBtn().click(); /* name prompt */ return p; };
  const div=(extra={})=>({instance_id:'inst-1',divergence:{state:'unconverged',task_id:'t1',since:'2026-10-21T14:00:00Z'},variables:['/s/a_crt'],additional:[{name:'/s/other_crt',deployed_version_id:'v1',latest_version_id:'v2'}],...extra});

  // the upgrade start refuses without a target or an instance through Feedback, never alert
  { T.selectedUpgradeInstances.clear(); const warned=[]; const warn=w.Feedback.warning; w.Feedback.warning=(m)=>{ warned.push(m); };
    await T.startUpgrade();
    w.Feedback.warning=warn;
    ok(alerts.length===0&&warned.length===1&&/Select at least one instance/.test(warned[0]),'upgrade start with no instance warns through Feedback, not alert'); }

  // 1: no diverged instance -> no dialog beyond the name prompt, create at once
  let p=run(); await sleep(150);
  ok(!d.querySelector('#dialog-modal[open]'),'empty diverged list opens no confirm dialog');
  await p;
  const pv1=posts(/\/b\/upgrade\/preview/), cr1=posts(/"instance_ids"/).filter(b=>b.name);
  ok(pv1.length===1&&JSON.stringify(pv1[0])===JSON.stringify(cr1[0]),'preview body matches the create body');
  ok(cr1.length===2&&!('upgrade_preview_id' in cr1[1]),'create sent without a preview id when nothing diverged');
  // 2: diverged -> dialog, create after confirm with id
  previews=[{status:200,body:{id:'pv-A'+EVIL,expires_at:'x',instance_ids:['inst-1'],diverged:[div({instance_id:EVIL})]}}];
  T.selectedUpgradeInstances.clear(); T.selectedUpgradeInstances.add('inst-1');
  const nBefore=posts(/"instance_ids"/).length;
  p=T.startUpgrade(); await waitDialog(); confirmBtn().click(); await sleep(100);
  ok(d.querySelector('#dialog-modal[open]')&&/not converged/.test(dlgText()),'confirm dialog opens for a diverged instance');
  ok(/To continue, type upgrade below\./.test(dlgText()),'a diverged upgrade asks for the word upgrade');
  ok(dlgText().includes('2026-10-21 14:00:00 UTC')&&!dlgText().includes('2026-10-21T14')&&dlgText().includes(EVIL)&&dlgText().includes('/s/other_crt')&&dlgText().includes('/s/a_crt')&&d.querySelectorAll('#dialog-modal img,#dialog-modal script').length===0,'dialog names the instance and its additional variables, hostile inert');
  ok(posts(/"instance_ids"/).length===nBefore+1,'no create before confirm (preview only)');
  cancelBtn().click(); await p; await sleep(30);
  ok(posts(/"instance_ids"/).length===nBefore+1,'Cancel creates no task');
  // 3: confirm
  previews=[{status:200,body:{id:'pv-B',expires_at:'x',instance_ids:['inst-1'],diverged:[div()]}}];
  p=run(); await sleep(60); await waitDialog(); confirmBtn().click(); await p; await sleep(30);
  const cr3=posts(/"instance_ids"/).filter(b=>b.upgrade_preview_id);
  ok(cr3.length===1&&cr3[0].upgrade_preview_id==='pv-B'&&alerts.length===0,'confirm creates with upgrade_preview_id');
  // 4: 409 -> re-preview and show again
  previews=[{status:200,body:{id:'pv-C',diverged:[div()]}},{status:200,body:{id:'pv-D',diverged:[div()]}}];
  creates=[{status:409,body:{error:'preview needed',code:'upgrade_preview_required'}},{status:201,body:{id:'task-9'}}];
  p=run(); await sleep(80); await waitDialog(); confirmBtn().click(); await sleep(120);
  ok(d.querySelector('#dialog-modal[open]')&&/fresh one/.test(dlgText()),'409 re-previews and opens the dialog again with a note');
  confirmBtn().click(); await p; await sleep(30);
  const ids=posts(/"instance_ids"/).filter(b=>b.upgrade_preview_id).map(b=>b.upgrade_preview_id);
  ok(ids.slice(-2).join()==='pv-C,pv-D','second create uses the new preview id: '+ids.join());
  // 5: 410
  previews=[{status:200,body:{id:'pv-E',diverged:[div()]}},{status:200,body:{id:'pv-F',diverged:[]}}];
  creates=[{status:410,body:{error:'gone',code:'preview_expired'}},{status:201,body:{id:'task-10'}}];
  p=run(); await sleep(80); await waitDialog(); confirmBtn().click(); await p; await sleep(60);
  ok(posts(/\/b\/upgrade\/preview/).length>=7&&!d.querySelector('#dialog-modal[open]'),'410 re-previews; a clean second preview creates without a dialog');
  // 6: bounded
  previews=[1,2,3,4].map(i=>({status:200,body:{id:'pv-G'+i,diverged:[div()]}}));
  creates=[1,2,3,4].map(()=>({status:409,body:{error:'need',code:'upgrade_preview_required'}}));
  const before=posts(/\/b\/upgrade\/preview/).length;
  p=run(); for(let i=0;i<3;i++){ await sleep(80); if(await waitDialog()) confirmBtn().click(); } await p; await sleep(60);
  ok(posts(/\/b\/upgrade\/preview/).length-before===3,'re-preview rounds are bounded at 3');
  ok(!d.querySelector('#dialog-modal[open]'),'no dialog left open');
  // 7: preview error 503
  previews=[{status:503,body:{error:'vault down',code:'vault_unavailable'}}]; creates=[];
  const nc=posts(/"instance_ids"/).length;
  p=run(); await p; await sleep(40);
  ok(posts(/"instance_ids"/).length===nc+1,'preview failure creates nothing');
  ok(alerts.length===0,'no alerts: '+alerts.join('|'));
  ok(!errors.some(e=>/Reference|TypeError|LOAD/.test(e)),'no script errors: '+errors.join('|'));
  expect(failures).toEqual([]);
}, 120000);
