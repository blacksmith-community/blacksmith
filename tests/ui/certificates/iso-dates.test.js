const { test, expect, afterEach } = require('bun:test');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');

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
afterEach(() => { env = null; });

const open = async (certs) => {
  env = await boot();
  env.server.certs = certs;
  env.server.caps.credhub = { state: 'configured' };
  await env.w.blacksmithNav.switchTab('certificates');
  await sleep(150);
  return env;
};

t('the Expires column shows ISO days with a UTC minute title and a sort value', async () => {
  const { d } = await open([cert('a', '2027-10-11T09:14:30Z')]);
  const cell = d.querySelector('#cert-tbody tr.cert-row .cert-col-expires');
  expect(!!cell).toBe(true);
  expect(cell.textContent).toBe('2027-10-11');
  expect(cell.getAttribute('title')).toBe('2027-10-11 09:14 UTC');
  expect(cell.getAttribute('data-sort-value')).toBe(String(Date.parse('2027-10-11T09:14:30Z')));
});

t('an unknown expiry has no sort value', async () => {
  const { d } = await open([cert('u', undefined, { effective_not_after: undefined, days_left: undefined, expiry: 'unknown' })]);
  const cell = d.querySelector('#cert-tbody tr.cert-row .cert-col-expires');
  expect(cell.textContent).toBe('Unknown');
  expect(cell.hasAttribute('data-sort-value')).toBe(false);
});

t('the inspector formats dates as ISO minutes in UTC', () => {
  const inst = H.instance.boot();
  const view = inst.w.__T.CertView;
  expect(view.formatDate('2027-10-11T09:14:30Z')).toBe('2027-10-11 09:14 UTC');
  expect(view.formatDate('')).toBe('N/A');
});
