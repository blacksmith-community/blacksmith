const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');
const { pinZone } = require('../fixed-zone.js');
pinZone('UTC');

const t = JSDOM ? test : test.skip;
const { boot: bootFleet, sleep: fleetSleep } = H.fleet;
const { boot, openInstanceTab, sleep, now, iso } = H.instance;

t('the certificate detail Valid from and Valid to carry the ISO value in a title', () => {
  const { w } = H.instance.boot();
  try {
    const view = w.__T.CertView;
    const node = view.detail({ version: 3, serialNumber: '01', subject: { commonName: 'a' }, issuer: { commonName: 'b' }, notBefore: '2026-10-11T09:14:30Z', notAfter: '2027-10-11T09:14:30Z' });
    const rows = Object.fromEntries([...node.querySelectorAll('tr')].map((tr) => [tr.querySelector('th').textContent, tr.querySelector('.cert-field-text')]));
    expect(rows['Valid from'].getAttribute('title')).toBe('2026-10-11T09:14:30.000Z');
    expect(rows['Valid to'].getAttribute('title')).toBe('2027-10-11T09:14:30.000Z');
    expect(rows['Version'].hasAttribute('title')).toBe(false);
  } finally { w.close(); }
});

t('a missing Valid from has no title', () => {
  const { w } = H.instance.boot();
  try {
    const node = w.__T.CertView.detail({ version: 3, serialNumber: '01', subject: { commonName: 'a' }, issuer: { commonName: 'b' }, notAfter: '2027-10-11T09:14:30Z' });
    const from = [...node.querySelectorAll('tr')].find((tr) => tr.querySelector('th').textContent === 'Valid from');
    expect(from.querySelector('.cert-field-text').hasAttribute('title')).toBe(false);
  } finally { w.close(); }
});

t('the failed-scan sentence carries the ISO times in a title', async () => {
  const { w, d, json } = boot({ oat: true });
  try {
    const body = {
      instance: { id: 'i1', name: 'valkey-1', deployment: 'd', service_name: 'valkey', plan_name: 'small' },
      scan: { scanned_at: iso(now), last_success_at: iso(now - 3600e3), stale: true, error: 'scan timed out after 60s' },
      certificates: [{ id: 'c1', key: '/x/tls', variable: 'tls', kind: 'leaf', common_name: 'cn', effective_not_after: iso(now + 5 * 86400e3), days_left: 5, expiry: 'critical', health: 'ok', attention: false, coverage: { complete: true }, observations: [] }],
      rotatable_variables: [], summary: { certificates: 1 }, capabilities: {},
    };
    w.__routes = [[/certificates\/instances\//, () => json(200, body)]];
    await openInstanceTab(w, d, 'i1');
    await sleep(40);
    const text = d.querySelector('#service-cert-overview-i1 .cert-notice-text');
    expect(text).not.toBeNull();
    expect(text.textContent).toContain('failed');
    const title = text.getAttribute('title');
    expect(title).toContain(new Date(now).toISOString());
    expect(title).toContain(new Date(now - 3600e3).toISOString());
  } finally { w.close(); }
});

t('a skeleton summary drops the Scanned title of the screen it replaces', async () => {
  const env = await bootFleet();
  try {
    await env.w.blacksmithNav.switchTab('certificates');
    await fleetSleep(150);
    const summary = env.d.getElementById('cert-fleet-summary');
    expect(summary.getAttribute('title')).toMatch(/^Scanned \d{4}-/);
    env.server.mode = 'never';
    await env.w.blacksmithCerts.refresh({ announce: false });
    await fleetSleep(80);
    expect(summary.textContent).toContain('Scanning 12 of 58');
    expect(summary.hasAttribute('title')).toBe(false);
  } finally { env.w.close(); }
});
