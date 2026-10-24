// Run with playwright-cli run-code --filename=tests/ui/batch-flow-browser-check.js.
// Serve ui on 4202. All backend traffic is mocked; no infrastructure is changed.
async (page) => {
  const errors = []; const creates=[]; let previews=0; let delayPreview=0;
  page.on('pageerror', error => errors.push(error.message));
  const check = (value, label) => { if (!value) throw new Error(label); };
  const instances = Object.fromEntries(Array.from({length: 1003}, (_,i) => [
    `cache-${String(i).padStart(4, '0')}`,
    {service_id: 'valkey', plan_id: 'cluster', instance_name: `Application cache ${i}`, vm_count: 6,
      stemcell: {os:'ubuntu-jammy',version:'1.80'}, deployment_name:`cluster-cache-${i}`}
  ]));
  const certificates = Object.keys(instances).map((id, i) => ({key:`/secret/lab/${id}/tls_crt`,variable:'tls_crt',kind:'leaf',days_left:i === 0 ? 10 : 1095,
    owners:[{kind:'instance',instance_id:id,instance_name:`Application cache ${i}`}], rotation:{allowed:true,modes:['redeploy','patch']},divergence:{state:'unconverged'},findings:[]}));
  await page.unrouteAll();
  await page.route('**/v2/catalog', r => r.fulfill({json:{services:[{id:'valkey',name:'Valkey',plans:[{id:'cluster',name:'cluster'}]}]}}));
  await page.route('**/b/**', async r => {
    const path = new URL(r.request().url()).pathname;
    let json = {};
    if (path === '/b/status') json = {env:'Lab preview fixture',deployment:'blacksmith-preview',instances,plans:{'valkey/cluster':{}}};
    else if (path === '/b/certificates/inventory') json = {certificates,capabilities:{rotation:{enabled:true},patch_in_place:{enabled:true}},settings:{warning_days:30,critical_days:7}};
    else if (path === '/b/certificates/rotation/preview') {
      previews++; if(delayPreview) await new Promise(resolve=>setTimeout(resolve,delayPreview));
      const body = r.request().postDataJSON();
      json = {preview_id:'fixture-preview',instances:body.selections.map(s=>({instance_id:s.instance_id,instance_name:instances[s.instance_id].instance_name,
        service:'Valkey',plan:'cluster',vms:6,selected:s.variables.map(name=>({name,days_left:1095}))}))};
    } else if(path === '/b/upgrade/preview') json={id:'upgrade-preview',diverged:[]};
    else if(path === '/b/upgrade/tasks' && r.request().method()==='POST') {creates.push(r.request().postDataJSON());json={id:'fixture-task'};}
    else if (r.request().method() !== 'GET') return r.fulfill({status:403,json:{error:'Writes blocked in browser check'}});
    else if (path.endsWith('/stemcells')) json = [{operating_system:'ubuntu-jammy',version:'1.90'}];
    else if (path.endsWith('/releases')) json = [];
    else if (path.endsWith('/tasks') || path.endsWith('/events') || path.endsWith('/vms') || path.endsWith('/endpoints') || path.endsWith('/registrations')) json = [];
    else if (path.includes('ui-terminal-status')) json = {enabled:false};
    await r.fulfill({json});
  });
  await page.emulateMedia({reducedMotion:'reduce'});
  await page.setViewportSize({width:1440,height:900});
  await page.goto('http://127.0.0.1:4202');

  await page.locator('.tabs [data-tab="upgrade"]').click();
  const next=page.locator('#batch-flow-next');
  const back=page.locator('#batch-flow-back');
  const crumb=n=>page.locator(`[data-batch-step="${n}"]`);
  await next.waitFor();
  check(await page.locator('#rotate-operation').isVisible(),'operation is initial focus');
  check(!await page.locator('.upgrade-services-panel').isVisible(),'selection hidden initially');
  check(await page.locator('#rotate-op-upgrade').isChecked(),'upgrade selected initially');
  check(await page.locator('.rotate-operation-option.is-selected').count()===1,'one selected operation card');
  await page.screenshot({path:'output/playwright/batch-flow-operation.png'});
  await page.locator('#rotate-op-rotate').check();await next.click();
  await page.locator('#rotate-mode-redeploy-tab').waitFor();
  await page.waitForFunction(()=>!document.getElementById('batch-flow-next').disabled);
  check(!await page.locator('.upgrade-instances').isVisible(),'only options shown');
  await page.locator('#rotate-mode-redeploy-tab').focus();
  await page.locator('#rotate-mode-redeploy-tab').press('ArrowRight');
  check(await page.locator('#rotate-mode-patch-tab').getAttribute('aria-selected')==='true','patch tab selected');
  check(await next.isDisabled(),'patch requires follow-up choice');
  await page.locator('#rotate-mode-converge-pending').check();
  check(!await next.isDisabled(),'follow-up enables continuation');
  check(await page.locator('.rotate-converge-option').first().evaluate(el=>getComputedStyle(el).display)==='grid','follow-up descriptions use separate grid rows');
  await page.screenshot({path:'output/playwright/batch-flow-patch-options.png'});
  await page.locator('#rotate-mode-redeploy-tab').click();
  await page.locator('#upgrade').evaluate(el=>el.scrollTop=0);
  await page.screenshot({path:'output/playwright/batch-flow-options.png'});
  await next.click();await page.locator('.rotate-chip').first().waitFor();
  const firstRow=page.locator('.upgrade-instance-item').nth(1);
  await firstRow.locator('.batch-select-state').click();
  check(await firstRow.locator('input[type=checkbox]').isChecked(),'visible select control tracks actual state');
  check(await firstRow.locator('.select-on').isVisible(),'selected text is visible');
  check(await page.locator('.upgrade-instance-row-number').count()===0,'row ordinals removed');
  await page.locator('#upgrade-select-all').click();
  check(await page.locator('.upgrade-instance-item').count()===50,'instance page bounded');
  await page.locator('#batch-search').fill('Application cache 1002');
  await crumb(1).click();await next.click();
  check(await page.locator('#batch-search').inputValue()==='Application cache 1002','back preserves filters');
  check((await page.locator('#batch-flow-context').textContent()).includes('50'),'back preserves selection');
  await page.locator('#batch-activity').click();await page.locator('#batch-new').click();
  check(await page.locator('.upgrade-instances').isVisible(),'Activity returns to current step');
  await next.click();await page.locator('#batch-flow-ack').waitFor();
  check(creates.length===0,'review never creates a task');
  check(!await page.locator('#dialog-modal-overlay.active').count(),'review is inline');
  check(await next.isDisabled(),'large rotation requires typed confirmation');
  await page.screenshot({path:'output/playwright/batch-flow-review.png'});
  const firstPreviewCount=previews;
  await back.click();await next.click();await page.locator('#batch-flow-ack').waitFor();
  check(previews>firstPreviewCount,'returning to review refreshes server preview');
  await page.locator('#batch-flow-ack').fill('rotate');
  await next.evaluate(button=>{button.click();button.click();});await page.waitForFunction(()=>!document.querySelector('.upgrade-right-panel').hidden);
  check(creates.length===1,'one confirmed task created');
  await page.locator('#batch-new').click();await crumb(0).click();
  await page.locator('#rotate-op-upgrade').check();await next.click();
  check(await next.isDisabled(),'upgrade needs target');
  await page.locator('#upgrade-stemcell-filter').selectOption('ubuntu-jammy/1.90');
  await next.click();await page.locator('#upgrade-select-all').click();await next.click();
  await page.locator('#batch-flow-job-name').waitFor();
  check(creates.length===1,'upgrade waits for review confirmation');
  await page.locator('#batch-flow-job-name').fill('Lab rollout');
  await back.click();await next.click();await page.locator('#batch-flow-job-name').waitFor();
  check(await page.locator('#batch-flow-job-name').inputValue()==='Lab rollout','review draft job name retained');
  await next.click();await page.waitForFunction(()=>!document.querySelector('.upgrade-right-panel').hidden);
  check(creates.length===2 && creates[1].name==='Lab rollout','upgrade submits reviewed scope and job name');
  await page.locator('#batch-new').click();await crumb(0).click();
  await page.locator('#rotate-op-converge').check();await next.click();await next.click();
  await page.locator('#upgrade-select-all').click();await next.click();await page.locator('#batch-flow-review > h3').first().waitFor();
  check(creates.length===2,'converge also waits for confirmation');
  await back.click();await crumb(0).click();
  await page.setViewportSize({width:390,height:844});
  await page.screenshot({path:'output/playwright/batch-flow-mobile.png'});
  check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'no mobile horizontal overflow');
  await page.locator('#rotate-op-rotate').check();await next.click();await next.click();
  await page.locator('#upgrade-select-all').click();
  delayPreview=250;await next.click();await back.click();
  await page.waitForTimeout(350);
  check(await page.locator('.upgrade-instances').isVisible(),'late preview cannot replace edited step');
  check(creates.length===2,'cancelling slow preview makes no write');
  check(errors.length===0,errors.join('\n'));
  return {flow:'passed',instances:1003,creates:creates.length,previews,errors};
}
