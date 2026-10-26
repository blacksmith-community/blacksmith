const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const {boot,sleep,EVIL} = H.main;
const K='/secret/dir/valkey_crt', K2='/secret/dir/redis_cluster_crt', KCA='/secret/dir/ca', KP='/secret/dir/pg_crt', KE='/secret/dir/'+EVIL;
const cert=(key,variable,kind,days,allowed,extra={})=>({key,variable,kind,days_left:days,expiry:'2027-01-01T00:00:00Z',owners:[{kind:'instance',instance_id:extra.id,instance_name:extra.name}],rotation:{allowed,reasons:allowed?[]:['Deploy is pending.']},findings:[],...extra.more});
function inventory(variant){
  const certs=[
    cert(K,'valkey_crt','leaf',10,true,{id:'inst-valkey',name:'orders-cache'}),
    cert(K2,'redis_cluster_crt','leaf',200,true,{id:'inst-valkey',name:'orders-cache'}),
    cert(KCA,'ca','ca',900,false,{id:'inst-valkey',name:'orders-cache'}),
    cert(KP,'pg_crt','leaf',5,false,{id:'inst-pg',name:'billing-db',more:{findings:[{code:'deploy_pending'}]}}),
    cert(KE,EVIL,'leaf',3,true,{id:EVIL,name:EVIL}),
  ];
  if(variant==='ineligible') certs[0].rotation={allowed:false,reasons:['Rotation is blocked now.']};
  return {certificates:certs,capabilities:{rotation:{enabled:true}},settings:{warning_days:30,critical_days:7}};
}
const visible=(n)=>{ for(let x=n;x;x=x.parentElement){ if(x.hidden) return false;} return true; };
t('rotate selection on Batch Operations', async () => {
  const env=boot(); const {w,d,errors,alerts,calls}=env; const T=w.__T;
  let inv=inventory(); let tasks=[]; let detail={};
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],
    ['/b/upgrade/settings',(u,o,j)=>j(200,{})],['/b/certificates/inventory',(u,o,j)=>j(200,inv)],
    [/\/b\/upgrade\/tasks\/[^/]+$/,(u,o,j)=>{const id=decodeURIComponent(u.split('/').pop()); return detail[id]?j(200,detail[id]):j(404,{error:'nf',code:'not_found'});}],
    [/\/b\/upgrade\/tasks$/,(u,o,j)=>j(200,tasks)]);
  w.plansData={services:[{id:'svc1',name:'valkey'},{id:EVIL,name:EVIL}]};
  w.serviceInstances={
    'inst-valkey':{service_id:'svc1',plan_id:'p1',plan:{id:'p1',name:'small'}},
    'inst-pg':{service_id:'svc1',plan_id:'p1',plan:{id:'p1',name:'small'}},
    'inst-held':{service_id:'svc1',plan_id:'p1',plan:{id:'p1',name:'small'}},
    [EVIL]:{service_id:EVIL,plan_id:EVIL,plan:{id:EVIL,name:EVIL},stemcell:{os:EVIL,version:EVIL,name:EVIL}}
  };
  await sleep(50);
  ok(!errors.some(e=>/LOAD ERROR|ReferenceError|TypeError/.test(e)),'loads with no throws');
  tasks=[{id:'t1',name:'Spring',kind:'certificates',status:'running'}];
  detail.t1={id:'t1',name:'Spring',kind:'certificates',status:'running',instances:[{instance_id:'inst-held',status:'running'},{instance_id:'inst-pg',status:'success'}]};
  w.plansData={services:[{id:'svc1',name:'valkey'},{id:EVIL,name:EVIL}]}; d.getElementById('upgrade').classList.add('active'); await T.initUpgradeTab(); await sleep(150);
  ok(!!d.getElementById('rotate-operation'),'operation fieldset installed');
  const radios=[...d.querySelectorAll('input[name="upgrade-operation"]')];
  ok(radios.length===4&&radios.map(r=>r.value).join()==='upgrade,rotate,converge,command'&&radios[0].checked,'four radios, upgrade default');
  const main=d.querySelector('#upgrade .upgrade-main');
  ok(main.firstElementChild.id==='rotate-operation'||main.firstElementChild.nextElementSibling===d.querySelector('.upgrade-top')||d.getElementById('rotate-operation').nextElementSibling.classList.contains('upgrade-top'),'selector sits above .upgrade-top');
  // upgrade mode rows: hostile + held
  const list=d.querySelector('#upgrade .upgrade-instances');
  ok(list.querySelectorAll('img,script').length===0&&list.textContent.includes(EVIL),'upgrade rows: hostile strings inert and literal');
  const held=list.querySelector('[data-instance-id="inst-held"]');
  ok(held&&held.dataset.held==='true'&&held.textContent.includes('Busy: rotation "Spring"'),'upgrade: held row shows its holder');
  d.getElementById('upgrade-select-all').click();
  ok(!T.selectedUpgradeInstances.has('inst-held')&&T.selectedUpgradeInstances.has('inst-valkey'),'Select All skips the held row');
  held.click(); ok(!T.selectedUpgradeInstances.has('inst-held'),'click on held row does not select');
  ok(d.getElementById('upgrade-start').textContent==='Upgrade Selected (3)','upgrade button counts 3 (non-held)');
  d.getElementById('upgrade-deselect-all').click();
  ok(T.selectedUpgradeInstances.size===0,'deselect all clears');

  // enter rotate mode, no selection
  radios[1].checked=true; radios[1].dispatchEvent(new w.Event('change',{bubbles:true})); await sleep(150);
  ok(d.getElementById('upgrade').dataset.operation==='rotate','rotate mode active (no confirm needed)');
  ok(!d.querySelector('.dialog-modal-overlay.active,#dialog-modal-overlay.active'),'no dialog with empty selection');
  const startBtn=d.getElementById('upgrade-start');
  ok(startBtn.textContent==='Upgrade Selected (0)'&&!visible(startBtn),'#upgrade-start hidden, text unchanged');
  ok(d.getElementById('rotate-preview')&&!visible(d.getElementById('rotate-preview')),'legacy action stays hidden behind step navigation');
  T.setBatchStep(1,false);
  ok(d.querySelector('.upgrade-targets-panel h2').textContent==='Certificate options','targets heading retitled');
  ok(!d.getElementById('rotate-threshold')&&!!d.getElementById('batch-expiring'),'expiry filter sits with the instance filters, not Options');
  const sel=w.blacksmithCerts.selection;
  ok(sel.size===0,'nothing is preselected');
  sel.set('inst-valkey',new Set([K])); T.renderRotateInstances(); T.updateOperationFooter();
  const chipOf=(id,key)=>[...d.querySelectorAll('.rotate-row')].filter(r=>r.dataset.instanceId===id).flatMap(r=>[...r.querySelectorAll('.rotate-chip')]).find(c=>c.dataset.variable===key);
  const c1=chipOf('inst-valkey',K), c2=chipOf('inst-valkey',K2);
  ok(c1&&c1.getAttribute('aria-pressed')==='true'&&c2.getAttribute('aria-pressed')==='false','chips reflect selection via aria-pressed');
  ok(c1.getAttribute('aria-label')==='Rotate valkey_crt on orders-cache','chip accessible name');
  ok(c1.tagName==='BUTTON'&&c1.type==='button','chip is a button');
  const rowCb=()=>d.querySelector('[data-instance-id="inst-valkey"] input[type=checkbox]');
  ok(rowCb().indeterminate===true&&rowCb().checked===false,'row checkbox mixed with one of two chips');
  // chip click does not toggle the row
  chipOf('inst-valkey',K2).click();
  ok(sel.get('inst-valkey').has(K)&&sel.get('inst-valkey').has(K2),'chip click toggles only that chip (row not toggled)');
  ok(rowCb().checked===true&&rowCb().indeterminate===false,'row checkbox checked with both chips');
  chipOf('inst-valkey',K).click();
  ok(!sel.get('inst-valkey').has(K)&&sel.get('inst-valkey').has(K2),'chip click clears just that chip');
  // clear stays clear across polls
  chipOf('inst-valkey',K2).click();
  for(let i=0;i<2;i++){ await T.fetchUpgradeTasks(); await T.loadRotateInventory({silent:true}); await sleep(20); }
  ok(!sel.has('inst-valkey'),'cleared chips stay clear across two polls and inventory refreshes');
  // row click toggles all
  d.querySelector('[data-instance-id="inst-valkey"] .upgrade-instance-id').click();
  ok(sel.get('inst-valkey')&&sel.get('inst-valkey').size===2,'row click selects every eligible chip');
  d.querySelector('[data-instance-id="inst-valkey"] .upgrade-instance-id').click();
  ok(!sel.has('inst-valkey'),'row click again clears them');
  // disabled rows show their reason
  const pgRow=d.querySelector('[data-instance-id="inst-pg"]');
  ok(pgRow.querySelector('input').disabled&&pgRow.textContent.includes('Deploy is pending.'),'ineligible row disabled with reason text');
  const heldRow=d.querySelector('[data-instance-id="inst-held"]');
  ok(heldRow.querySelector('input').disabled&&heldRow.textContent.includes('Busy: rotation "Spring"'),'held row disabled with holder');
  const noInv=d.querySelector('[data-instance-id="inst-held"]'); 
  // hostile
  const evilRow=d.querySelector('#upgrade .upgrade-instances-list');
  ok(evilRow.querySelectorAll('img,script').length===0&&evilRow.textContent.includes(EVIL),'rotate rows: hostile strings inert and literal');
  ok(chipOf(EVIL,KE)&&chipOf(EVIL,KE).getAttribute('aria-label').includes(EVIL),'hostile chip label intact as text');
  // bulk
  d.getElementById('rotate-select-visible').click();
  ok(sel.get('inst-valkey').size===2&&sel.has(EVIL)&&!sel.has('inst-held')&&!sel.has('inst-pg'),'Select visible takes eligible chips, skips held and ineligible');
  d.getElementById('rotate-clear-visible').click();
  ok(sel.size===0,'Clear visible clears them');
  d.getElementById('batch-select-matching').click();
  ok(sel.get('inst-valkey').size===2,'Select all matching selects both');
  const line=d.getElementById('rotate-selection-line').textContent;
  ok(/3 certificates selected on 2 instances/.test(line),'selection line counts: '+line);
  ok(d.getElementById('rotate-preview').textContent==='Preview rotation of 3 certificates on 2 instances','footer label');
  ok(d.getElementById('rotate-preview').id.startsWith('rotate-'),'footer id starts with rotate-');
  // service filter hides
  const sf=d.getElementById('upgrade-service-filter'); sf.value='svc1'; sf.dispatchEvent(new w.Event('change'));
  await sleep(20);
  ok(sel.get('inst-valkey').size===2&&sel.has(EVIL),'service filter change keeps the shared selection');
  ok(/hidden by the filters/.test(d.getElementById('rotate-selection-line').textContent),'selection line reports pairs hidden by filters');
  sf.value=''; sf.dispatchEvent(new w.Event('change')); await sleep(20);
  // review
  d.getElementById('rotate-review').click();
  const rv=d.getElementById('rotate-review-list');
  ok(!rv.hidden&&rv.querySelectorAll('li').length===3&&rv.querySelectorAll('img,script').length===0&&rv.textContent.includes(EVIL),'review lists each pair, hostile inert');
  ok(d.getElementById('rotate-review').getAttribute('aria-expanded')==='true','review toggle aria-expanded');
  // become ineligible
  inv=inventory('ineligible'); await T.loadRotateInventory({silent:true}); await sleep(20);
  ok(d.getElementById('rotate-review-list').textContent.includes('no longer eligible')&&sel.get('inst-valkey').has(K),'ineligible pair stays in review as no longer eligible');
  ok(/1 no longer eligible/.test(d.getElementById('rotate-selection-line').textContent),'selection line counts stale');
  d.querySelector('#rotate-review-list li.is-stale button').click();
  ok(!sel.get('inst-valkey').has(K),'Remove drops the pair');
  inv=inventory();
  // inventory failure
  w.__routes.unshift(['/b/certificates/inventory',(u,o,j)=>j(503,{success:false,error:'CredHub is down.',code:'credhub_unavailable'})]);
  await T.loadRotateInventory({silent:true}); await sleep(20);
  ok(d.getElementById('rotate-notice').textContent.includes('could not be refreshed')&&!!d.getElementById('rotate-retry'),'silent refresh failure keeps list and shows Retry');
  w.__routes.shift();
  d.getElementById('rotate-retry').click(); await sleep(50);
  ok(!d.getElementById('rotate-retry'),'Retry reloads and clears the note');
  // converge
  sel.clear();
  d.getElementById('rotate-op-converge').checked=true; d.getElementById('rotate-op-converge').dispatchEvent(new w.Event('change',{bubbles:true})); await sleep(80);
  ok(d.getElementById('upgrade').dataset.operation==='converge','converge mode');
  const rows=[...d.querySelectorAll('#upgrade .upgrade-instance-item')].map(r=>r.dataset.instanceId);
  ok(rows.join()==='inst-pg','converge lists only the instance waiting for a deploy: '+rows);
  ok(d.querySelectorAll('.rotate-chip').length===0,'converge rows have no chips');
  d.querySelector('[data-instance-id="inst-pg"] input').click(); await sleep(10);
  ok(d.getElementById('rotate-preview').textContent==='Preview converge of 1 instance','converge footer label: '+d.getElementById('rotate-preview').textContent);
  // Changing operation preserves each selection draft and does not ask to discard it.
  await T.requestOperation('upgrade');
  ok(d.getElementById('upgrade').dataset.operation==='upgrade','switches to upgrade without destructive confirmation');
  await T.requestOperation('converge');
  ok(d.getElementById('rotate-preview').textContent==='Preview converge of 1 instance','converge draft remains selected');
  await T.requestOperation('rotate');
  sel.clear(); sel.set('inst-valkey',new Set([K]));
  await T.requestOperation('upgrade');
  ok(sel.get('inst-valkey').has(K),'rotate draft remains selected after changing operation');
  T.setBatchStep(1,false);
  ok(visible(d.getElementById('upgrade-stemcell-filter'))&&visible(d.getElementById('upgrade-release-filter')),'options step restores upgrade controls');
  ok(!visible(d.getElementById('upgrade-start')),'legacy upgrade action remains hidden');
  ok(!d.getElementById('rotate-preview')&&!d.getElementById('rotate-threshold'),'rotate controls removed');
  ok(d.querySelector('.upgrade-targets-panel h2').textContent==='Upgrade targets','heading restored without step numbers');
  // ids
  const ids=[...d.querySelectorAll('[id^="upgrade-advrel-"],[id^="upgrade-release-"]')].map(n=>n.id).sort();
  ok(ids.join()==='upgrade-release-filter,upgrade-release-floor','advrel/release id query returns only pre-existing controls: '+ids);
  ok([...d.querySelectorAll('#upgrade [id^="rotate-"]')].length>0&&![...d.querySelectorAll('#upgrade [id]')].some(n=>/^rotate-/.test(n.id)===false&&false),'rotate ids exist');
  ok(alerts.length===0,'no alert/confirm/prompt: '+alerts.join('|'));
  ok(!errors.some(e=>/Reference|TypeError|LOAD/.test(e)),'no script errors: '+errors.join('|'));
  expect(failures).toEqual([]);
}, 120000);
