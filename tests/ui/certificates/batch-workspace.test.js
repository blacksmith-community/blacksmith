const { test, expect } = require('bun:test');
const { main: { boot, sleep } } = require('./harness.js');
const { JSDOM } = require('./support.js');
const t = JSDOM ? test : test.skip;

async function fleet() {
  const env = boot();
  const { w, d } = env;
  w.__routes.push(
    ['/b/bosh/stemcells', (u,o,j) => j(200, [])],
    ['/b/bosh/releases', (u,o,j) => j(200, [])],
    ['/b/bosh/deployment-releases', (u,o,j) => j(200, {})],
    ['/b/upgrade/settings', (u,o,j) => j(200, {})],
    ['/b/upgrade/tasks', (u,o,j) => j(200, [])]
  );
  await sleep(30);
  w.serviceInstances = Object.fromEntries(Array.from({length: 1003}, (_,i) => [
    `instance-${String(i).padStart(4, '0')}`,
    {service_id: 'valkey', plan_id: 'cluster', name: `Cache ${i}`}
  ]));
  d.getElementById('upgrade').classList.add('active');
  await w.__T.initUpgradeTab();
  await sleep(30);
  return env;
}

t('batch selection is bounded and persists across pages and filters', async () => {
  const {w,d} = await fleet();
  try {
    const rows = () => [...d.querySelectorAll('#upgrade .upgrade-instance-item')];
    expect(rows()).toHaveLength(50);
    d.getElementById('upgrade-select-all').click();
    expect(w.__T.selectedUpgradeInstances.size).toBe(50);
    d.getElementById('batch-next').click();
    expect(rows()[0].dataset.instanceId).toBe('instance-0050');
    rows()[0].querySelector('input').click();
    expect(w.__T.selectedUpgradeInstances.size).toBe(51);
    const search = d.getElementById('batch-search');
    search.value = 'instance-100';
    search.dispatchEvent(new w.Event('input'));
    expect(rows()).toHaveLength(3);
    expect(d.getElementById('batch-selection-summary').textContent).toContain('51 hidden by filters');
    d.getElementById('batch-select-matching').click();
    expect(w.__T.selectedUpgradeInstances.size).toBe(54);
    d.getElementById('batch-view-selected').click();
    expect(rows()).toHaveLength(50);
    expect(d.getElementById('batch-search').value).toBe('');
    d.getElementById('upgrade-deselect-all').click();
    expect(w.__T.selectedUpgradeInstances.size).toBe(0);
  } finally { w.close(); }
});

t('Activity and New operation are separate views without losing choices', async () => {
  const {w,d} = await fleet();
  try {
    d.getElementById('upgrade-select-all').click();
    d.getElementById('batch-activity').click();
    expect(d.querySelector('.upgrade-main').hidden).toBe(true);
    expect(d.querySelector('.upgrade-right-panel').hidden).toBe(false);
    d.getElementById('batch-new').click();
    expect(d.querySelector('.upgrade-main').hidden).toBe(false);
    expect(d.querySelector('.upgrade-right-panel').hidden).toBe(true);
    expect(w.__T.selectedUpgradeInstances.size).toBe(50);
    expect(d.getElementById('upgrade-select-range')).toBeNull();
  } finally { w.close(); }
});

t('review paginates the fleet and hides copyable technical paths', async () => {
  const {w} = boot();
  try {
    const body = w.__T.buildPreviewBody({instances: Array.from({length:1003}, (_,i) => ({
      instance_id: String(i), instance_name: `Cache ${i}`, vms: 3,
      selected: [{name:'/secret/cache/tls_crt',days_left:1095}]
    }))}, 'redeploy');
    expect(body.querySelectorAll('.rotate-preview-card')).toHaveLength(50);
    expect(body.querySelector('.rotate-preview-summary').textContent).toContain('1003 instances');
    body.querySelector('[data-preview-next]').click();
    expect(body.querySelector('.rotate-preview-name').textContent).toBe('Cache 50');
    const path = body.querySelector('.rotate-preview-path');
    expect(path.closest('details').open).toBe(false);
    const copyPath = path.parentElement.querySelector('button');
    expect(copyPath.getAttribute('aria-label')).toBe('Copy certificate path');
    expect(copyPath.querySelector('svg')).not.toBeNull();
    expect(copyPath.textContent.trim()).toBe('');
  } finally { w.close(); }
});

t('rotation page and matching selection skip unavailable instances', async () => {
  const {w,d} = await fleet();
  try {
    const certs = Object.keys(w.serviceInstances).map((id,i) => ({
      key:`/lab/${id}/tls`,variable:'tls',kind:'leaf',days_left:100,
      owners:[{kind:'instance',instance_id:id,instance_name:`Cache ${i}`}],
      rotation:{allowed:i !== 50,reasons:i === 50 ? ['Not eligible'] : []}
    }));
    w.__routes.push(['/b/certificates/inventory',(u,o,j)=>j(200,{
      certificates:certs,capabilities:{rotation:{enabled:true}},settings:{warning_days:30,critical_days:7}
    })]);
    w.__T.renderOperation('rotate');
    await sleep(40);
    const selection = w.blacksmithCerts.selection;
    expect(d.querySelectorAll('.rotate-row')).toHaveLength(50);
    d.getElementById('upgrade-select-all').click();
    expect(selection.size).toBe(50);
    d.getElementById('batch-next').click();
    expect(d.querySelector('.rotate-row input').disabled).toBe(true);
    d.getElementById('upgrade-select-all').click();
    expect(selection.size).toBe(99);
    d.getElementById('batch-select-matching').click();
    expect(selection.size).toBe(1002);
    expect(selection.has('instance-0050')).toBe(false);
    d.getElementById('rotate-review').click();
    expect(d.querySelectorAll('#rotate-review-list li')).toHaveLength(50);
    d.querySelector('[data-selection-next]').click();
    expect(d.querySelector('#rotate-review-list li').textContent).toContain('Cache 51');
    d.getElementById('batch-eligibility').value = 'blocked';
    d.getElementById('batch-eligibility').dispatchEvent(new w.Event('change'));
    expect(d.querySelectorAll('.rotate-row')).toHaveLength(1);
    expect(d.getElementById('batch-selection-summary').textContent).toContain('1002 hidden by filters');
    d.getElementById('upgrade-deselect-all').click();
    expect(selection.size).toBe(0);
  } finally { w.close(); }
});

t('excluded review entries are bounded and remain inspectable', () => {
  const {w} = boot();
  try {
    const body = w.__T.buildPreviewBody({instances:[{instance_id:'ok',vms:1,selected:[]}].concat(
      Array.from({length:1003},(_,i)=>({instance_id:`blocked-${i}`,blocked:{message:'Busy'}})))}, 'redeploy');
    expect(body.querySelectorAll('.rotate-preview-excluded-list li')).toHaveLength(50);
    expect(body.querySelector('.rotate-preview-summary').textContent).toContain('1003 excluded');
    body.querySelector('[data-excluded-next]').click();
    expect(body.querySelector('.rotate-preview-excluded-list li').textContent).toContain('blocked-50');
  } finally { w.close(); }
});

t('opening a task or rotation selects the appropriate workspace view', async () => {
  const {w,d} = await fleet();
  try {
    w.__routes.unshift(['/b/upgrade/tasks/task-1', (u,o,j) => j(200, {id:'task-1',status:'pending',instances:[]})]);
    await w.__T.selectUpgradeTask('task-1');
    expect(d.querySelector('.upgrade-main').hidden).toBe(true);
    w.__T.renderOperation('rotate');
    expect(d.querySelector('.upgrade-main').hidden).toBe(false);
    expect(d.querySelector('.upgrade-right-panel').hidden).toBe(true);
  } finally { w.close(); }
});
