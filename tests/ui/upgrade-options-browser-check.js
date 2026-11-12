// Run with playwright-cli run-code --filename=tests/ui/upgrade-options-browser-check.js.
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
    else if (path.endsWith('/releases')) json = Array.from({length:153},(_,i)=>({name:`release-${String(i).padStart(3,'0')}`,release_versions:[{version:'2.0'},{version:'1.0'}]}));
    else if (path.endsWith('/tasks') || path.endsWith('/events') || path.endsWith('/vms') || path.endsWith('/endpoints') || path.endsWith('/registrations')) json = [];
    else if (path.includes('ui-terminal-status')) json = {enabled:false};
    await r.fulfill({json});
  });
  await page.emulateMedia({reducedMotion:'reduce'});
  await page.setViewportSize({width:1440,height:900});
  await page.goto('http://127.0.0.1:4202');

  await page.locator('#rail-area-upgrade').click();await page.locator('#batch-new').click();
  await page.locator('#batch-flow-next').click();
  const opener=page.locator('#batch-edit-releases');
  await page.waitForFunction(()=>!document.getElementById('batch-edit-releases').disabled);
  check(await page.locator('.batch-upgrade-options-table tbody th').allTextContents().then(v=>v.join(','))==='System,Releases','compact table rows');
  check(await page.locator('.batch-upgrade-options-table th.sortable').count()===0,'form rows are not sortable');
  await page.screenshot({path:'output/playwright/upgrade-options-table.png'});
  await opener.click();
  const picker=page.locator('.batch-release-picker');
  const search=page.locator('#batch-release-search');
  check(await picker.locator('tbody tr').count()===20,'bounded release page');
  await search.fill('release-149');
  const target=picker.locator('select');await target.selectOption('2.0');
  check(await page.locator('#upgrade-advrel-release-149').inputValue()==='','draft does not leak');
  await page.locator('[data-dialog-role="cancel"]').click();
  check(await page.locator('#upgrade-advrel-release-149').inputValue()==='','cancel preserves saved choices');
  check(await opener.evaluate(el=>el===document.activeElement),'focus restored after cancel');
  await opener.click();await search.fill('release-149');await picker.locator('select').selectOption('2.0');
  await search.fill('');await page.locator('#batch-release-selected-only').check();
  check(await picker.locator('tbody tr').count()===1,'selected-only narrows draft');
  check(await picker.locator('select').inputValue()==='2.0','search and filter preserve draft');
  await page.locator('[data-dialog-role="confirm"]').click();
  check(await page.locator('#upgrade-advrel-release-149').inputValue()==='2.0','Apply commits chosen version');
  check((await page.locator('#batch-release-summary').textContent()).includes('release-149'),'summary shows selected release');
  check(!await page.locator('#batch-flow-next').isDisabled(),'advanced target enables next step');
  await page.locator('#batch-flow-back').click();await page.locator('#batch-flow-next').click();
  check((await page.locator('#batch-release-summary').textContent()).includes('release-149'),'navigation preserves target');
  await opener.click();await search.fill('release-149');await picker.locator('select').selectOption('1.0');await page.keyboard.press('Escape');
  check(await page.locator('#upgrade-advrel-release-149').inputValue()==='2.0','Escape cancels edits');
  await page.locator('#batch-release-summary button').click();
  check(await page.locator('#upgrade-advrel-release-149').inputValue()==='','remove clears target');
  check(await page.locator('#batch-flow-next').isDisabled(),'removing last target blocks next');
  await opener.click();await search.fill('does-not-exist');check(await picker.getByText('No releases match.',{exact:false}).isVisible(),'empty search feedback');await search.fill('');
  await page.screenshot({path:'output/playwright/upgrade-release-picker.png'});
  for(const theme of ['light','dark']){
    await page.setViewportSize({width:390,height:844});await page.evaluate(t=>{document.documentElement.dataset.theme=t;document.documentElement.style.colorScheme=t;},theme);
    check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'no viewport overflow');
    check(await picker.evaluate(el=>el.scrollWidth<=el.clientWidth+1),'no picker overflow');
    await page.screenshot({path:`output/playwright/upgrade-release-picker-${theme}.png`});
  }
  check(creates.length===0,'choosing versions starts no task');
  check(errors.length===0,errors.join('\n'));
  return {options:'passed',releases:153,creates:0,errors};
}
