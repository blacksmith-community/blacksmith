const { test, expect } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const ID = '42';
const CONTENT = 'azs:\n- name: z1\n';
const detail = { id: ID, name: 'default', type: 'cloud', team: 'blacksmith', created_at: '2026-10-24T10:00:00Z', is_active: true, content: CONTENT, metadata: { a: 1 } };
const listEntry = { id: `${ID}*`, name: 'default', type: 'cloud', team: 'blacksmith', created_at: '2026-10-24T10:00:00Z', is_active: true };

const mockConfigs = (w) => {
  w.__routes.push([/\/b\/configs\/cloud\/default\/versions/, (u, o, json) => json(200, { configs: [listEntry], count: 1 })]);
  w.__routes.push([/\/b\/configs\/42/, (u, o, json) => json(200, detail)]);
};

// A user sees an element only when neither it nor an ancestor hides it.
const shown = (el) => {
  for (let n = el; n && n.nodeType === 1; n = n.parentElement) {
    if (n.classList.contains('is-hidden') || n.classList.contains('is-invisible') || n.style.display === 'none' || n.hasAttribute('inert')) return false;
  }
  return true;
};

const expectOverview = (d) => {
  const text = (id) => d.getElementById(id).textContent;
  expect(text('config-id')).toContain(ID);
  expect(text('config-name')).toBe('default');
  expect(text('config-type')).toBe('cloud');
  expect(d.getElementById('config-content-text').textContent).toBe(CONTENT);
  expect(shown(d.getElementById('config-id'))).toBe(true);
  expect(shown(d.getElementById('config-content-text'))).toBe(true);
};

t('the config ID link on the configs list shows the config', async () => {
  const { w, d } = boot();
  try {
    mockConfigs(w);
    w.showConfigDetails(ID);
    await sleep(100);
    expectOverview(d);
  } finally { w.close(); }
});

t('the BOSH configs list link shows the config and its versions', async () => {
  const { w, d } = boot();
  try {
    mockConfigs(w);
    w.showConfigVersions('cloud', 'default', ID);
    await sleep(150);
    expectOverview(d);
    expect(d.querySelectorAll('#config-versions-list .version-item').length).toBe(1);
  } finally { w.close(); }
});
