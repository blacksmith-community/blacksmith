const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const { boot, sleep, now, day, iso } = H.fleet;

const cert = (id, days, extra = {}) => Object.assign({
  id, key: `/secret/${id}`, variable: `var_${id}`, kind: 'leaf', common_name: `cn-${id}`, issuer: 'CN=Services CA',
  owners: [{ kind: 'instance', instance_id: `inst-${id}`, instance_name: `owner ${id}`, service_name: 'valkey', plan_name: 'small' }],
  affected_instances: 1, effective_not_after: iso(now + days * day), days_left: days,
  expiry: days < 0 ? 'expired' : 'ok', health: 'ok', attention: false, observations: [], findings: [],
  coverage: { expected_listeners: 1, observed_listeners: 1, failed_listeners: 0, complete: true, stale: false },
  rotation: { allowed: false, reasons: [] },
}, extra);

const open = async (certs) => {
  const env = await boot();
  env.server.certs = certs;
  env.server.caps.credhub = { state: 'configured' };
  await env.w.blacksmithNav.switchTab('certificates');
  await sleep(150);
  return env;
};
const pill = (d) => d.getElementById('cert-next-expiry');

t('next expiry pill and the 90 day section', async () => {
  // N days, naming the soonest unexpired certificate and ignoring an expired one
  let { d } = await open([cert('old', -10), cert('b', 12), cert('a', 5), cert('c', 200)]);
  let p = pill(d);
  ok(!!p && p.textContent === 'Next expiry in 5 days', 'N days: ' + (p && p.textContent));
  ok(/owner a/.test(p.title) && /var_a/.test(p.title), 'title names instance and variable: ' + p.title);
  ok(p === d.querySelector('#cert-strips').firstElementChild, 'the pill comes first in the summary');
  ok(p.tagName === 'SPAN' && !p.matches('button, [tabindex], [role=button]') && !p.hasAttribute('aria-pressed'), 'not a button, not focusable, never pressed');
  const before = d.getElementById('cert-fleet-timeline').hidden;
  p.click(); await sleep(60);
  ok(p.textContent === 'Next expiry in 5 days' && d.querySelectorAll('.cert-strip-btn[aria-pressed="true"]').length === 0, 'clicking it toggles nothing');
  ok(before === false && !d.getElementById('cert-fleet-timeline').hidden && !!d.querySelector('#cert-fleet-timeline .cert-tl-btn'), '90 day section is present when something expires within 90 days');
  ok(d.querySelector('#cert-fleet-timeline .cert-timeline-title').textContent === 'Expiring in the next 90 days', 'section heading unchanged');

  // 1 day
  ({ d } = await open([cert('a', 1), cert('b', 40)]));
  ok(pill(d).textContent === 'Next expiry in 1 day', 'singular: ' + pill(d).textContent);

  // today
  ({ d } = await open([cert('a', 0), cert('b', 40)]));
  ok(pill(d).textContent === 'Next expiry today', 'today: ' + pill(d).textContent);

  // none upcoming (only expired and unknown), and nothing within 90 days
  ({ d } = await open([cert('old', -3), cert('unk', 0, { effective_not_after: undefined, days_left: undefined, expiry: 'unknown' })]));
  ok(pill(d).textContent === 'No upcoming expiries', 'none: ' + pill(d).textContent);
  ok(d.getElementById('cert-fleet-timeline').hidden === true, '90 day section is absent when nothing is due');

  // everything beyond 90 days: the pill still counts, the section is gone
  ({ d } = await open([cert('a', 100), cert('b', 400)]));
  ok(pill(d).textContent === 'Next expiry in 100 days', 'beyond 90 days: ' + pill(d).textContent);
  ok(d.getElementById('cert-fleet-timeline').hidden === true, '90 day section is absent when the soonest is past 90 days');
  ok(!/Nothing expires in the next 90 days/.test(d.getElementById('certificates').textContent), 'the duplicate line is gone');

  // the 30 day banner is unchanged
  ok(/^Nothing expires within 30 days\./.test((d.getElementById('cert-reassurance') || { textContent: '' }).textContent), '30 day banner kept');
  expect(failures).toEqual([]);
});
