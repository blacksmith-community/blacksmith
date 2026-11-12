const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot, sleep } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const UI = path.resolve(__dirname, '..', '..', 'ui');
const css = () => fs.readFileSync(path.join(UI, 'blacksmith.css'), 'utf8');

// The block of rules that applies under 700px, so a rule outside it fails.
const phoneRules = () => {
  const text = css();
  const start = text.indexOf('@media (max-width: 699.98px) { /* stacked cards */');
  expect(start).toBeGreaterThan(-1);
  let depth = 0;
  for (let i = text.indexOf('{', start); i < text.length; i += 1) {
    if (text[i] === '{') depth += 1;
    if (text[i] === '}') { depth -= 1; if (depth === 0) return text.slice(start, i + 1); }
  }
  throw new Error('unterminated media block');
};

const mount = (w, d, html) => {
  const host = d.createElement('div');
  host.innerHTML = html;
  d.body.appendChild(host);
  w.__T.initializeAllTables();
  return host;
};

const tasks = [
  { id: 7, state: 'done', description: 'create release', deployment: 'dep-a', user: 'admin', started_at: '2026-10-31T10:00:00Z', ended_at: '2026-10-31T10:05:00Z' },
  { id: 9, state: 'error', description: 'delete deployment', deployment: 'dep-b', user: 'ops', started_at: '2026-10-31T11:00:00Z', ended_at: '2026-10-31T11:01:00Z' }
];
const vms = [
  { id: 'vm-a', job_name: 'blacksmith', index: 0, state: 'running', job_state: 'running', ips: ['10.0.0.4'], az: 'z1' },
  { id: 'vm-b', job_name: 'blacksmith', index: 1, state: 'running', job_state: 'failing', ips: ['10.0.0.5'], az: 'z2' }
];
const events = [
  { id: '1', time: '2026-10-31T10:00:00Z', user: 'admin', action: 'create', object_type: 'deployment', object_name: 'x', task_id: '7' },
  { id: '2', time: '2026-10-31T09:00:00Z', user: 'ops', action: 'delete', object_type: 'deployment', object_name: 'y', task_id: '9' }
];
const configs = { configs: [{ id: '1', type: 'cloud', name: 'default', team: 'blacksmith', created_at: '2026-10-25' }] };

const labels = (table) => Array.from(table.tBodies[0].rows[0].cells).map((c) => c.getAttribute('data-label'));

t('every cell in a card table carries its column name', () => {
  const { w, d } = boot();
  try {
    const t1 = mount(w, d, w.__T.formatTasks(tasks)).querySelector('table.tasks-table');
    expect(labels(t1)).toEqual(['ID', 'State', 'Description', 'Deployment', 'User', 'Started', 'Duration']);
    expect(t1.getAttribute('data-cards')).toBe('true');
    const t2 = mount(w, d, w.__T.formatVMs(vms, 'inst-1')).querySelector('table.vms-table');
    expect(labels(t2).slice(0, 5)).toEqual(['Instance', 'VM state', 'Job state', 'IPs', 'AZ']);
    const t3 = mount(w, d, w.__T.formatEvents(events)).querySelector('table.events-table');
    expect(labels(t3)).toEqual(['Timestamp', 'User', 'Action', 'Object', 'Task', 'Error']);
    const t4 = mount(w, d, w.__T.formatConfigs(configs)).querySelector('.configs-table-container table');
    expect(labels(t4)).toEqual(['ID', 'Type', 'Name', 'Team', 'Created at']);
  } finally { w.close(); }
});

t('the VMs Actions cell has no label and keeps its buttons', () => {
  const { w, d } = boot();
  try {
    const table = mount(w, d, w.__T.formatVMs(vms, 'inst-1')).querySelector('table.vms-table');
    const row = table.tBodies[0].rows[0];
    const action = row.cells[row.cells.length - 1];
    expect(action.getAttribute('data-label')).toBe('');
    expect(action.querySelectorAll('button, a').length).toBeGreaterThan(0);
    action.querySelectorAll('button').forEach((b) => expect(b.getAttribute('aria-label') || b.getAttribute('title') || b.textContent.trim()).toBeTruthy());
  } finally { w.close(); }
});

t('rows added after the first pass get their labels', async () => {
  const { w, d } = boot();
  try {
    const table = mount(w, d, w.__T.formatTasks(tasks)).querySelector('table.tasks-table');
    const body = table.tBodies[0];
    const row = body.rows[0].cloneNode(true);
    row.querySelectorAll('[data-label]').forEach((c) => c.removeAttribute('data-label'));
    body.appendChild(row);
    await sleep(30);
    expect(row.cells[1].getAttribute('data-label')).toBe('State');
  } finally { w.close(); }
});

t('a row that spans the table is left unlabelled', () => {
  const { w, d } = boot();
  try {
    const table = mount(w, d, w.__T.formatTasks([])).querySelector('table.tasks-table');
    const cell = table.tBodies[0].querySelector('td[colspan]');
    expect(cell).not.toBeNull();
    expect(cell.hasAttribute('data-label')).toBe(false);
  } finally { w.close(); }
});

t('the toolbar row inside a table head is not a card header', () => {
  const { w, d } = boot();
  try {
    const host = mount(w, d, '<table class="tasks-table"><thead><tr class="table-controls-row"><th colspan="2" class="table-controls-header">toolbar</th></tr><tr><th>Service</th><th>Plans</th></tr></thead><tbody><tr><td>valkey</td><td>2 plan(s)</td></tr></tbody></table>');
    const table = host.querySelector('table');
    expect(labels(table)).toEqual(['Service', 'Plans']);
    expect(table.tHead.rows[0].querySelector('[data-label]')).toBeNull();
  } finally { w.close(); }
});

t('a sortable card table gets a Sort by select that orders the rows', () => {
  const { w, d } = boot();
  try {
    const host = mount(w, d, w.__T.formatTasks(tasks));
    const table = host.querySelector('table.tasks-table');
    const bar = table.previousElementSibling;
    expect(bar.classList.contains('cards-sort')).toBe(true);
    const select = bar.querySelector('select');
    expect(select.id).toBeTruthy();
    expect(bar.querySelector(`label[for="${select.id}"]`).textContent).toBe('Sort by');
    const options = Array.from(select.options).map((o) => o.textContent);
    expect(options).toContain('ID, ascending');
    expect(options).toContain('ID, descending');
    const pick = (text) => {
      select.value = Array.from(select.options).find((o) => o.textContent === text).value;
      select.dispatchEvent(new w.Event('change', { bubbles: true }));
    };
    const ids = () => Array.from(table.tBodies[0].rows).map((r) => r.cells[0].textContent.trim());
    pick('ID, descending');
    expect(ids()).toEqual(['9', '7']);
    pick('ID, ascending');
    expect(ids()).toEqual(['7', '9']);
    // Running the pass again does not add a second select.
    w.__T.initializeAllTables();
    expect(host.querySelectorAll('.cards-sort').length).toBe(1);
  } finally { w.close(); }
});

t('a table that opts out of sorting gets no Sort by select', () => {
  const { w, d } = boot();
  try {
    const host = mount(w, d, '<table class="vms-table" data-sortable="false"><thead><tr><th>Instance</th><th>Actions</th></tr></thead><tbody><tr><td>a</td><td><button aria-label="x">x</button></td></tr></tbody></table>');
    expect(host.querySelector('.cards-sort')).toBeNull();
    expect(labels(host.querySelector('table'))).toEqual(['Instance', '']);
  } finally { w.close(); }
});

t('the stylesheet stacks the rows under 700px and leaves wider screens alone', () => {
  const rules = phoneRules();
  expect(rules).toMatch(/table\[data-cards="true"\][^{]*\{[^}]*display: block/);
  expect(rules).toMatch(/tbody > tr[^{]*\{[^}]*border: 1px solid var\(--border\)/);
  expect(rules).toMatch(/td::before[^{]*\{[^}]*content: attr\(data-label\)/);
  expect(rules).toMatch(/td\[data-label=""\]::before[^{]*\{[^}]*display: none/);
  // The head stays in the accessibility tree, off screen.
  expect(rules).toMatch(/thead > tr:not\(\.table-controls-row\)[^{]*\{[^}]*clip/);
  expect(rules).toMatch(/\.cards-sort[^{]*\{[^}]*display: flex/);
  // Outside the media block the Sort by bar stays hidden.
  const outside = css().replace(rules, '');
  expect(outside).toMatch(/\.cards-sort \{[^}]*display: none/);
  expect(outside).not.toMatch(/data-cards[^{]*\{[^}]*attr\(data-label\)/);
});

t('the card tables are named in one selector list in the script', () => {
  const src = fs.readFileSync(path.join(UI, 'blacksmith.js'), 'utf8');
  const list = src.match(/const CARD_TABLE_SELECTOR = \[([^\]]*)\]/)[1];
  ['.tasks-table', '.vms-table', '.events-table', '.configs-table-container table'].forEach((name) => expect(list).toContain(name));
});

t('the log tables become cards with a label on every cell', () => {
  const { w, d } = boot();
  try {
    const broker = mount(w, d, w.__T.formatBlacksmithLogs('2026-10-31T10:00:00Z INFO first\n2026-10-31T10:00:01Z ERROR second', null, { total_lines: 2, has_more: false })).querySelector('table.logs-table');
    expect(broker.getAttribute('data-cards')).toBe('true');
    const brokerLabels = labels(broker);
    expect(brokerLabels.length).toBeGreaterThan(0);
    brokerLabels.forEach((label) => expect(label).toBeTruthy());
    const inst = mount(w, d, w.__T.formatInstanceLogs({ 'node/a': { 'a.log': 'INFO first' } }, false)).querySelector('table.instance-logs-table');
    expect(inst.getAttribute('data-cards')).toBe('true');
    expect(labels(inst)).toEqual(['Timestamp (UTC)', 'Level', 'Message']);
  } finally { w.close(); }
});

t('the deployment and debug log tables are in the card list', () => {
  const src = fs.readFileSync(path.join(UI, 'blacksmith.js'), 'utf8');
  const list = src.match(/const CARD_TABLE_SELECTOR = \[([^\]]*)\]/)[1];
  ['.deployment-log-table', '.debug-log-table', '.instance-logs-table', '.logs-table'].forEach((name) => expect(list).toContain(name));
});

t('the Marketplace and Services tables become cards and keep their toolbar row', () => {
  const { w, d } = boot();
  try {
    const wrap = (cls, heads, cells) => `<div class="logs-table-container"><table class="${cls}"><thead><tr class="table-controls-row"><th colspan="${heads.length}" class="table-controls-header"><div class="table-controls-container"><button type="button">Refresh</button></div></th></tr><tr>${heads.map((h) => `<th>${h}</th>`).join('')}</tr></thead><tbody><tr>${cells.map((c) => `<td>${c}</td>`).join('')}</tr></tbody></table></div>`;
    const market = mount(w, d, wrap('marketplace-services-table', ['Service', 'Description', 'Plans', 'Available'], ['blacksmith', 'broker', '2 plan(s)', 'Yes'])).querySelector('table');
    expect(labels(market)).toEqual(['Service', 'Description', 'Plans', 'Available']);
    expect(market.getAttribute('data-cards')).toBe('true');
    expect(market.querySelector('.table-controls-row button')).not.toBeNull();
    const svc = mount(w, d, wrap('service-instances-table', ['Name', 'Type', 'Status', 'Bindings'], ['db', 'managed', 'succeeded', '<span>1</span>'])).querySelector('table');
    expect(labels(svc)).toEqual(['Name', 'Type', 'Status', 'Bindings']);
  } finally { w.close(); }
});

t('the Org and Space pairs stack under 700px', () => {
  const rules = phoneRules();
  expect(rules).toMatch(/\.org-space-table[^{]*\{[^}]*display: (block|grid)/);
  expect(rules).toMatch(/\.org-space-table select[^{]*\{[^}]*width: 100%/);
  const outside = css().replace(rules, '');
  expect(outside).toMatch(/\.org-space-table select \{[^}]*width: 200px/);
});

const H = require('./certificates/harness.js');

t('the certificate tables are in the card list and keep wide columns hidden', () => {
  const src = fs.readFileSync(path.join(UI, 'blacksmith.js'), 'utf8');
  const list = src.match(/const CARD_TABLE_SELECTOR = \[([^\]]*)\]/)[1];
  expect(list).toContain('.cert-table');
  expect(list).toContain('.cert-horizon-table');
  const rules = phoneRules();
  expect(rules).toMatch(/td\.cert-col-wide[^{]*\{[^}]*display: none/);
});

t('every fleet certificate row is a card of labelled pairs', async () => {
  const { boot: bootFleet, sleep: nap, iso, now, day } = H.fleet;
  const { w, d, server } = await bootFleet();
  try {
    server.certs = [{ id: 'A', key: '/ws/A', variable: 'A', kind: 'leaf', common_name: 'cn-A', issuer: 'CN=X', owners: [{ kind: 'instance', instance_id: 'i1', instance_name: 'one', service_name: 'valkey', plan_name: 's' }], affected_instances: 1, effective_not_after: iso(now + 40 * day), days_left: 40, expiry: 'ok', health: 'ok', attention: true, findings: [], observations: [], coverage: { expected_listeners: 1, observed_listeners: 1, failed_listeners: 0, complete: true }, rotation: { allowed: true }, versions: {} }];
    // The page starts its table observer on DOMContentLoaded, so the rows render as they do live.
    d.dispatchEvent(new w.Event('DOMContentLoaded'));
    await w.blacksmithNav.switchTab('certificates');
    await nap(250);
    const table = d.getElementById('cert-table');
    expect(table.getAttribute('data-cards')).toBe('true');
    const row = d.querySelector('#cert-tbody tr.cert-row');
    const names = Array.from(row.cells).map((c) => c.getAttribute('data-label'));
    expect(names.slice(1, 6)).toEqual(['Expiry', 'Health', 'Certificate', 'Owner', 'Expires']);
    // The checkbox cell carries no visible label, only its own accessible name.
    expect(names[0]).toBe('');
    expect(row.cells[0].querySelector('input').getAttribute('aria-label')).toMatch(/^Rotate /);
  } finally { w.close(); }
});

t('card cells and the toolbar row stay transparent so the card shows one surface', () => {
  const rules = phoneRules();
  expect(rules).toMatch(/> tbody > tr > td \{[^}]*background: transparent/);
  // The dark theme paints the toolbar header with an important rule, so the card rule has to as well.
  expect(rules).toMatch(/th\.table-controls-header \{[^}]*background: transparent !important/);
});
