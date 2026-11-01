const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot, sleep } } = require('./certificates/harness.js');

const UUID = '154a360d-8f56-41e4-98cf-0a30856edd69';
const instances = {
  [UUID]: {
    service_id: 'valkey', plan_id: 'standalone', plan: { id: 'standalone', name: 'standalone' },
    instance_name: 'certmgmt-valkey-solo', created: 1793500000, vm_status: 'running', vm_count: 1, vm_healthy: 1
  },
  'plain-id': { service_id: 'valkey', plan_id: 'standalone', plan: { id: 'standalone', name: 'standalone' } }
};

const render = (w, d) => {
  w.plansData = { services: [{ id: 'valkey', name: 'valkey' }] };
  const host = d.createElement('div');
  host.innerHTML = w.__T.renderServicesTemplate(instances);
  return host;
};

test('a card leads with the name, then service and plan with the status, then the UUID', () => {
  const { w, d } = boot();
  try {
    const card = render(w, d).querySelector(`.service-item[data-instance-id="${UUID}"]`);
    const rows = [...card.children].map((c) => c.className.split(' ')[0]);
    expect(rows).toEqual(['service-instance-name', 'service-meta', 'service-id']);
    expect(card.querySelector('.service-instance-name').textContent).toBe('certmgmt-valkey-solo');
    const meta = card.querySelector('.service-meta');
    expect(meta.querySelector('.service-meta-text').textContent).toBe('valkey / standalone');
    // The status badge ends the same line.
    expect(meta.lastElementChild.classList.contains('vm-status-badge')).toBe(true);
    expect(meta.textContent).not.toMatch(/\d{4}-\d{2}-\d{2}/);
  } finally { w.close(); }
});

test('the UUID is a small muted line with its full value in the title', () => {
  const { w, d } = boot();
  try {
    const card = render(w, d).querySelector(`.service-item[data-instance-id="${UUID}"]`);
    const id = card.querySelector('.service-id');
    expect(id.textContent).toBe(UUID);
    expect(id.getAttribute('title')).toBe(UUID);
    const css = fs.readFileSync(path.join(__dirname, '..', '..', 'ui', 'blacksmith.css'), 'utf8');
    const rule = css.match(/#services \.service-item \.service-id \{([^}]*)\}/)[1];
    expect(rule).toContain('text-overflow: ellipsis');
    expect(rule).toContain('white-space: nowrap');
    expect(rule).toContain('monospace');
    expect(rule).not.toContain('word-break');
  } finally { w.close(); }
});

test('a card without a name shows the UUID once, in the heading position', () => {
  const { w, d } = boot();
  try {
    const card = render(w, d).querySelector('.service-item[data-instance-id="plain-id"]');
    expect(card.textContent.match(/plain-id/g)).toHaveLength(1);
    expect(card.firstElementChild.className).toContain('service-instance-name');
  } finally { w.close(); }
});

test('search still matches the UUID', async () => {
  const routes = [['/v2/catalog', (u, o, j) => j(200, { services: [{ id: 'valkey', name: 'valkey', plans: [{ id: 'standalone', name: 'standalone' }] }] })],
    ['/b/status', (u, o, j) => j(200, { env: 'lab', deployment: 'blacksmith', instances, plans: {} })]];
  const { w, d } = boot({ noDialogs: true, routes });
  try {
    await sleep(80);
    const search = d.getElementById('instance-search');
    search.value = '0a30856e';
    search.dispatchEvent(new w.Event('input'));
    const shown = [...d.querySelectorAll('#services .service-item')].map((i) => i.dataset.instanceId);
    expect(shown).toEqual([UUID]);
  } finally { w.close(); }
});

test('opening an instance scrolls its card into view in the sidebar', async () => {
  const routes = [['/v2/catalog', (u, o, j) => j(200, { services: [{ id: 'valkey', name: 'valkey', plans: [{ id: 'standalone', name: 'standalone' }] }] })],
    ['/b/status', (u, o, j) => j(200, { env: 'lab', deployment: 'blacksmith', instances, plans: {} })],
    [`/b/${UUID}/details`, (u, o, j) => j(200, { instance_name: 'certmgmt-valkey-solo' })]];
  const { w, d } = boot({ noDialogs: true, routes });
  try {
    await sleep(80);
    const scrolled = [];
    w.HTMLElement.prototype.scrollIntoView = function (options) { scrolled.push([this.dataset.instanceId, options]); };
    await w.blacksmithViews.activate({ screen: 'services', instance: UUID, section: 'details' }, {});
    expect(scrolled.map(([id]) => id)).toContain(UUID);
    expect(scrolled.find(([id]) => id === UUID)[1]).toEqual({ block: 'nearest' });
  } finally { w.close(); }
});
