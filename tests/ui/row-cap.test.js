const { test, expect, afterEach } = require('bun:test');
const H = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');

const t = JSDOM ? test : test.skip;
const { boot } = H.main;

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

t('the instance overview table, the CF service instances table, and the log tables carry the cap', () => {
  const { w } = boot({ noDialogs: true });
  try {
    const instances = {};
    for (let i = 0; i < 150; i++) instances['id' + i] = { service_id: 'redis', plan: { name: 'small' }, instance_name: 'cache-' + i, vm_status: 'running' };
    const host = w.document.createElement('div');
    w.document.body.appendChild(host);
    host.innerHTML = w.__T.renderBlacksmithOverview({ deployment: 'blacksmith', env: 'lab', plans: {}, instances });
    // The overview is a separate view that mounts the instance table, so the attribute lives in the source.
    const src = require('fs').readFileSync(require('path').join(__dirname, '..', '..', 'ui', 'blacksmith.js'), 'utf8');
    for (const cls of ['instances-overview-table', 'service-instances-table', 'deployment-log-table', 'debug-log-table']) {
      expect(src).toMatch(new RegExp(`<table class="${cls}"[^>]*data-row-cap="100"`));
    }
  } finally { w.close(); }
});
