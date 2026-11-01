const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');
const { pinZone } = require('../fixed-zone.js');
pinZone('UTC');

const t = JSDOM ? test : test.skip;
const { boot, sleep } = H.fleet;
const DAY = 86400000;
const STAMP = /^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d [A-Z]+$/;

async function openCert(tweak) {
  const env = await boot();
  const { w, d, server } = env;
  tweak(server.certs.find((c) => c.id === 'c2'));
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  w.blacksmithCerts.state.attention = false; await w.blacksmithCerts.refresh({ announce: true }); await sleep(80);
  d.querySelector('#cert-tbody tr.cert-row[data-cert-id="c2"]').click(); await sleep(120);
  return { ...env, dr: d.getElementById('cert-fleet-drawer') };
}

const observations = () => [
  { source: 'credhub', owner: { kind: 'instance' }, locator: '/ws-bosh/valkey/var2', sha256_fingerprint: 'abc123', matches: 'latest', state: 'ok', not_after: new Date(Date.now() + 5 * DAY).toISOString(), observed_at: '2026-10-31T08:00:00Z' },
  { source: 'probe', owner: { kind: 'instance' }, locator: '10.0.0.4:6379', matches: 'deployed', state: 'ok', not_after: new Date(Date.now() + 5 * DAY).toISOString(), observed_at: '2026-10-31T09:30:00Z' },
  { source: 'manifest', owner: { kind: 'instance' }, locator: 'instance_groups/valkey/jobs/valkey/properties/tls/cert', sha256_fingerprint: 'feedface0123', matches: 'deployed', state: 'ok', not_after: new Date(Date.now() + 5 * DAY).toISOString(), observed_at: '2026-10-31T10:45:00Z' },
];

t('every Last read value in the drawer is one full timestamp', async () => {
  const { w, dr } = await openCert((c) => { c.observations = observations(); });
  try {
    const cells = [];
    dr.querySelectorAll('tr').forEach((tr) => {
      const th = tr.querySelector('th');
      if (th && /^Last read/.test(th.textContent)) cells.push(tr.querySelector('td').textContent.trim());
    });
    const headers = Array.from(dr.querySelectorAll('th[scope="col"]'));
    headers.forEach((th, i) => {
      if (th.textContent !== 'Last read' && th.textContent !== 'Ran') return;
      const column = Array.from(th.parentElement.children).indexOf(th);
      th.closest('table').querySelectorAll('tbody tr').forEach((tr) => cells.push(tr.children[column].textContent.trim()));
    });
    expect(cells.length).toBeGreaterThanOrEqual(3);
    cells.forEach((text) => {
      expect(text).toMatch(STAMP);
      expect(text.match(/2026-10-31/g).length).toBe(1);
    });
  } finally { w.close(); }
});

t('a copy button in the other sources table is named for what it copies, not its value', async () => {
  const { w, dr, d } = await openCert((c) => { c.observations = observations(); });
  try {
    const buttons = Array.from(dr.querySelectorAll('.cert-others-table .cert-copy-btn'));
    expect(buttons.length).toBeGreaterThan(0);
    const labels = buttons.map((b) => b.getAttribute('aria-label'));
    labels.forEach((label) => {
      expect(label).toMatch(/^Copy [A-Za-z ]+$/);
      expect(label).not.toContain('feedface');
      expect(label).not.toContain('instance_groups');
    });
    w.navigator.clipboard = { writeText: () => Promise.resolve() };
    expect(Array.from(dr.querySelectorAll('.cert-probe-table:not(.cert-others-table) .cert-copy-btn')).map((b) => b.getAttribute('aria-label'))).toEqual(['Copy endpoint']);
    buttons[0].click(); await sleep(150);
    expect(d.getElementById('blacksmith-live-region').textContent).toBe(`Copied ${labels[0].replace(/^Copy /, '')}`);
  } finally { w.close(); }
});
