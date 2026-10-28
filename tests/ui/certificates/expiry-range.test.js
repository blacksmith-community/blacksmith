const { test, expect, afterEach } = require('bun:test');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');

const t = JSDOM ? test : test.skip;
const { boot, sleep, now, day, iso } = H.fleet;

const cert = (id, days, when) => ({
  id, key: `/secret/${id}`, variable: `var_${id}`, kind: 'leaf', common_name: `cn-${id}`, issuer: 'CN=Services CA',
  owners: [{ kind: 'instance', instance_id: `inst-${id}`, instance_name: `owner ${id}`, service_name: 'valkey', plan_name: 'small' }],
  affected_instances: 1, effective_not_after: when || iso(now + days * day), days_left: days,
  expiry: days < 0 ? 'expired' : (days < 7 ? 'critical' : (days < 30 ? 'warning' : 'ok')), health: 'ok', attention: false, observations: [], findings: [],
  coverage: { expected_listeners: 1, observed_listeners: 1, failed_listeners: 0, complete: true, stale: false },
  rotation: { allowed: false, reasons: [] },
});
const spread = () => [cert('gone', -5), cert('a3', 3), cert('b10', 10), cert('c20', 20), cert('d45', 45), cert('e120', 120), cert('f300', 300)];
const dateOf = (days) => iso(now + days * day).slice(0, 10);

const envs = [];
afterEach(() => { while (envs.length) { const env = envs.pop(); expect(env.dialogs).toEqual([]); env.w.close(); } });

const open = async () => {
  const env = await boot();
  env.dialogs = [];
  // The fleet harness boots without a noDialogs option, so the dialogs are replaced once it is up.
  ['alert', 'confirm', 'prompt'].forEach((kind) => { env.w[kind] = (m) => { env.dialogs.push(`${kind}:${m}`); throw new Error(`window.${kind} called`); }; });
  envs.push(env);
  env.server.certs = spread();
  env.server.caps.credhub = { state: 'configured' };
  await env.w.blacksmithNav.switchTab('certificates');
  await sleep(150);
  const clear = env.d.querySelector('.cert-clear-btn');
  if (clear) { clear.click(); await sleep(150); }
  return env;
};
const rows = (d) => Array.from(d.querySelectorAll('tbody tr[data-cert-id]')).map((r) => r.dataset.certId).sort();
const toggle = (d) => d.getElementById('cert-range-toggle');
const type = async (env, from, to) => {
  const { d, w } = env;
  [['cert-range-from', from], ['cert-range-to', to]].forEach(([id, value]) => {
    const input = d.getElementById(id);
    input.value = value;
    input.dispatchEvent(new w.Event('change', { bubbles: true }));
  });
  await sleep(150);
};

t('Custom range sits beside the horizon table and opens two labelled date fields', async () => {
  const env = await open();
  const { d } = env;
  const panel = d.getElementById('cert-range');
  expect(toggle(d).textContent).toBe('Custom range');
  expect(toggle(d).getAttribute('aria-expanded')).toBe('false');
  expect(panel.hidden).toBe(true);
  toggle(d).click();
  await sleep(50);
  expect(toggle(d).getAttribute('aria-expanded')).toBe('true');
  expect(panel.hidden).toBe(false);
  for (const [id, label] of [['cert-range-from', 'From'], ['cert-range-to', 'To']]) {
    expect(d.getElementById(id).type).toBe('date');
    expect(d.querySelector(`label[for="${id}"]`).textContent).toBe(label);
  }
}, 60000);

t('both ends are inclusive UTC dates and the query carries them as an instant range', async () => {
  const env = await open();
  const { d, log } = env;
  toggle(d).click();
  await type(env, dateOf(10), dateOf(45));
  expect(rows(d)).toEqual(['b10', 'c20', 'd45']);
  const query = log.filter((l) => l.includes('expires_after')).pop();
  const params = new URL(query.split(' ')[1], 'http://x').searchParams;
  expect(params.get('expires_after')).toBe(`${dateOf(10)}T00:00:00.000Z`);
  expect(params.get('expires_before')).toBe(`${new Date(Date.parse(`${dateOf(45)}T00:00:00Z`) + day).toISOString()}`);
}, 60000);

t('an open end leaves that side open and an expired certificate counts only by its date', async () => {
  const env = await open();
  const { d } = env;
  toggle(d).click();
  await type(env, dateOf(120), '');
  expect(rows(d)).toEqual(['e120', 'f300']);
  await type(env, '', dateOf(3));
  expect(rows(d)).toEqual(['a3', 'gone']);
  await type(env, dateOf(-6), dateOf(-4));
  expect(rows(d)).toEqual(['gone']);
  await type(env, dateOf(0), dateOf(30));
  expect(rows(d)).toEqual(['a3', 'b10', 'c20']);
}, 60000);

t('the boundaries are the first and last instants of the From and To days', async () => {
  const env = await open();
  const { d } = env;
  env.server.certs = [
    cert('fromStart', 40, `${dateOf(50)}T00:00:00.000Z`), cert('beforeFrom', 40, `${dateOf(49)}T23:59:59.999Z`),
    cert('toEnd', 40, `${dateOf(60)}T23:59:59.000Z`), cert('afterTo', 40, `${dateOf(61)}T00:00:00.000Z`),
  ];
  toggle(d).click();
  await type(env, dateOf(50), dateOf(60));
  expect(rows(d)).toEqual(['fromStart', 'toEnd']);
}, 60000);

t('a reversed range marks both fields invalid until it is fixed', async () => {
  const env = await open();
  const { d } = env;
  toggle(d).click();
  await type(env, dateOf(100), dateOf(45));
  const from = d.getElementById('cert-range-from'), to = d.getElementById('cert-range-to');
  expect([from.getAttribute('aria-invalid'), to.getAttribute('aria-invalid')]).toEqual(['true', 'true']);
  await type(env, dateOf(20), dateOf(45));
  expect([from.hasAttribute('aria-invalid'), to.hasAttribute('aria-invalid')]).toEqual([false, false]);
}, 60000);

t('a From after To shows a status message, sends no request, and keeps the results', async () => {
  const env = await open();
  const { d, log } = env;
  toggle(d).click();
  await type(env, dateOf(10), dateOf(45));
  const before = log.length;
  await type(env, dateOf(100), dateOf(45));
  const msg = d.getElementById('cert-range-error');
  expect(msg.getAttribute('role')).toBe('status');
  expect(msg.textContent).toContain('From');
  expect(d.getElementById('cert-range-from').getAttribute('aria-describedby')).toBe('cert-range-error');
  expect(rows(d)).toEqual(['b10', 'c20', 'd45']);
  expect(log.slice(before).filter((l) => l.includes('inventory') && l.includes('expires_after'))).toEqual([]);
  await sleep(300);
  expect(d.getElementById('cert-range-from').value).toBe(dateOf(100));
  await type(env, dateOf(20), dateOf(45));
  expect(msg.textContent).toBe('');
  expect(rows(d)).toEqual(['c20', 'd45']);
}, 60000);

t('the range shows as a chip, and removing it or emptying both fields clears it', async () => {
  const env = await open();
  const { d } = env;
  toggle(d).click();
  await type(env, dateOf(10), dateOf(45));
  const chip = () => Array.from(d.querySelectorAll('.cert-chip-text')).filter((c) => /^expires from/.test(c.textContent));
  expect(chip().map((c) => c.textContent)).toEqual([`expires from ${dateOf(10)} to ${dateOf(45)}`]);
  await type(env, '', '');
  expect(chip()).toEqual([]);
  expect(rows(d).length).toBe(7);
  await type(env, dateOf(10), dateOf(45));
  chip()[0].closest('.cert-chip').click();
  await sleep(150);
  expect(rows(d).length).toBe(7);
  expect(d.getElementById('cert-range-from').value).toBe('');
  expect(d.getElementById('cert-range').hidden).toBe(true);
}, 60000);

t('a fixed horizon and Custom range replace each other, and the horizons keep working', async () => {
  const env = await open();
  const { d } = env;
  toggle(d).click();
  await type(env, dateOf(100), dateOf(400));
  expect(rows(d)).toEqual(['e120', 'f300']);
  d.querySelector('button[data-horizon="30"]').click();
  await sleep(150);
  expect(d.getElementById('cert-range').hidden).toBe(true);
  expect(toggle(d).getAttribute('aria-expanded')).toBe('false');
  expect(rows(d)).toEqual(['a3', 'b10', 'c20']);
  toggle(d).click();
  await sleep(150);
  expect(d.querySelector('button[data-horizon="30"]').getAttribute('aria-pressed')).toBe('false');
  toggle(d).click();
  await sleep(150);
  expect(rows(d).length).toBe(7);
}, 60000);
