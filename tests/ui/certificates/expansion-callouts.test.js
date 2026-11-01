const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');
const t = JSDOM ? test : test.skip;
const { boot, sleep } = H.fleet;

const strip = (css) => css.replace(/\/\*[\s\S]*?\*\//g, '');
const bodies = (file, selector) => {
  const out = [];
  strip(fs.readFileSync(path.join(H.UI, file), 'utf8')).replace(/([^{}]+)\{([^{}]*)\}/g, (m, sel, body) => {
    if (sel.split(',').map((s) => s.replace(/\s+/g, ' ').trim()).includes(selector)) out.push(body);
    return m;
  });
  return out.join(';');
};
const gap = (b) => (b.match(/(?:^|[;\s])gap:\s*([^;]+)/) || [])[1].trim();

async function expand(id, tweak) {
  const env = await boot();
  const { w, d, server } = env;
  tweak(server.certs.find((c) => c.id === id));
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  w.blacksmithCerts.state.attention = false; await w.blacksmithCerts.refresh({ announce: true }); await sleep(80);
  d.querySelector(`[data-cert-id="${id}"] .cert-expand-btn`).click(); await sleep(30);
  return { ...env, box: d.querySelector('.cert-expansion') };
}

t('the warnings and notices of a row are one list with one shared rule', async () => {
  const { w, box } = await expand('c2', (c) => {
    c.findings = [
      { code: 'a', severity: 'warning', message: 'A warning.' },
      { code: 'b', severity: 'info', message: 'A notice.' }
    ];
    c.rotation = { allowed: true, modes: ['redeploy'], reasons: [] };
  });
  try {
    const list = box.querySelector('ul.cert-drawer-callouts');
    expect(list).not.toBeNull();
    expect(list.getAttribute('aria-label')).toBe('Warnings and notices');
    const items = [...list.children];
    expect(items.map((x) => x.tagName)).toEqual(['LI', 'LI']);
    expect(items.map((x) => x.querySelector('.cert-callout-label').textContent)).toEqual(['Warning', 'Notice']);
    expect(list.querySelector('.cert-notice')).toBeNull();
    expect(bodies('blacksmith.css', '.cert-drawer-callouts')).toMatch(/border-left:/);
    expect(bodies('blacksmith.css', '.cert-drawer-callouts .cert-callout')).not.toMatch(/border/);
  } finally { w.close(); }
});

t('a row with an error finding draws the rule in the danger color', async () => {
  const { w, box } = await expand('c2', (c) => {
    c.findings = [
      { code: 'a', severity: 'error', message: 'An error.' },
      { code: 'b', severity: 'warning', message: 'A warning.' }
    ];
  });
  try {
    const list = box.querySelector('ul.cert-drawer-callouts');
    expect(list.classList.contains('cert-drawer-callouts--danger')).toBe(true);
    expect(bodies('blacksmith.css', '.cert-drawer-callouts--danger')).toMatch(/border-left-color:\s*var\(--danger\)/);
  } finally { w.close(); }
});

t('a row with only warnings keeps the amber rule', async () => {
  const { w, box } = await expand('c2', (c) => {
    c.findings = [{ code: 'b', severity: 'warning', message: 'A warning.' }];
  });
  try {
    const list = box.querySelector('ul.cert-drawer-callouts');
    expect(list.classList.contains('cert-drawer-callouts--danger')).toBe(false);
    expect(list.classList.contains('cert-drawer-callouts--warn')).toBe(true);
  } finally { w.close(); }
});

t('CredHub Details reads as the label and value grid of the task header', () => {
  const grid = bodies('forge-theme.css', '.cert-expansion .cert-credhub-table tbody');
  const facts = bodies('forge-theme.css', '.entity-facts');
  expect(grid).toMatch(/display:\s*grid/);
  expect(grid).toMatch(/grid-template-columns:\s*repeat\(2,\s*minmax\(0,\s*1fr\)\)/);
  expect(gap(grid)).toBe(gap(facts));
  expect(bodies('forge-theme.css', '.cert-expansion .cert-credhub-table th')).toMatch(/font-size:\s*\.75rem/);
  expect(bodies('forge-theme.css', '.cert-expansion .cert-credhub-table td')).toMatch(/margin:\s*2px 0 0|padding:\s*2px 0 0/);
});

t('the CredHub table keeps its table roles once it is laid out as a grid', async () => {
  const { w, box } = await expand('c2', (c) => {
    c.key = '/ws-bosh/valkey/var2';
    c.observations = [{ source: 'credhub', owner: { kind: 'instance' }, locator: '/ws-bosh/valkey/var2', version_id: 'v1', state: 'ok' }];
  });
  try {
    const table = box.querySelector('.cert-credhub-table');
    expect(table.getAttribute('role')).toBe('table');
    expect(table.querySelector('tr').getAttribute('role')).toBe('row');
    expect(table.querySelector('th').getAttribute('role')).toBe('rowheader');
    expect(table.querySelector('td').getAttribute('role')).toBe('cell');
  } finally { w.close(); }
});
