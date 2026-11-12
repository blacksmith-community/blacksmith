const { test, expect } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

// The copied state swaps the copy icon for the Lucide check.
const CHECK = 'svg.lucide-check';

// Mounts rendered markup in the document so the delegated click handler runs.
const mount = (d, html) => {
  const host = d.createElement('div');
  host.innerHTML = html;
  d.body.appendChild(host);
  return host;
};

const copyButtons = (host) => Array.from(host.querySelectorAll('button[data-entity-copy], button.copy-btn-inline'));

const expectIconButtons = (buttons) => {
  expect(buttons.length).toBeGreaterThan(0);
  buttons.forEach((b) => {
    expect(b.querySelector('svg')).not.toBeNull();
    expect(b.querySelector('svg').getAttribute('stroke')).toBe('currentColor');
    expect(b.textContent.trim()).toBe('');
    expect(b.getAttribute('aria-label') || '').toMatch(/^Copy /);
  });
};

t('the broker overview copy buttons are icon buttons that copy their value', async () => {
  const { w, d } = boot();
  try {
    const copied = [];
    w.navigator.clipboard = { writeText: (v) => { copied.push(v); return Promise.resolve(); } };
    const host = mount(d, w.__T.renderBlacksmithOverview({ deployment: 'ocfp-lab-blacksmith', env: 'ocfp-lab', instances: {}, plans: {} }));
    const buttons = copyButtons(host);
    expectIconButtons(buttons);
    const deployment = buttons.find((b) => b.getAttribute('aria-label') === 'Copy Deployment');
    const environment = buttons.find((b) => b.getAttribute('aria-label') === 'Copy Environment');
    expect(deployment).toBeDefined();
    expect(environment).toBeDefined();
    expect(deployment.classList.contains('ghost')).toBe(false);

    environment.click();
    await sleep(120);
    expect(copied).toEqual(['ocfp-lab']);
    expect(d.getElementById('blacksmith-live-region').textContent).toBe('Copied Environment');
    expect(environment.querySelector(CHECK)).not.toBeNull();
    expect(environment.getAttribute('aria-label')).toBe('Copy Environment');
  } finally { w.close(); }
});

t('the instance overview copy buttons are icon buttons that copy their value', async () => {
  const { w, d } = boot();
  try {
    const copied = [];
    w.navigator.clipboard = { writeText: (v) => { copied.push(v); return Promise.resolve(); } };
    mount(d, w.__T.renderServiceDetail('id-1', { service_id: 'valkey', plan_id: 'cluster' }, null));
    const overview = mount(d, w.serviceInstanceDetailsContent['id-1']);
    const buttons = copyButtons(overview);
    expectIconButtons(buttons);
    const first = buttons[0];
    const expected = first.dataset.entityCopy;
    first.click();
    await sleep(120);
    expect(copied).toEqual([expected]);
    expect(d.getElementById('blacksmith-live-region').textContent).toBe('Copied ' + first.getAttribute('aria-label').slice(5));
  } finally { w.close(); }
});

t('inline copy buttons keep their icon and copy through copyValue', async () => {
  const { w, d } = boot();
  try {
    const copied = [];
    w.navigator.clipboard = { writeText: (v) => { copied.push(v); return Promise.resolve(); } };
    const host = mount(d, '<span class="copy-wrapper"><span>vm-1</span><button class="copy-btn-inline" aria-label="Copy VM" title="Copy VM"><svg stroke="currentColor"></svg></button></span>');
    const button = host.querySelector('button');
    await w.copyValue({ preventDefault() {}, stopPropagation() {}, currentTarget: button }, 'vm-1');
    expect(copied).toEqual(['vm-1']);
    expect(button.classList.contains('copied')).toBe(true);
    expect(button.querySelector(CHECK)).not.toBeNull();
  } finally { w.close(); }
});

const fs = require('fs');
const path = require('path');
const ui = path.resolve(__dirname, '../../ui');

t('every copy button in the markup uses the one icon and has a name', () => {
  const files = ['blacksmith.js', 'index.html'].map((f) => fs.readFileSync(path.join(ui, f), 'utf8'));
  const buttons = [];
  files.forEach((src) => {
    const re = /<button\b[^>]*?(?:class="[^"]*\b(?:copy-btn-logs|copy-btn-manifest|copy-btn)\b[^"]*"|id="copy-config-content"|id="copy-deployment-names")[^>]*>[\s\S]*?<\/button>/g;
    let m;
    while ((m = re.exec(src))) buttons.push(m[0]);
  });
  // The set of literal copy sites is pinned by action hook, so a lost button fails here.
  const hooks = [
    ['copyValue(event, \'${value}\')', 'copyValue(event, \'${value}\')'],
    ['data-entity-copy', 'data-entity-copy'],
    ['id="copy-deployment-names"', 'copy-deployment-names'],
    ['data-secret-copy', 'data-secret-copy'],
    ['copyTableRowsAsText(', 'copyTableRowsAsText'],
    ['copyValue(event, ${escapeAttribute(JSON.stringify(copy))})', 'copyValue(event, copy)'],
    ['copyManifest(', 'copyManifest'],
    ['copyTaskRaw(', 'copyTaskRaw'],
    ['id="copy-header-deployment"', 'copy-header-deployment'],
    ['id="copy-config-content"', 'copy-config-content'],
    ['id="task-deployment-copy"', 'task-deployment-copy'],
  ];
  const found = buttons.map((markup) => (hooks.find(([needle]) => markup.includes(needle)) || [null, 'unknown'])[1]).sort();
  expect(found).toEqual([
    'copy-config-content', 'copy-deployment-names', 'copy-header-deployment', 'copyManifest', 'copyTableRowsAsText',
    'copyTaskRaw', 'copyValue(event, \'${value}\')', 'copyValue(event, \'${value}\')', 'copyValue(event, copy)',
    'data-entity-copy', 'data-secret-copy', 'task-deployment-copy',
  ].sort());
  buttons.forEach((markup) => {
    const open = markup.split(/>\s*(?:<svg|\$\{COPY_ICON_SVG|\$\{blacksmithIcon\()/)[0];
    expect(open).toMatch(/aria-label="[^"]+"/);
    if (markup.includes('${COPY_ICON_SVG}')) return;
    // Static markup carries the helper's output, and generated markup calls the helper.
    expect(markup).toMatch(/blacksmithIcon\('copy', \{ size: 1[46] \}\)|<svg class="lucide lucide-copy"/);
    expect(markup).not.toContain('<rect x="9" y="9"');
    expect(markup).not.toMatch(/<svg[^>]*aria-label=/);
  });
});

t('table, manifest, and certificate copy buttons keep their label and swap to the check', async () => {
  const { w, d } = boot();
  try {
    const copied = [];
    w.navigator.clipboard = { writeText: (v) => { copied.push(v); return Promise.resolve(); } };
    const host = mount(d, '<table class="demo-table"><thead><tr><th>Name</th></tr></thead><tbody><tr><td>alpha</td></tr></tbody></table>'
      + '<button class="copy-btn-logs" aria-label="Copy filtered table rows">' + '<svg stroke="currentColor"></svg></button>');
    const logs = host.querySelector('button');
    await w.copyTableRowsAsText('.demo-table', { currentTarget: logs, preventDefault() {}, stopPropagation() {} });
    expect(copied).toEqual(['Name\nalpha']);
    expect(logs.classList.contains('copied')).toBe(true);
    expect(logs.querySelector(CHECK)).not.toBeNull();
    expect(logs.getAttribute('aria-label')).toBe('Copy filtered table rows');

    // A button with a visible label keeps it beside the icon.
    const labeled = mount(d, '<button class="copy-btn-logs" aria-label="Copy table"><svg stroke="currentColor"></svg><span>Table</span></button>').querySelector('button');
    await w.copyTableRowsAsText('.demo-table', { currentTarget: labeled, preventDefault() {}, stopPropagation() {} });
    expect(labeled.querySelector(CHECK)).not.toBeNull();
    expect(labeled.querySelector('span').textContent).toBe('Copied!');
  } finally { w.close(); }
});

t('the certificate copy button draws the shared icon and swaps to the check', async () => {
  const { instance } = require('./certificates/harness.js');
  const env = instance.boot();
  const { w, d } = env;
  const copied = [];
  w.navigator.clipboard = { writeText: (v) => { copied.push(v); return Promise.resolve(); } };
  const labeled = w.__T.CertView.copyButton('Copy command: openssl x509', 'openssl x509', 'Copy');
  const iconOnly = w.__T.CertView.copyButton('Copy subject', 'CN=demo');
  d.body.appendChild(labeled);
  d.body.appendChild(iconOnly);
  [labeled, iconOnly].forEach((b) => {
    expect(b.querySelector('svg.lucide-copy')).not.toBeNull();
    expect(b.querySelector('svg').getAttribute('stroke')).toBe('currentColor');
    expect(b.getAttribute('aria-label')).toMatch(/^Copy/);
  });
  expect(iconOnly.textContent.trim()).toBe('');
  expect(labeled.textContent.trim()).toBe('Copy');
  iconOnly.click();
  await sleep(120);
  expect(copied).toEqual(['CN=demo']);
  expect(iconOnly.classList.contains('copied')).toBe(true);
  expect(iconOnly.querySelector(CHECK)).not.toBeNull();
  w.close();
});

t('copy buttons are named for what they copy and share one icon', () => {
  const sources = ['blacksmith.js', 'index.html'].map((f) => fs.readFileSync(path.join(ui, f), 'utf8')).join('\n');
  expect(sources).not.toContain('aria-label="Copy value"');
  expect(sources).toContain("cidCopy(vm.vm_cid, 'VM CID')");
  expect(sources).toContain('aria-label="Copy ${label}"');
  expect(sources).toContain("manifestValue(release.sha1, 'release SHA1')");
  expect(sources).toContain('aria-label="Copy ${escapeAttribute(label)}"');
  expect(sources).not.toContain('<rect x="9" y="9"');
  expect(sources).not.toContain('M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1');
  const { blacksmithIcon } = require(path.join(ui, 'icons.js'));
  const staticCopies = sources.match(/<svg class="lucide lucide-copy"[^>]*>[\s\S]*?<\/svg>/g) || [];
  staticCopies.forEach((markup) => {
    const size = /width="(\d+)"/.exec(markup)[1];
    expect(markup).toBe(blacksmithIcon('copy', { size: Number(size) }));
  });
});

t('a copy calls flashCopyIcon once per success path', () => {
  const src = fs.readFileSync(path.join(ui, 'blacksmith.js'), 'utf8').split('\n');
  src.forEach((line, i) => {
    if (!/^\s*flashCopyIcon\(/.test(line)) return;
    const near = src.slice(Math.max(0, i - 6), i).filter((l) => /^\s*flashCopyIcon\(/.test(l));
    expect(near.length).toBe(0);
  });
});

// Every key/value screen puts the copy icon in front of its value text. The
// list is pinned by screen, so a screen that falls back to the old order, or
// one added without the order, shows up here by name.
const textBeforeCopy = (host) => {
  const bad = [];
  Array.from(host.querySelectorAll('button.copy-btn-inline[data-entity-copy], button.copy-btn-inline[onclick]')).forEach((button) => {
    const cell = button.closest('td, dd');
    if (!cell) return;
    const walker = host.ownerDocument.createTreeWalker(cell, 4);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      if (!node.nodeValue.trim() || node.parentElement.closest('button')) continue;
      if (button.compareDocumentPosition(node) & 2) { bad.push(button.getAttribute('aria-label') + ' after ' + node.nodeValue.trim().slice(0, 20)); break; }
    }
  });
  return bad;
};
const VM = { id: 'abc', job_name: 'node', index: 0, state: 'running', job_state: 'running', az: 'z1', vm_type: 'small', ips: ['10.0.0.4'], vm_cid: 'vm-1', agent_id: 'agent-1', disk_cids: ['disk-1'] };
const LEADING_SCREENS = [
  ['Instance Summary, Instance context and Instance metadata', (T) => { T.renderServiceDetail('id-1', { service_id: 'valkey', plan: { name: 'small' } }, { instance_name: 'a', deployment_name: 'b', organization_name: 'c', space_name: 'd', created_at: '2026-10-31T10:00:00Z' }); return T.window.serviceInstanceDetailsContent['id-1']; }],
  ['Blacksmith Broker Summary, Deployment context and technical details', (T) => T.renderBlacksmithOverview({ deployment: 'blacksmith', env: 'lab', instances: {}, plans: {} }, { BOSH: { address: 'https://10.0.0.6:25555' } })],
  ['instance and broker VMs, table and details', (T) => T.formatVMs([VM], 'inst-1')],
  ['instance Credentials', (T) => T.formatCredentials({ username: 'admin', host: 'cache.example', port: 6379 })],
  ['broker Service plans, plan details', (T) => T.renderPlanDetail({ name: 'valkey' }, { name: 'small', blacksmith: { instances: 1, limit: 5 } })],
];
LEADING_SCREENS.forEach(([name, render]) => {
  t('copy icons lead on ' + name, () => {
    const { w, d } = boot();
    try {
      const host = mount(d, render(Object.assign({ window: w }, w.__T)));
      expect(host.querySelectorAll('button.copy-btn-inline').length).toBeGreaterThan(0);
      expect(textBeforeCopy(host)).toEqual([]);
    } finally { w.close(); }
  });
});
