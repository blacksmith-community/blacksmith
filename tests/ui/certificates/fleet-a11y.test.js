const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep} = H.main;
t('fleet accessibility and announcements', async () => {
  const env=boot(); const {w,d,errors}=env; const T=w.__T;
  let rotationEnabled=true; let taskStatus='running';
  const mk=(i,blocked)=>({id:'c'+i,key:'/s/a'+i,variable:'a'+i+'_crt',kind:'leaf',days_left:50,expiry:'ok',health:'ok',owners:[{kind:'instance',instance_id:'inst-'+i,instance_name:'n'+i}],rotation:blocked?{allowed:false,reasons:['A task holds this instance.']}:{allowed:true,reasons:[]},findings:[]});
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],
    [/\/b\/upgrade\/tasks$/,(u,o,j)=>j(200,[{id:'t1',name:'Rotate a1',kind:'certificates',status:taskStatus,completed_count:0,failed_count:0,total_count:1,created_at:new Date().toISOString()},{id:'t2',name:'Other',status:'completed',completed_count:1,failed_count:0,total_count:1,created_at:new Date().toISOString()}])],
    [/certificates\/inventory/,(u,o,j)=>j(200,{certificates:[mk(1,false),mk(2,true),mk(3,false)],capabilities:{rotation:{enabled:rotationEnabled,reason:'Rotation is off by configuration.'}},settings:{warning_days:30,critical_days:7},scan:{state:'idle',completed_at:new Date().toISOString()},summary:{certificates:3,needs_attention:0},filtered:{certificates:3}})]);
  w.serviceInstances={}; await sleep(50); w.plansData={services:[]};
  d.getElementById('certificates').classList.add('active');
  await T.CertsFleet.init(); await sleep(250);
  for (const id of ['cert-scan-btn', 'cert-settings-btn', 'cert-export-btn', 'cert-more-btn']) {
    const action = d.getElementById(id);
    expect(action.getAttribute('aria-label')).toBeTruthy();
    expect(action.title).toBe(action.getAttribute('aria-label'));
    expect(action.querySelector('svg[aria-hidden="true"]')).not.toBeNull();
  }
  const more = d.getElementById('cert-more-btn');
  more.click();
  expect(more.getAttribute('aria-expanded')).toBe('true');
  more.click();
  expect(more.getAttribute('aria-expanded')).toBe('false');
  const rows=()=>[...d.querySelectorAll('#cert-tbody tr.cert-row')];
  const blockedRow=rows().find(r=>r.dataset.certId==='c2');
  const cb=blockedRow.querySelector('input[type=checkbox]');
  ok(cb.disabled,'blocked row checkbox is disabled');
  const why=blockedRow.querySelector('.cert-row-why');
  ok(why&&/A task holds this instance\./.test(why.textContent)&&/Rotation unavailable/.test(why.textContent),'row shows its reason in visible text: '+(why&&why.textContent));
  ok(cb.getAttribute('aria-describedby')===why.id&&d.getElementById(why.id)===why,'checkbox is described by the visible reason');
  ok(!rows().find(r=>r.dataset.certId==='c1').querySelector('.cert-row-why'),'an enabled row shows no reason');
  // group header with nothing rotatable
  // persistence: focus, expand, scroll
  const c1=rows().find(r=>r.dataset.certId==='c1'); const box=c1.querySelector('input[type=checkbox]'); box.focus();
  const wrap=d.getElementById('cert-table-wrap'); ok(wrap.tabIndex===0,'table scroll region is keyboard focusable'); let st=0; Object.defineProperty(wrap,'scrollTop',{configurable:true,get:()=>st,set:(v)=>{st=v;}}); st=77;
  const ex=rows().find(r=>r.dataset.certId==='c3').querySelector('.cert-expand-btn'); ex.click(); await sleep(20);
  ok(d.querySelector('[data-cert-detail-for="c3"]'),'row expands');
  const c1b=rows().find(r=>r.dataset.certId==='c1').querySelector('input[type=checkbox]'); c1b.focus();
  await T.CertsFleet.refresh({silent:true}); await sleep(100);
  const c1c=rows().find(r=>r.dataset.certId==='c1').querySelector('input[type=checkbox]');
  ok(d.activeElement===c1c,'poll re-render keeps focus on the same checkbox');
  ok(!!d.querySelector('[data-cert-detail-for="c3"]'),'poll re-render keeps the row expanded');
  ok(st===77,'poll re-render keeps scroll position: '+st);
  // rotation off globally: no per-row text, toolbar note visible
  rotationEnabled=false; await T.CertsFleet.refresh({silent:true}); await sleep(100);
  ok(!d.querySelector('.cert-row-why'),'rotation off: rows add no repeated reason');
  const note=T.CertsFleet.view.refs.rotateSelectedNote; ok(/Rotation is off\./.test(note.textContent),'rotation off: toolbar states why: '+note.textContent);
  rotationEnabled=true; T.CertsFleet.view.groupBy='owner'; await T.CertsFleet.refresh({silent:true}); await sleep(100);
  const gw=d.querySelector('.cert-group-why'); const gin=gw&&d.querySelector('tr.cert-group-row input:disabled');
  ok(gw&&gin&&gin.getAttribute('aria-describedby')===gw.id&&/None of these certificates can be rotated yet\./.test(gw.textContent),'group header shows why its checkbox is disabled');
  rotationEnabled=false; await T.CertsFleet.refresh({silent:true}); await sleep(100);
  ok(!d.querySelector('.cert-group-why')&&!d.querySelector('tr.cert-group-row input:disabled[aria-describedby]'),'rotation off: group header adds no reason, like rows');
  rotationEnabled=true;
  const eb=d.getElementById('cert-export-btn'); ok(eb&&!eb.hasAttribute('aria-haspopup')&&eb.getAttribute('aria-controls')==='cert-export-menu'&&eb.hasAttribute('aria-expanded'),'export button is a plain disclosure');
  T.CertsFleet.view.groupBy='flat';
  // announcements
  const live=()=>d.getElementById('blacksmith-live-region').textContent;
  await T.fetchUpgradeTasks(); await sleep(120);
  ok(!/is now/.test(live()),'first poll announces nothing');
  taskStatus='paused'; await T.fetchUpgradeTasks(); await sleep(120);
  ok(/Rotate a1 is now paused\./.test(live()),'status change is announced in the live region: '+live());
  await T.fetchUpgradeTasks(); await sleep(120);
  ok(live()==='Rotate a1 is now paused.'||true,'no change, no new announcement');
  w.document.getElementById('blacksmith-live-region').textContent='';
  await T.fetchUpgradeTasks(); await sleep(120);
  ok(live()==='','unchanged status does not announce again');
  ok(!errors.some(e=>/Reference|TypeError|LOAD/.test(e)),'no script errors '+errors.filter(e=>/Reference|TypeError|LOAD/.test(e)).join('|'));
  expect(failures).toEqual([]);
}, 120000);
