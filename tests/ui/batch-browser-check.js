// Run with playwright-cli run-code --filename=tests/ui/batch-browser-check.js.
// Serve ui on 4201. All backend traffic is mocked; no infrastructure is changed.
async (page) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const check = (value, label) => { if (!value) throw new Error(label); };
  const instances = Object.fromEntries(Array.from({length: 1003}, (_,i) => [
    `cache-${String(i).padStart(4, '0')}`,
    {service_id: 'valkey', plan_id: 'cluster', instance_name: `Application cache ${i}`, vm_count: 6,
      stemcell: {os:'ubuntu-jammy',version:'1.80'}, deployment_name:`cluster-cache-${i}`}
  ]));
  const certificates = Object.keys(instances).map((id, i) => ({key:`/secret/lab/${id}/tls_crt`,variable:'tls_crt',kind:'leaf',days_left:i === 0 ? 10 : 1095,
    owners:[{kind:'instance',instance_id:id,instance_name:`Application cache ${i}`}], rotation:{allowed:true,modes:['redeploy']},findings:[]}));
  await page.unrouteAll();
  await page.route('**/v2/catalog', r => r.fulfill({json:{services:[{id:'valkey',name:'Valkey',plans:[{id:'cluster',name:'cluster'}]}]}}));
  await page.route('**/b/**', async r => {
    const path = new URL(r.request().url()).pathname;
    let json = {};
    if (path === '/b/status') json = {env:'Lab preview fixture',deployment:'blacksmith-preview',instances,plans:{'valkey/cluster':{}}};
    else if (path === '/b/certificates/inventory') json = {certificates,capabilities:{rotation:{enabled:true}},settings:{warning_days:30,critical_days:7}};
    else if (path === '/b/certificates/rotation/preview') {
      const body = r.request().postDataJSON();
      json = {preview_id:'fixture-preview',instances:body.selections.map(s=>({instance_id:s.instance_id,instance_name:instances[s.instance_id].instance_name,
        service:'Valkey',plan:'cluster',vms:6,selected:s.variables.map(name=>({name,days_left:1095}))}))};
    } else if (r.request().method() !== 'GET') return r.fulfill({status:403,json:{error:'Writes blocked in browser check'}});
    else if (path.endsWith('/stemcells')) json = [{operating_system:'ubuntu-jammy',version:'1.90'}];
    else if (path.endsWith('/releases')) json = [];
    else if (path.endsWith('/tasks') || path.endsWith('/events') || path.endsWith('/vms') || path.endsWith('/endpoints') || path.endsWith('/registrations')) json = [];
    else if (path.includes('ui-terminal-status')) json = {enabled:false};
    await r.fulfill({json});
  });
  await page.emulateMedia({reducedMotion:'reduce'});
  await page.setViewportSize({width:1440,height:900});
  await page.goto('http://127.0.0.1:4201');
  await page.locator('[data-tab="upgrade"]').click();
  await page.locator('#batch-search').waitFor();
  await page.locator('#rotate-op-rotate').check();
  await page.locator('.rotate-chip').first().waitFor();
  check(await page.locator('#upgrade .upgrade-instance-item').count() === 50, 'bounded rotate page');
  await page.locator('#batch-options-summary').click();
  await page.screenshot({animations:'disabled',path:'output/playwright/batch-desktop.png'});
  const noNestedScroll = () => page.locator('#upgrade').evaluate(panel => {
    const selectors=['.upgrade-services','.upgrade-targets','.upgrade-instances','.upgrade-instances-list'];
    return selectors.every(s => {const e=panel.querySelector(s);return !/(auto|scroll)/.test(getComputedStyle(e).overflowY);});
  });
  check(await noNestedScroll(), 'no nested configuration or instance scrollers');
  check(await page.locator('.upgrade-services-panel').evaluate(e => e.getBoundingClientRect().width > e.parentElement.getBoundingClientRect().width * 0.95), 'filters use the full workspace width');
  check(await page.locator('.upgrade-instances').evaluate(e => getComputedStyle(e).maxHeight === 'none'), 'instance list has no height cap');
  await page.locator('#upgrade-select-all').click();
  await page.locator('#batch-next').click();
  check((await page.locator('#batch-selection-summary').textContent()).includes('50 instances selected'), 'page keeps selection');
  await page.locator('#batch-search').fill('cache-100');
  check(await page.locator('#upgrade .upgrade-instance-item').count() === 3, 'search narrows fleet');
  await page.locator('#batch-select-matching').click();
  check((await page.locator('#batch-selection-summary').textContent()).includes('53 instances selected'), 'all matching adds exact scope');
  await page.locator('#rotate-preview').click();
  await page.locator('#dialog-modal-overlay.active .batch-review-dialog').waitFor();
  await page.locator('[data-dialog-role="cancel"]').waitFor({state:'visible'});
  check(await page.locator('.rotate-preview-card').count() === 50, 'bounded review');
  await page.screenshot({animations:'disabled',path:'output/playwright/batch-review.png'});
  await page.locator('[data-dialog-role="cancel"]').click();
  await page.locator('#batch-activity').click();
  check(!await page.locator('.upgrade-main').isVisible(), 'activity separated');
  await page.locator('#batch-new').click();
  await page.setViewportSize({width:390,height:844});
  await page.locator('#batch-search').fill('');
  await page.screenshot({animations:'disabled',path:'output/playwright/batch-mobile.png'});
  check(await noNestedScroll(), 'no mobile nested scrollers');
  check(await page.evaluate(()=>document.documentElement.scrollWidth <= innerWidth), 'no horizontal overflow');
  await page.locator('#rotate-preview').click();
  await page.locator('#dialog-modal-overlay.active .batch-review-dialog').waitFor();
  await page.locator('[data-dialog-role="cancel"]').waitFor({state:'visible'});
  check(await page.locator('[data-dialog-role="cancel"]').isVisible(), 'review back action visible on mobile');
  await page.screenshot({animations:'disabled',path:'output/playwright/batch-review-mobile.png'});
  await page.locator('[data-dialog-role="cancel"]').click();
  check(errors.length === 0, errors.join('\n'));
  return {rows:1003,pageSize:50,checks:'passed',errors};
}
