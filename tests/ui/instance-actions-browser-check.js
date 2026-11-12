// Run with playwright-cli run-code --filename=tests/ui/instance-actions-browser-check.js.
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
  await page.addInitScript(()=>{window.copiedTexts=[];Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async text=>{window.copiedTexts.push(text);}}});});
  await page.goto('http://127.0.0.1:4202');

  await page.locator('#rail-area-services').click();await page.locator('#console-rail a[data-screen="services"]:not([data-instance])').click();
  await page.locator('#refresh-services').waitFor();
  for(const width of [1440,390]){
    await page.setViewportSize({width,height:900});
    const buttons=await page.locator('#services .filter-buttons button').evaluateAll(nodes=>nodes.map(el=>({text:el.textContent.trim(),title:el.title,name:el.getAttribute('aria-label'),top:el.getBoundingClientRect().top,width:el.getBoundingClientRect().width,height:el.getBoundingClientRect().height})));
    check(buttons.length===3,'three actions');check(buttons.every(button=>!button.text&&button.title&&button.name),'named icon-only controls');
    check(buttons.every(button=>Math.abs(button.top-buttons[0].top)<1),'actions share a row');
    check(buttons.every(button=>button.width>=40&&button.width<=44&&button.height>=40),'compact usable targets');
    await page.screenshot({path:`output/playwright/instance-actions-${width}.png`});
  }
  await page.locator('#instance-search').fill('Application cache 1002');
  await page.locator('#copy-deployment-names').click();
  await page.waitForFunction(()=>window.copiedTexts.length===1);
  check((await page.evaluate(()=>window.copiedTexts[0])).includes('cache-1002'),'copy uses matching instances');
  await page.waitForFunction(()=>document.getElementById('copy-deployment-names').title==='Deployment names copied');
  await page.locator('#clear-filters').click();check(await page.locator('#instance-search').inputValue()==='','clear resets search');
  await page.locator('#refresh-services').click();
  await page.waitForFunction(()=>document.getElementById('refresh-services')&&!document.getElementById('refresh-services').disabled);
  check(await page.locator('#services .filter-buttons button').count()===3,'refresh restores the three icon actions');
  check(errors.length===0,errors.join('\n'));
  return {actions:'passed',widths:[1440,390],errors};
}
