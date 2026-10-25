const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');

const t = JSDOM ? test : test.skip;
const {boot,sleep} = H.main;

const inst={service_id:'svc1',plan_id:'p1',plan:{id:'p1',name:'small'}};
const mk=(id,key,variable,instId,name,days,exp)=>({id,key,variable,kind:'leaf',days_left:days,expiry:exp,health:'ok',owners:[{kind:'instance',instance_id:instId,instance_name:name}],rotation:{allowed:true,modes:['redeploy','patch'],reasons:[]},findings:[]});

async function setup(){
  const env=boot(); const {w,d}=env;
  const certs=[mk('c1','/s/a_crt','a_crt','inst-a','cache-a',5,'critical'),mk('c2','/s/b_crt','b_crt','inst-b','cache-b',200,'ok'),mk('c3','/s/c_crt','c_crt','inst-c','cache-c',300,'ok')];
  w.__routes.push(['/b/bosh/stemcells',(u,o,j)=>j(200,[])],['/b/bosh/releases',(u,o,j)=>j(200,[])],['/b/bosh/deployment-releases',(u,o,j)=>j(200,{})],['/b/upgrade/settings',(u,o,j)=>j(200,{})],
    [/certificates\/inventory/,(u,o,j)=>j(200,{certificates:certs,capabilities:{rotation:{enabled:true},patch_in_place:{enabled:true}},settings:{warning_days:30,critical_days:7},scan:{state:'idle',completed_at:'2026-10-21T10:00:00Z'},summary:{certificates:3,needs_attention:0},filtered:{certificates:3,affected_instances:3}})],
    [/certificates\/items\//,(u,o,j)=>j(200,{...certs[0],pem:''})],
    [/\/b\/upgrade\/tasks$/,(u,o,j)=>j(200,[])]);
  w.plansData={services:[{id:'svc1',name:'valkey'}]};
  w.serviceInstances={'inst-a':inst,'inst-b':inst,'inst-c':inst};
  await sleep(50);
  d.getElementById('upgrade').classList.add('active');
  await w.__T.initUpgradeTab();
  d.getElementById('certificates').classList.add('active');
  await w.__T.CertsFleet.init(); await sleep(150);
  return {...env,T:w.__T};
}
const box=(d,id)=>d.querySelector(`[data-instance-id="${id}"] input[type=checkbox]`);
const row=(d,id)=>d.querySelector(`[data-instance-id="${id}"]`);

// Every assertion about a handed-over instance, so a drawer handover and a
// Rotate selected handover are held to the same standard.
function expectSelected(d,w,ids){
  ids.forEach((id)=>{
    expect(box(d,id).checked).toBe(true);
    expect(row(d,id).classList.contains('selected')).toBe(true);
  });
  expect(d.getElementById('batch-flow-context').textContent).toBe(`${ids.length} ${ids.length===1?'instance':'instances'} selected from the Certificates tab`);
  expect([...w.blacksmithCerts.selection.keys()].sort()).toEqual(ids);
}

t('Rotate in the details panel opens the batch flow with the instance selected', async () => {
  const {w,d,T}=await setup();
  try {
    d.querySelector('.cert-row').click(); await sleep(150);
    d.querySelector('.cert-drawer-rotate').click(); await sleep(200);
    expect(d.querySelector('.cert-rotate-dialog')).toBeNull();
    expect(d.getElementById('upgrade').dataset.operation).toBe('rotate');
    expect(T.batchFlow.step).toBe(2);
    expect(d.querySelector('.batch-steps')).not.toBeNull();
    expectSelected(d,w,['inst-a']);
    expect(box(d,'inst-b').checked).toBe(false);
    expect(row(d,'inst-b').classList.contains('selected')).toBe(false);
    // the operator can still change the selection
    d.querySelector('[data-instance-id="inst-b"] .upgrade-instance-id').click();
    expect(box(d,'inst-b').checked).toBe(true);
    expect(d.getElementById('batch-flow-context').textContent).toBe('2 instances selected');
  } finally { w.close(); }
});

t('Rotate selected carries every selected certificate the same way', async () => {
  const {w,d}=await setup();
  try {
    const boxes=[...d.querySelectorAll('.cert-row input[type=checkbox]')];
    boxes[0].click(); boxes[2].click(); await sleep(30);
    d.querySelector('.cert-rotate-selected').click(); await sleep(200);
    expect(d.querySelector('.cert-rotate-dialog')).toBeNull();
    expectSelected(d,w,['inst-a','inst-c']);
    expect(box(d,'inst-b').checked).toBe(false);
  } finally { w.close(); }
});

t('a handover uses the batch flow mode tabs and default', async () => {
  const {w,d,T}=await setup();
  try {
    d.querySelector('.cert-row').click(); await sleep(150);
    d.querySelector('.cert-drawer-rotate').click(); await sleep(200);
    T.setBatchStep(1,false);
    const root=d.getElementById('rotate-mode');
    expect(root).not.toBeNull();
    expect(d.getElementById('rotate-mode-redeploy-tab').getAttribute('aria-selected')).toBe('true');
    expect(d.getElementById('rotate-mode-patch-tab').getAttribute('aria-selected')).toBe('false');
    expect(T.rotateState.mode).toBe('redeploy');
    expect(d.querySelector('.cert-rotate-mode')).toBeNull();
  } finally { w.close(); }
});

t('a handover resets the expiry filter to Any so it hides nothing', async () => {
  const {w,d,T}=await setup();
  try {
    // Narrow the list first, as an operator might have before leaving.
    await T.requestOperation('rotate'); await sleep(80);
    const filter=d.getElementById('batch-expiring');
    filter.value='7'; filter.dispatchEvent(new w.Event('change',{bubbles:true})); await sleep(30);
    expect(row(d,'inst-b')).toBeNull();
    const boxes=[...d.querySelectorAll('.cert-row input[type=checkbox]')];
    boxes[1].click(); await sleep(30);
    d.querySelector('.cert-rotate-selected').click(); await sleep(200);
    expect(d.getElementById('batch-expiring').value).toBe('');
    expect(row(d,'inst-b')).not.toBeNull();
    expect(box(d,'inst-b').checked).toBe(true);
  } finally { w.close(); }
});
