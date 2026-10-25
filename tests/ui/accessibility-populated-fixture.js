// Load read-only synthetic data for accessibility audits. Serve ui on port 4202.
// Start `python3 -m http.server 4202 --directory ui` first. All API calls are fixtures.
async (page) => {
  const writes = [];
  let delayCredentials = false;
  const catalog = { services: [{ id: 'valkey', name: 'valkey', description: 'Managed cache', plans: [{ id: 'small', name: 'small', description: 'Single node cache' }] }] };
  const instances = Object.fromEntries(Array.from({ length: 1003 }, (_, i) => [`cache-${i + 1}`, {
    service_id: 'valkey', plan_id: 'small', instance_name: `Application cache ${i + 1}`,
    created_at: '2026-10-20T10:00:00Z', stemcell: 'ubuntu-jammy/1.80', deployment_name: `small-cache-${i + 1}`,
    vm_status: 'running', vm_count: 1, vm_healthy: 1
  }]));
  await page.unrouteAll();
  await page.route('**/v2/catalog', route => route.fulfill({ json: catalog }));
  await page.route('**/b/**', async route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/b/upgrade/preview') return route.fulfill({json:{id:'preview',diverged:[]}});
    if (route.request().method() !== 'GET') {
      writes.push(path);
      return route.fulfill({ status: 403, json: { error: 'Writes blocked in UI test' } });
    }
    let json = {};
    if (path === '/b/blacksmith/credentials' && delayCredentials) await new Promise(resolve => setTimeout(resolve, 200));
    if (path === '/b/status') json = { env: 'UI regression fixture', deployment: 'blacksmith-preview', instances, plans: { 'valkey/small': { limit: 100 } } };
    else if (path.includes('ui-terminal-status')) json = { enabled: false };
    else if (path.endsWith('/stemcells')) json = [{ operating_system: 'ubuntu-jammy', version: '1.90' }];
    else if (path.endsWith('/releases')) json = [{ name: 'valkey-forge', release_versions: [{ version: '1.3.0' }] }];
    else if (path.endsWith('/deployment-releases')) json = Object.fromEntries(Object.keys(instances).map(id => [`small-${id}`, ['valkey-forge/1.2.0']]));
    else if (path === '/b/configs') json={configs:[{id:'42*',name:'default',type:'cloud',team:'blacksmith',created_at:'2026-10-24T10:00:00Z'}]};
    else if (path === '/b/configs/42') json={id:'42',name:'default',type:'cloud',created_at:'2026-10-24T10:00:00Z',content:'azs: []'};
    else if (path === '/b/tasks/71') json={task:{id:71,state:'done',description:'Create deployment',deployment:'blacksmith-preview',timestamp:1792936800},events:[]};
    else if (path === '/b/tasks') json=[{id:71,state:'done',description:'Create deployment',deployment:'blacksmith-preview',timestamp:1792936800}];
    else if (path.endsWith('/events')) json=[{time:1792936800,user:'admin',action:'create',object_type:'deployment',object_name:'blacksmith-preview',task_id:71}];
    else if (path.endsWith('/vms')) json=[{instance:'node/0',job_state:'running',state:'running',ips:['10.0.0.1'],az:'z1',vm_type:'small',agent_id:'agent-1'}];
    else if (path.endsWith('/credentials')) json={service_type:'valkey',host:'fixture.invalid',port:6379,username:'fixture',password:'fixture-only',ca_certificate:'-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----'};
    else if (path.endsWith('/tasks') || path.endsWith('/events') || path.endsWith('/vms') || path.endsWith('/endpoints') || path.endsWith('/registrations')) json = [];
    else if (path.endsWith('/details')) {
      if (path.includes('cache-2/')) await new Promise(resolve => setTimeout(resolve, 200));
      json = { deployment_name: path.split('/')[2] };
    } else if (path.endsWith('/manifest')) json = { parsed: { name: 'preview', releases: [], instance_groups: [] }, text: 'name: preview' };
    else if (path.endsWith('/logs')) json = { logs: '2026-10-25 12:00:00.000 [INFO] Fixture ready', total_lines: 1 };
    else if (path.endsWith('/settings')) json = { max_parallel_jobs: 2 };
    await route.fulfill({ json });
  });
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.goto('http://127.0.0.1:4202');
  await page.locator('#blacksmith .entity-overview').waitFor();

return {ready:true};
}