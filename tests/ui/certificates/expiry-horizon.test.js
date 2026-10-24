const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const t = JSDOM ? test : test.skip;

const { boot, sleep, now, day, iso } = H.fleet;

const cert = (id, days, extra = {}) => Object.assign({
  id, key: `/secret/${id}`, variable: `var_${id}`, kind: 'leaf', common_name: `cn-${id}`, issuer: 'CN=Services CA',
  owners: [{ kind: 'instance', instance_id: `inst-${id}`, instance_name: `owner ${id}`, service_name: 'valkey', plan_name: 'small' }],
  affected_instances: 1, effective_not_after: iso(now + days * day), days_left: days,
  expiry: days < 0 ? 'expired' : (days < 7 ? 'critical' : (days < 30 ? 'warning' : 'ok')), health: 'ok', attention: false, observations: [], findings: [],
  coverage: { expected_listeners: 1, observed_listeners: 1, failed_listeners: 0, complete: true, stale: false },
  rotation: { allowed: false, reasons: [] },
}, extra);

const unknown = (id) => cert(id, 0, { effective_not_after: undefined, days_left: undefined, expiry: 'unknown' });

// Spread across the horizons, with one expired and one unknown certificate
// that must never count, and one certificate exactly one week out.
const spread = () => [
  cert('expired', -5), unknown('unk'), cert('a3', 3), cert('edge7', 7), cert('b10', 10, { attention: true }), cert('c20', 20),
  cert('d45', 45), cert('e75', 75), cert('f120', 120), cert('g300', 300), cert('h500', 500),
];

const open = async (certs, credhub = 'configured') => {
  const env = await boot();
  env.server.certs = certs;
  env.server.caps.credhub = { state: credhub };
  await env.w.blacksmithNav.switchTab('certificates');
  await sleep(150);
  return env;
};

const table = (d) => d.querySelector('#cert-fleet-notices table');
const counts = (d) => Array.from(table(d).querySelectorAll('tbody td')).map((td) => td.textContent);
const heads = (d) => Array.from(table(d).querySelectorAll('thead th')).map((th) => th.textContent);
const horizonBtn = (d, days) => d.querySelector(`#cert-fleet-notices button[data-horizon="${days}"]`);
const rows = (d) => Array.from(d.querySelectorAll('tbody tr[data-cert-id]')).map((r) => r.dataset.certId).sort();
const press = async (el) => { el.click(); await sleep(150); };

// The tab opens on the Needs attention view, so tests that count rows start
// by clearing the filters.
const openAll = async (certs, credhub) => {
  const env = await open(certs, credhub);
  await press(env.d.querySelector('.cert-clear-btn'));
  return env;
};
const horizonChips = (d) => Array.from(d.querySelectorAll('.cert-chip-text')).filter((c) => /^expires within/.test(c.textContent));

t('the horizon table shows cumulative counts for seven horizons', async () => {
  const { ok, failures } = checks();
  const { d } = await open(spread());
  const tbl = table(d);
  ok(!!tbl && tbl.tagName === 'TABLE', 'a real table');
  ok(/Certificates expiring within/.test(tbl.querySelector('caption').textContent), 'it has a caption');
  ok(heads(d).join('|') === '1 week|2 weeks|30 days|60 days|90 days|180 days|1 year', 'headers: ' + heads(d).join('|'));
  ok(counts(d).join(',') === '2,3,4,5,6,7,8', 'cumulative counts, excluding expired and unknown: ' + counts(d).join(','));
  ok(!d.getElementById('cert-reassurance') && !/Nothing (we could observe )?expires/.test(d.getElementById('cert-fleet-notices').textContent), 'the reassurance sentence is gone');
  ok(horizonBtn(d, 14).getAttribute('aria-label') === '3 certificates expire within 2 weeks. Show them.', 'label: ' + horizonBtn(d, 14).getAttribute('aria-label'));
  expect(failures).toEqual([]);
}, 60000);

t('the table renders while certificates are critical or warning', async () => {
  const { ok, failures } = checks();
  const { d, server } = await open(spread());
  ok(server.certs.some((c) => c.expiry === 'critical') && server.certs.some((c) => c.expiry === 'warning') && server.certs.some((c) => c.expiry === 'expired'), 'fixture has expired, critical, and warning');
  ok(!!table(d) && counts(d).length === 7, 'table renders beside the problems');
  expect(failures).toEqual([]);
}, 60000);

t('pressing a count filters the details table, and pressing again clears it', async () => {
  const { ok, failures } = checks();
  const { d } = await openAll(spread());
  ok(rows(d).length === 11, 'all rows to start: ' + rows(d).length);
  await press(horizonBtn(d, 14));
  ok(rows(d).join(',') === 'a3,b10,edge7', '2 weeks shows its three: ' + rows(d).join(','));
  ok(horizonBtn(d, 14).getAttribute('aria-pressed') === 'true', 'shows as pressed');
  ok(horizonChips(d).length === 1 && horizonChips(d)[0].textContent === 'expires within 14 days', 'an active-filter chip appears');
  ok(counts(d).join(',') === '2,3,4,5,6,7,8', 'counts still describe the whole fleet');

  await press(horizonBtn(d, 90));
  ok(rows(d).join(',') === 'a3,b10,c20,d45,e75,edge7', 'another horizon replaces it: ' + rows(d).join(','));
  ok(horizonBtn(d, 14).getAttribute('aria-pressed') === 'false' && horizonBtn(d, 90).getAttribute('aria-pressed') === 'true', 'only one is pressed');
  ok(horizonChips(d).length === 1 && horizonChips(d)[0].textContent === 'expires within 90 days', 'one chip, replaced');

  await press(horizonBtn(d, 90));
  ok(rows(d).length === 11, 'pressing again clears the filter: ' + rows(d).length);
  ok(horizonChips(d).length === 0, 'the chip is gone');
  expect(failures).toEqual([]);
}, 60000);

t('the horizon filter composes with another filter and clears with Clear filters', async () => {
  const { ok, failures } = checks();
  const { d } = await openAll(spread());
  const attention = Array.from(d.querySelectorAll('.cert-strip-btn')).find((b) => /^Needs attention/.test(b.getAttribute('aria-label')));
  await press(attention);
  ok(rows(d).join(',') === 'b10', 'attention alone: ' + rows(d).join(','));
  await press(horizonBtn(d, 7));
  ok(rows(d).length === 0, 'attention and 1 week share nothing: ' + rows(d).join(','));
  await press(horizonBtn(d, 30));
  ok(rows(d).join(',') === 'b10', 'attention and 30 days: ' + rows(d).join(','));
  const clear = d.querySelector('.cert-clear-btn');
  await press(clear);
  ok(rows(d).length === 11, 'Clear filters empties both: ' + rows(d).length);
  ok(horizonBtn(d, 30).getAttribute('aria-pressed') === 'false' && attention.getAttribute('aria-pressed') === 'false', 'nothing stays pressed');
  expect(failures).toEqual([]);
}, 60000);

t('a zero count is plain text', async () => {
  const { ok, failures } = checks();
  const { d } = await open([cert('far', 200), cert('farther', 400)]);
  ok(counts(d).join(',') === '0,0,0,0,0,0,1', 'counts: ' + counts(d).join(','));
  const cells = Array.from(table(d).querySelectorAll('tbody td'));
  ok(cells.slice(0, 6).every((td) => !td.querySelector('button') && td.textContent === '0'), 'zeros are not buttons');
  ok(!!cells[6].querySelector('button'), 'a non-zero count is a button');
  expect(failures).toEqual([]);
}, 60000);

t('the gap line appears only when there are gaps', async () => {
  const { ok, failures } = checks();
  let { d } = await open([cert('a', 3), cert('b', 20), unknown('unk')], 'not_configured');
  let line = d.querySelector('#cert-fleet-notices .cert-horizon-gaps');
  ok(!!line && line.textContent === 'These counts cover what we could observe. 1 certificate has an unknown expiry and CredHub is not available.', 'gap text: ' + (line && line.textContent));
  ok(table(d).parentElement.nextElementSibling === line && line.getAttribute('role') === 'status', 'the line sits directly under the table and is a status');

  ({ d } = await open([cert('a', 3), cert('b', 20)]));
  ok(!d.querySelector('#cert-fleet-notices .cert-horizon-gaps'), 'no line without gaps');
  ok(!!table(d), 'the table still renders');
  expect(failures).toEqual([]);
}, 60000);

// Freezes the browser clock and makes the fake server report the same time.
const freeze = (env, at) => {
  const clock = { at };
  env.w.Date.now = () => clock.at;
  const orig = env.w.fetch;
  env.w.fetch = (u, o) => orig(u, o).then((res) => {
    if (!String(u).startsWith('/b/certificates/inventory')) return res;
    const json = res.json;
    return Object.assign({}, res, { json: () => json().then((b) => Object.assign({}, b, { generated_at: iso(clock.at) })) });
  });
  return clock;
};

const openFrozen = async (certs) => {
  const env = await boot();
  const clock = freeze(env, now);
  env.server.certs = certs;
  env.server.caps.credhub = { state: 'configured' };
  await env.w.blacksmithNav.switchTab('certificates');
  await sleep(150);
  return { env, clock };
};

const lastQuery = (env) => {
  const url = env.log.filter((l) => /\/b\/certificates\/inventory\?/.test(l) && !/summary_only/.test(l)).pop();
  return new URL(url.replace(/^GET /, ''), 'http://x').searchParams;
};

t('the counts follow the clock between polls', async () => {
  const { ok, failures } = checks();
  const { env, clock } = await openFrozen(spread());
  const { d, w } = env;
  ok(counts(d).join(',') === '2,3,4,5,6,7,8', 'at the start: ' + counts(d).join(','));
  clock.at = now + 4 * day;
  await w.blacksmithCerts.refresh({ silent: true });
  await sleep(150);
  ok(counts(d).join(',') === '2,2,3,4,5,6,7', 'four days later, with no new data, the expired certificate has left every count: ' + counts(d).join(','));
  await press(horizonBtn(d, 30));
  const q = lastQuery(env);
  ok(q.get('expires_after') === iso(now + 4 * day + 1) && q.get('expires_before') === iso(now + 34 * day + 1), 'the click query uses the advanced time: ' + q.get('expires_after') + ' ' + q.get('expires_before'));
  expect(failures).toEqual([]);
}, 60000);

t('a horizon after a timeline week intersects the two ranges', async () => {
  const { ok, failures } = checks();
  const { env } = await openFrozen(spread());
  const { d } = env;
  await press(d.querySelector('#cert-clear-filters, .cert-clear-btn'));
  await press(d.querySelectorAll('#cert-fleet-timeline .cert-tl-btn')[1]);
  let q = lastQuery(env);
  ok(q.get('expires_after') === iso(now) && q.get('expires_before') === iso(now + 7 * day), 'week alone: ' + q.get('expires_after') + ' ' + q.get('expires_before'));
  await press(horizonBtn(d, 30));
  q = lastQuery(env);
  ok(q.get('expires_after') === iso(now + 1), 'the later start wins: ' + q.get('expires_after'));
  ok(q.get('expires_before') === iso(now + 7 * day), 'the earlier end wins: ' + q.get('expires_before'));
  await press(horizonBtn(d, 30));
  await press(horizonBtn(d, 7));
  q = lastQuery(env);
  ok(q.get('expires_before') === iso(now + 7 * day), 'a week range and 1 week end together on the boundary: ' + q.get('expires_before'));
  await press(horizonBtn(d, 7));
  await press(d.querySelector('.cert-clear-btn'));
  await press(horizonBtn(d, 7));
  q = lastQuery(env);
  ok(q.get('expires_after') === iso(now + 1) && q.get('expires_before') === iso(now + 7 * day + 1), 'a boundary horizon alone keeps the plus one millisecond: ' + q.get('expires_after') + ' ' + q.get('expires_before'));
  expect(failures).toEqual([]);
}, 60000);

t('the pressed label, focus, accent, and narrow scrolling', async () => {
  const { ok, failures } = checks();
  const { d } = await open(spread());
  const box = d.getElementById('cert-horizon');
  ok(box.classList.contains('cert-notice--error') && !box.classList.contains('cert-notice--ok'), 'the accent follows the fleet: ' + box.className);
  ok(table(d).parentElement.classList.contains('cert-horizon-scroll'), 'the table scrolls inside its own box');
  const b = horizonBtn(d, 14);
  b.focus();
  await press(b);
  const again = horizonBtn(d, 14);
  ok(again.getAttribute('aria-label') === '3 certificates expire within 2 weeks. Showing them; press to clear.', 'pressed label: ' + again.getAttribute('aria-label'));
  ok(d.activeElement === again, 'focus stays on the rebuilt button');

  const calm = await open([cert('a', 100), cert('b', 200)]);
  ok(calm.d.getElementById('cert-horizon').classList.contains('cert-notice--ok'), 'a calm fleet keeps the green accent');
  expect(failures).toEqual([]);
}, 60000);

t('a poll with unchanged data leaves the band, its focus, and its status line in place', async () => {
  const { ok, failures } = checks();
  const { env } = await openFrozen([cert('a', 3), cert('b', 20), unknown('unk')]);
  const { d, w } = env;
  const scroll = d.querySelector('.cert-horizon-scroll');
  const tbl = table(d);
  const btn = horizonBtn(d, 30);
  const gap = d.querySelector('.cert-horizon-gaps');
  scroll.scrollLeft = 40;
  scroll.focus();
  ok(d.activeElement === scroll, 'the scroll region holds focus');
  await w.blacksmithCerts.refresh({ silent: true });
  await sleep(150);
  ok(d.querySelector('.cert-horizon-scroll') === scroll && table(d) === tbl && horizonBtn(d, 30) === btn, 'the same nodes after a quiet poll');
  ok(d.querySelector('.cert-horizon-gaps') === gap, 'the status line is not re-inserted');
  ok(d.activeElement === scroll, 'focus stays on the scroll region');
  ok(scroll.scrollLeft === 40, 'scroll position stays: ' + scroll.scrollLeft);

  // Real changes still update, in place.
  btn.focus();
  env.server.certs = [cert('a', 3), cert('b', 20), cert('c', 25), unknown('unk')];
  await w.blacksmithCerts.refresh({ silent: true });
  await sleep(150);
  ok(counts(d)[2] === '3' && d.querySelector('.cert-horizon-scroll') === scroll, 'a changed count patches the cell and keeps the region: ' + counts(d).join(','));
  ok(d.activeElement === horizonBtn(d, 30), 'focus follows the rebuilt button');
  expect(failures).toEqual([]);
}, 60000);

t('a quiet poll leaves the other notices, their open state, and their focus alone', async () => {
  const { ok, failures } = checks();
  const env = await boot();
  env.server.certs = [cert('a', 3), cert('b', 20)];
  env.server.caps.credhub = { state: 'not_configured' };
  env.server.scan = Object.assign({}, env.server.scan, { errors: [{ instance_id: 'i1', error: 'timed out', last_success_at: iso(now - 3600e3) }] });
  await env.w.blacksmithNav.switchTab('certificates');
  await sleep(150);
  const { d, w } = env;
  const help = d.querySelector('#cert-fleet-notices details');
  const show = Array.from(d.querySelectorAll('#cert-fleet-notices button')).find((b) => /Show them/.test(b.textContent));
  ok(!!help && !!show, 'the CredHub help and the Show them button render');
  const helpBox = help.closest('.cert-notice');
  const showBox = show.closest('.cert-notice');
  help.open = true;
  show.focus();
  await w.blacksmithCerts.refresh({ silent: true });
  await sleep(150);
  ok(d.querySelector('#cert-fleet-notices details') === help && help.open === true, 'the opened help section stays open and is the same node');
  ok(help.closest('.cert-notice') === helpBox && show.closest('.cert-notice') === showBox && d.contains(show), 'the notice elements are the same');
  ok(d.activeElement === show, 'focus stays on Show them');
  expect(failures).toEqual([]);
}, 60000);
