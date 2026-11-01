const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot, sleep } } = require('./certificates/harness.js');
const { pinZone } = require('./fixed-zone.js');

pinZone('America/New_York');

const OAT = fs.readFileSync(path.resolve(__dirname, '..', '..', 'ui', 'vendor', 'oat', 'oat.min.js'), 'utf8');

// The harness window does not run inline handler attributes, so this runs the button's own
// onclick text on a real click event, as a browser would.
const clickButton = (w, button) => {
  button.addEventListener('click', (event) => new w.Function('event', button.getAttribute('onclick')).call(button, event));
  button.click();
};
const fetchedAtOf = (content) => content.querySelector('.data-fetched-note time')?.getAttribute('datetime');

// Leaves the view and comes back, which is when the "Data fetched" line is drawn.
const comeBack = (w, content, key) => {
  expect(w.__T.retainView(content, 'other')).toBe(false);
  content.innerHTML = '<p>Other</p>';
  expect(w.__T.retainView(content, key)).toBe(true);
};

test('Refresh on instance logs moves the fetched time', async () => {
  const { w, d } = boot();
  try {
    let now = Date.parse('2026-11-01T10:00:05Z');
    w.Date.now = () => now;
    await sleep(200);
    const panel = d.getElementById('services');
    panel.classList.add('active');
    const item = d.createElement('div');
    item.className = 'service-instance-item active';
    item.dataset.id = 'inst-1';
    panel.appendChild(item);
    w.currentInstanceInfo = { id: 'inst-1' };
    let content = panel.querySelector('.detail-content');
    if (!content) {
      content = d.createElement('div');
      content.className = 'detail-content';
      panel.appendChild(content);
    }
    panel.querySelectorAll('.detail-tab').forEach((tab) => tab.classList.toggle('active', tab.dataset.tab === 'instance-logs'));
    expect(w.__T.retainView(content, 'instance-logs')).toBe(false);
    content.innerHTML = w.__T.formatInstanceLogs({ 'job/0': { 'job.log': 'one line' } }, false);
    w.fetch = () => Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve(JSON.stringify({ 'job/0': { 'job.log': 'a refreshed line' } })) });
    now = Date.parse('2026-11-01T10:30:00Z');
    // The logs toolbar has no inline handler, so a plain click reaches the document listener.
    const button = content.querySelector('[data-instance-log="refresh"]');
    expect(button).not.toBeNull();
    button.click();
    await sleep(60);
    comeBack(w, content, 'instance-logs');
    expect(fetchedAtOf(content)).toBe('2026-11-01T10:30:00.000Z');
  } finally { w.close(); }
});

const startBroker = async (routes) => {
  const env = boot({ noDialogs: true, routes: [
    ['/b/status', (u, o, j) => j(200, { env: 'test', deployment: 'blacksmith', instances: {}, plans: {} })],
    ['/b/config/ssh/ui-terminal-status', (u, o, j) => j(200, {})],
    ['/v2/catalog', (u, o, j) => j(200, { services: [] })],
    ...routes] });
  const { w, d } = env;
  w.eval(OAT);
  await sleep(60);
  const content = d.createElement('div');
  content.className = 'detail-content';
  d.body.appendChild(content);
  content.innerHTML = w.__T.renderBrokerTab();
  await w.__T.initBrokerTab();
  await sleep(20);
  const manager = w.CFEndpointManager;
  manager.selectedEndpoint = 'lab';
  manager.connectionStates.lab = { connected: true };
  return { ...env, content, manager };
};

test('Refresh marketplace moves the fetched time', async () => {
  const { w, d, content, manager } = await startBroker([
    ['/b/cf/endpoints/lab/marketplace', (u, o, j) => j(200, { services: [{ name: 'market-service', description: 'd', plans: [{}], available: true }] })]]);
  try {
    let now = Date.parse('2026-11-01T10:00:05Z');
    w.Date.now = () => now;
    expect(w.__T.retainView(content, 'broker')).toBe(false);
    await manager.loadTabContent('marketplace', 'lab');
    await sleep(20);
    now = Date.parse('2026-11-01T10:30:00Z');
    clickButton(w, content.querySelector('[data-panel="marketplace"] .refresh-btn-logs'));
    await sleep(60);
    comeBack(w, content, 'broker');
    expect(fetchedAtOf(content)).toBe('2026-11-01T10:30:00.000Z');
  } finally { w.close(); }
});

test('Refresh service instances moves the fetched time', async () => {
  const { w, d, content, manager } = await startBroker([
    ['/b/cf/endpoints/lab/orgs/org-1/spaces/space-1/services', (u, o, j) => j(200, { services: [{ name: 'a-service', type: 'managed', guid: 'g1', last_operation: { state: 'succeeded' } }] })],
    ['/bindings', (u, o, j) => j(200, { bindings: [] })]]);
  try {
    let now = Date.parse('2026-11-01T10:00:05Z');
    w.Date.now = () => now;
    expect(w.__T.retainView(content, 'broker')).toBe(false);
    const panel = content.querySelector('[data-panel="services"]');
    panel.innerHTML = '<select id="org-selector"><option value="org-1">Org</option></select><select id="space-selector"><option value="space-1">Space</option></select><div id="services-results"></div>';
    d.getElementById('org-selector').value = 'org-1';
    d.getElementById('space-selector').value = 'space-1';
    await manager.loadServiceInstances('lab', 'org-1', 'space-1');
    await sleep(20);
    now = Date.parse('2026-11-01T10:30:00Z');
    clickButton(w, content.querySelector('#services-results .refresh-btn-logs'));
    await sleep(60);
    comeBack(w, content, 'broker');
    expect(fetchedAtOf(content)).toBe('2026-11-01T10:30:00.000Z');
  } finally { w.close(); }
});
