const { test, expect, afterEach } = require('bun:test');
const H = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');

const { boot, until, TEST_LIMIT } = H.main;
const t = JSDOM ? (n, f, l) => test(n, f, l === undefined ? TEST_LIMIT : l) : test.skip;

const envs = [];
afterEach(() => { envs.splice(0).forEach((e) => { expect(e.alerts).toEqual([]); e.w.close(); }); });

// A table of n rows named r1 to rn, with a search box wired as the page wires it.
const build = (n, attrs = 'data-row-cap="100" data-row-cap-noun="things"') => {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { d, w } = env;
  const host = d.createElement('div');
  const rows = Array.from({ length: n }, (_, i) => `<tr><td>r${i + 1}</td><td>${i % 2 ? 'odd' : 'even'}</td></tr>`).join('');
  host.innerHTML = `<div class="scroll"><table class="cap-table" ${attrs}><thead><tr><th>Name</th><th>Kind</th></tr></thead><tbody>${rows}</tbody></table></div>`;
  d.body.appendChild(host);
  w.__T.initializeAllTables();
  return { env, d, w, host, table: host.querySelector('table') };
};

const visible = (table) => [...table.tBodies[0].rows].filter((r) => r.style.display !== 'none' && !r.classList.contains('row-capped'));
const bar = (host) => host.querySelector('.row-cap');
// The bar a reader sees. A table within its cap keeps a hidden bar.
const seen = (host) => { const b = bar(host); return b && !b.hidden ? b : null; };
const settle = () => new Promise((r) => setTimeout(r, 0));

t('a table over the cap shows the first 100 rows, a status, and a Show more button', () => {
  const { host, table } = build(250);
  expect(visible(table).length).toBe(100);
  expect(visible(table)[0].cells[0].textContent).toBe('r1');
  const status = bar(host).querySelector('[role="status"]');
  expect(status.textContent).toBe('Showing 100 of 250 things');
  expect(status.getAttribute('aria-live')).toBe('polite');
  const button = bar(host).querySelector('button');
  expect(button.textContent).toBe('Show 100 more');
  expect(button.getAttribute('type')).toBe('button');
  expect(button.dataset.keepKey).toBeTruthy();
});

t('a table at or under the cap shows every row and no bar', () => {
  const { host, table } = build(100);
  expect(visible(table).length).toBe(100);
  expect(seen(host)).toBeNull();
  const small = build(3);
  expect(visible(small.table).length).toBe(3);
  expect(seen(small.host)).toBeNull();
});

t('a table without the attribute is never capped', () => {
  const { host, table } = build(250, '');
  expect(visible(table).length).toBe(250);
  expect(seen(host)).toBeNull();
});

t('Show more reveals the next batch, and the last click hides the button and keeps focus in the bar', () => {
  const { host, table } = build(250);
  const button = bar(host).querySelector('button');
  button.focus();
  button.click();
  expect(visible(table).length).toBe(200);
  expect(bar(host).querySelector('[role="status"]').textContent).toBe('Showing 200 of 250 things');
  expect(bar(host).querySelector('button').textContent).toBe('Show 50 more');
  bar(host).querySelector('button').click();
  expect(visible(table).length).toBe(250);
  expect(bar(host).querySelector('[role="status"]').textContent).toBe('Showing 250 of 250 things');
  expect(bar(host).querySelector('button').hidden).toBe(true);
  expect(bar(host).contains(table.ownerDocument.activeElement)).toBe(true);
});

t('a search covers the whole data set, not just the visible rows, and the status counts the matches', async () => {
  const { host, table } = build(250);
  // r250 is past the first 100 rows. Hide every row that does not match, as the page's search does.
  [...table.tBodies[0].rows].forEach((row) => { row.style.display = row.cells[0].textContent === 'r250' ? '' : 'none'; });
  await settle();
  expect(visible(table).map((r) => r.cells[0].textContent)).toEqual(['r250']);
  expect(seen(host)).toBeNull();
  // Clearing the search brings the cap back.
  [...table.tBodies[0].rows].forEach((row) => { row.style.display = ''; });
  await settle();
  expect(visible(table).length).toBe(100);
  expect(bar(host).querySelector('[role="status"]').textContent).toBe('Showing 100 of 250 things');
});

t('a filter that leaves more than the cap is capped and counted', async () => {
  const { host, table } = build(250);
  [...table.tBodies[0].rows].forEach((row) => { row.style.display = row.cells[1].textContent === 'odd' ? '' : 'none'; });
  await settle();
  expect(visible(table).length).toBe(100);
  expect(bar(host).querySelector('[role="status"]').textContent).toBe('Showing 100 of 125 things');
});

t('sorting orders the whole data set and the cap then takes the first rows', async () => {
  const { host, table } = build(250);
  const header = table.querySelector('th');
  header.click();
  header.click();
  await settle();
  const names = visible(table).map((r) => r.cells[0].textContent);
  expect(names.length).toBe(100);
  // Descending by name puts r99 first among the strings that sort numerically, so the largest number leads.
  expect(names[0]).toBe('r250');
  expect(bar(host).querySelector('[role="status"]').textContent).toBe('Showing 100 of 250 things');
});

t('initializing twice adds one bar', () => {
  const { w, host } = build(250);
  w.__T.initializeAllTables();
  expect(host.querySelectorAll('.row-cap').length).toBe(1);
});

t('the instance overview caps 150 instances at 100 rows with a status', async () => {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { w, d } = env;
  const instances = {};
  for (let i = 0; i < 150; i++) instances['id' + i] = { service_id: 'redis', plan: { name: 'small' }, instance_name: 'cache-' + i, vm_status: 'running' };
  const host = d.createElement('div');
  d.body.appendChild(host);
  host.innerHTML = w.__T.renderServicesTemplate(instances);
  w.__T.initializeAllTables();
  const table = host.querySelector('.instances-overview-table');
  expect(visible(table).length).toBe(100);
  expect(seen(host).querySelector('[role="status"]').textContent).toBe('Showing 100 of 150 instances');
  seen(host).querySelector('button').click();
  expect(visible(table).length).toBe(150);
});

t('an instance log table caps its lines the same way', () => {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { w, d } = env;
  const lines = Array.from({ length: 130 }, (_, i) => `2026-11-14T10:00:00Z INFO line ${i + 1}`).join('\n');
  const host = d.createElement('div');
  d.body.appendChild(host);
  host.innerHTML = w.__T.formatJobLogContent('redis/0', { 'redis.log': lines });
  w.__T.initializeAllTables();
  const table = host.querySelector('.instance-logs-table');
  expect(table.dataset.rowCap).toBe('100');
  expect(visible(table).length).toBe(100);
  expect(seen(host).querySelector('[role="status"]').textContent).toBe('Showing 100 of 130 log entries');
});

t('group headings are not counted, and an expanded row follows its certificate', async () => {
  const { host, table } = build(0, 'data-row-cap="2" data-row-cap-noun="certificates" data-row-cap-skip=".group" data-row-cap-detail=".more"');
  table.tBodies[0].innerHTML = '<tr class="group"><td>g</td><td></td></tr><tr><td>a</td><td></td></tr><tr class="more"><td>a detail</td><td></td></tr><tr><td>b</td><td></td></tr><tr class="group"><td>h</td><td></td></tr><tr><td>c</td><td></td></tr><tr class="more"><td>c detail</td><td></td></tr>';
  await settle();
  const shown = [...table.tBodies[0].rows].filter((r) => !r.classList.contains('row-capped')).map((r) => r.cells[0].textContent);
  expect(shown).toEqual(['g', 'a', 'a detail', 'b', 'h']);
  expect(seen(host).querySelector('[role="status"]').textContent).toBe('Showing 2 of 3 certificates');
});

t('a re-rendered table keeps the same key and the rows the reader revealed', () => {
  const first = build(250);
  const key = bar(first.host).querySelector('button').dataset.keepKey;
  expect(key).toBe('row-cap:cap-table');
  bar(first.host).querySelector('button').click();
  expect(visible(first.table).length).toBe(200);
  // The page renders the table again, as it does on a refresh.
  const { d, w } = first;
  first.host.remove();
  const host = d.createElement('div');
  host.innerHTML = first.host.innerHTML.replace(/<div class="row-cap"[\s\S]*$/, '');
  host.innerHTML = `<div class="scroll"><table class="cap-table" data-row-cap="100" data-row-cap-noun="things"><thead><tr><th>Name</th></tr></thead><tbody>${Array.from({ length: 250 }, (_, i) => `<tr><td>r${i + 1}</td></tr>`).join('')}</tbody></table></div>`;
  d.body.appendChild(host);
  w.__T.initializeAllTables();
  expect(bar(host).querySelector('button').dataset.keepKey).toBe(key);
  expect(visible(host.querySelector('table')).length).toBe(200);
  expect(bar(host).querySelector('[role="status"]').textContent).toBe('Showing 200 of 250 things');
});

t('the certificate table caps 130 certificates at 100 rows', async () => {
  const { boot: bootFleet, iso, now, day } = H.fleet;
  const { w, d, server } = await bootFleet();
  try {
    server.certs = Array.from({ length: 130 }, (_, i) => ({ id: 'C' + i, key: '/ws/C' + i, variable: 'C' + i, kind: 'leaf', common_name: 'cn-' + i, issuer: 'CN=X', owners: [{ kind: 'instance', instance_id: 'i1', instance_name: 'one', service_name: 'valkey', plan_name: 's' }], affected_instances: 1, effective_not_after: iso(now + 40 * day), days_left: 40, expiry: 'ok', health: 'ok', attention: true, findings: [], observations: [], coverage: { expected_listeners: 1, observed_listeners: 1, failed_listeners: 0, complete: true }, rotation: { allowed: true }, versions: {} }));
    d.dispatchEvent(new w.Event('DOMContentLoaded'));
    await w.blacksmithNav.switchTab('certificates');
    const capped = () => { const table = d.getElementById('cert-table'); return !!table && visible(table).filter((r) => r.classList.contains('cert-row')).length === 100; };
    await until(capped, 'the certificate table to show its first 100 rows');
    const table = d.getElementById('cert-table');
    expect(visible(table).filter((r) => r.classList.contains('cert-row')).length).toBe(100);
    expect(seen(d.body).querySelector('[role="status"]').textContent).toBe('Showing 100 of 130 certificates');
  } finally { w.close(); }
});
