const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');
const t = JSDOM ? test : test.skip;
const { boot, openInstanceTab, sleep } = H.instance;

const strip = (css) => css.replace(/\/\*[\s\S]*?\*\//g, '');
const bodies = (file, selector) => {
  const out = [];
  strip(fs.readFileSync(path.join(H.UI, file), 'utf8')).replace(/([^{}]+)\{([^{}]*)\}/g, (m, sel, body) => {
    if (sel.split(',').map((s) => s.replace(/\s+/g, ' ').trim()).includes(selector)) out.push(body);
    return m;
  });
  return out.join(';');
};

const FILES = [
  { name: 'ca.pem', path: '/etc/ssl/certs/ca.pem' },
  { name: 'svc.pem', path: '/var/vcap/jobs/x/svc.pem' }
];
const open = async () => {
  const env = boot({ oat: true });
  const { w, d, json } = env;
  w.__routes = [
    [/certificates\/trusted\/file/, (u, o) => json(200, { success: true, data: { certificates: [] } })],
    [/certificates\/trusted/, () => json(200, { success: true, data: { files: FILES } })],
    [/certificates\/instances\//, () => json(404, { success: false })]
  ];
  const host = await openInstanceTab(w, d, 'i1');
  await sleep(30);
  return { ...env, host };
};

t('each sub-tab panel is one frame holding a heading, a line, and a row of actions', async () => {
  const { w, host } = await open();
  const panels = [...host.querySelectorAll('bs-tabs > [role="tabpanel"]')];
  expect(panels.map((p) => p.dataset.tab)).toEqual(['overview', 'manifest', 'trusted', 'endpoint']);
  for (const p of panels.slice(1)) {
    expect(p.querySelector('.operation-content, table.operation-form, .testing-history, .history-header')).toBeNull();
    const head = p.querySelector(':scope > .cert-source-head');
    expect(head).not.toBeNull();
    expect(head.querySelector(':scope > h5')).not.toBeNull();
    expect(head.querySelectorAll(':scope > p').length).toBe(1);
    expect(head.querySelector(':scope > .cert-source-actions [data-cert-action]')).not.toBeNull();
  }
  expect(panels[0].querySelector('.operation-content')).toBeNull();
  expect(host.querySelector('.service-testing-container')).toBeNull();
  w.close();
});

t('the actions keep their ids and handlers', async () => {
  const { w, host } = await open();
  expect([...host.querySelectorAll('[data-cert-action]')].map((b) => b.dataset.certAction)).toEqual(['fetch-manifest', 'fetch-vm', 'fetch-service-endpoint']);
  for (const id of ['manifest-certificates-results-i1', 'manifest-certificates-list-i1', 'vm-certificates-results-i1', 'vm-certificates-list-i1', 'service-endpoint-address-i1', 'service-endpoint-results-i1', 'service-endpoint-details-i1']) {
    expect(host.querySelector('#' + id)).not.toBeNull();
  }
  w.close();
});

t('the inner result boxes lose their border inside a panel', () => {
  const box = bodies('forge-theme.css', '.cert-source-pane .cert-list-container');
  expect(box).toMatch(/border:\s*0|border:\s*none/);
});

t('trusted certificate files are one table with a Load Certificate action per row', async () => {
  const { w, d, host, calls } = await open();
  host.querySelector('[data-cert-action="fetch-vm"]').click();
  await sleep(60);
  const table = host.querySelector('#vm-certificates-list-i1 table.cert-files-table');
  expect(table).not.toBeNull();
  expect([...table.querySelectorAll('thead th')].map((x) => x.textContent)).toEqual(['File', 'Path', 'Actions']);
  const rows = [...table.querySelectorAll('tbody.cert-file-item > tr.cert-file-header')];
  expect(rows.map((r) => r.children[0].textContent)).toEqual(['ca.pem', 'svc.pem']);
  expect(rows[0].children[1].classList.contains('cert-file-path')).toBe(true);
  const button = rows[0].children[2].querySelector('button.load-cert-btn');
  expect(button.classList.contains('btn-secondary')).toBe(true);
  expect(button.textContent.trim()).toBe('Load Certificate');
  expect(bodies('forge-theme.css', '.cert-files-table .cert-file-path')).toMatch(/monospace/);
  expect(host.querySelector('.cert-files-list, .cert-files-header')).toBeNull();
  expect(calls.every((c) => !/^https?:\/\/(?!localhost)/.test(String(c)))).toBe(true);
  w.close();
});

t('a loaded certificate opens in the expansion row of its file', async () => {
  const { w, host } = await open();
  host.querySelector('[data-cert-action="fetch-vm"]').click();
  await sleep(60);
  const item = host.querySelector('tbody.cert-file-item');
  const button = item.querySelector('button.load-cert-btn');
  button.click();
  await sleep(60);
  const content = item.querySelector('tr.cert-file-expansion > td > .cert-file-content');
  expect(content).not.toBeNull();
  expect(content.style.display).toBe('block');
  expect(content.textContent).toContain('No certificate data found');
  expect(button.disabled).toBe(false);
  const css = bodies('forge-theme.css', '.cert-files-table .cert-file-content');
  expect(css).toMatch(/border:\s*0|border:\s*none/);
  w.close();
});
