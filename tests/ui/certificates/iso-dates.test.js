const { test, expect, afterEach } = require('bun:test');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');
const { pinZone } = require('../fixed-zone.js');
pinZone('UTC');

const t = JSDOM ? test : test.skip;
const { boot, sleep, now, day, iso } = H.fleet;

const cert = (id, when, extra = {}) => Object.assign({
  id, key: `/secret/${id}`, variable: `var_${id}`, kind: 'leaf', common_name: `cn-${id}`, issuer: 'CN=Services CA',
  owners: [{ kind: 'instance', instance_id: `inst-${id}`, instance_name: `owner ${id}`, service_name: 'valkey', plan_name: 'small' }],
  affected_instances: 1, effective_not_after: when, days_left: Math.round((Date.parse(when) - now) / day),
  expiry: 'ok', health: 'ok', attention: false, observations: [], findings: [],
  coverage: { expected_listeners: 1, observed_listeners: 1, failed_listeners: 0, complete: true, stale: false },
  rotation: { allowed: false, reasons: [] },
}, extra);

let env = null;
afterEach(() => {
  const current = env;
  env = null;
  if (!current) return;
  expect(current.alerts).toEqual([]);
  current.w.close();
});

// The fleet harness installs its console capture inside boot. Dialog calls are
// recorded and refused so a stray one fails the suite.
const open = async (certs) => {
  env = await boot();
  env.alerts = [];
  ['alert', 'confirm', 'prompt'].forEach((kind) => {
    env.w[kind] = (m) => { env.alerts.push(`${kind}:${m}`); throw new Error(`window.${kind} called`); };
  });
  env.server.certs = certs;
  env.server.caps.credhub = { state: 'configured' };
  await env.w.blacksmithNav.switchTab('certificates');
  await sleep(150);
  return env;
};

t('the Expires column shows ISO days with a timestamp title and a sort value', async () => {
  const { d } = await open([cert('a', '2027-10-11T09:14:30Z')]);
  const cell = d.querySelector('#cert-tbody tr.cert-row .cert-col-expires');
  expect(!!cell).toBe(true);
  expect(cell.textContent).toBe('2027-10-11');
  expect(cell.getAttribute('title')).toBe('2027-10-11 09:14:30 UTC');
  expect(cell.getAttribute('data-sort-value')).toBe(String(Date.parse('2027-10-11T09:14:30Z')));
});

t('an unknown expiry has no sort value', async () => {
  const { d } = await open([cert('u', undefined, { effective_not_after: undefined, days_left: undefined, expiry: 'unknown' })]);
  const cell = d.querySelector('#cert-tbody tr.cert-row .cert-col-expires');
  expect(cell.textContent).toBe('Unknown');
  expect(cell.hasAttribute('data-sort-value')).toBe(false);
});

const probe = (locator, notAfter, readAt) => ({ source: 'probe', owner: { kind: 'instance' }, locator, state: 'ok', matches: 'deployed', not_after: notAfter, observed_at: readAt });

t('the probe table sorts same-year dates in both directions', async () => {
  const observations = [
    probe('p-mid', '2027-05-02T00:00:00Z', '2026-10-02T08:00:00Z'),
    probe('p-late', '2027-11-20T00:00:00Z', '2026-10-20T08:00:00Z'),
    probe('p-early', '2027-02-15T00:00:00Z', '2026-10-11T08:00:00Z'),
    { source: 'probe', owner: { kind: 'instance' }, locator: 'p-none', state: 'failed', error: 'timed out' },
  ];
  const { d, w } = await open([cert('a', '2027-10-11T09:14:30Z', { observations })]);
  // The page starts the table observer on DOMContentLoaded, which the harness skips.
  d.dispatchEvent(new w.Event('DOMContentLoaded'));
  d.querySelector('#cert-tbody tr.cert-row').click();
  await sleep(120);
  const table = d.querySelector('#cert-fleet-drawer .cert-drawer-probes table');
  const names = () => [...table.querySelectorAll('tbody tr')].map((r) => r.querySelector('.cert-field-text').textContent).join(',');
  const headers = [...table.querySelectorAll('thead th')];
  for (const title of ['Expiry seen', 'Ran']) {
    const header = headers.find((th) => th.textContent === title);
    const sortable = header.classList.contains('sortable');
    expect(sortable).toBe(true);
    header.click();
    expect(names().endsWith('p-none')).toBe(true);
    const asc = names().split(',').slice(0, 3);
    header.click();
    expect(names().startsWith('p-none')).toBe(true);
    expect(names().split(',').slice(1)).toEqual(asc.slice().reverse());
    if (title === 'Expiry seen') expect(asc).toEqual(['p-early', 'p-mid', 'p-late']);
    else expect(asc).toEqual(['p-mid', 'p-early', 'p-late']);
  }
});

t('the inspector formats dates as timestamps', () => {
  const inst = H.instance.boot();
  const view = inst.w.__T.CertView;
  expect(view.formatDate('2027-10-11T09:14:30Z')).toBe('2027-10-11 09:14:30 UTC');
  expect(view.formatDate('')).toBe('N/A');
});
