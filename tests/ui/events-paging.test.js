const { test, expect } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');

const t = JSDOM ? test : test.skip;

const ID = 'inst-1';
// The deployment's events, newest first, every one by a different action so a search can pick one out.
const history = (total) => Array.from({ length: total }, (_, i) => { const id = total - i; return { id: String(id), time: '2026-10-31T10:00:00Z', user: 'admin', action: `act-${id}`, object_type: 'deployment', object_name: 'valkey-1', task_id: String(1000 + id) }; });

const serve = (all) => (u) => {
  const q = new URL(u, 'http://localhost').searchParams;
  const limit = Number(q.get('limit')) || 100;
  const offset = Number(q.get('offset')) || 0;
  return { events: all.slice(offset, offset + limit), offset, limit, has_more: all.length > offset + limit };
};

const mount = async (all) => {
  const { w, d, calls } = boot();
  await sleep(50);
  const host = d.createElement('div');
  d.body.appendChild(host);
  w.__routes.push([`/b/${ID}/events`, (u, o, json) => json(200, serve(all)(u))]);
  host.innerHTML = await w.__T.fetchServiceDetail(ID, 'events');
  return { w, d, calls, host };
};
const rows = (host) => Array.from(host.querySelectorAll('.events-table tbody tr'));
const more = (host) => host.querySelector('[data-events-load-more]');
const status = (host) => host.querySelector('.events-load-status');

t('the first load asks for a page, and a full page shows Load more with a polite status', async () => {
  const { w, host, calls } = await mount(history(250));
  try {
    const url = calls.filter((c) => c.includes(`/b/${ID}/events`)).pop();
    expect(url).toContain('paged=1');
    expect(url).toContain('limit=100');
    expect(rows(host).length).toBe(100);
    expect(more(host).dataset.keepKey).toBe('events-load-more:service-events');
    expect(more(host).getAttribute('type')).toBe('button');
    expect(status(host).getAttribute('role')).toBe('status');
    expect(status(host).getAttribute('aria-live')).toBe('polite');
    expect(status(host).textContent).toBe('Showing 100 events. Older events are available.');
  } finally { w.close(); }
});

t('a short history shows no button', async () => {
  const { w, host } = await mount(history(40));
  try {
    expect(rows(host).length).toBe(40);
    expect(more(host)).toBeNull();
    expect(status(host)).toBeNull();
  } finally { w.close(); }
});

t('Load more appends the next page by offset until none is left', async () => {
  const { w, d, host, calls } = await mount(history(250));
  try {
    more(host).click();
    await sleep(100);
    expect(calls.filter((c) => c.includes(`/b/${ID}/events`)).pop()).toContain('offset=100');
    expect(rows(host).length).toBe(200);
    expect(rows(host)[100].querySelector('.event-action').textContent).toBe('act-150');
    expect(status(host).textContent).toBe('Showing 200 events. Older events are available.');
    more(host).click();
    await sleep(100);
    expect(rows(host).length).toBe(250);
    expect(more(host)).toBeNull();
    expect(status(host).textContent).toBe('Showing all 250 events.');
    expect(host.contains(d.activeElement)).toBe(true);
  } finally { w.close(); }
});

t('a search covers rows added by Load more', async () => {
  // The refresh is the page's own path that attaches the search box after it renders the table.
  const { w, d, calls } = boot();
  try {
    await sleep(50);
    const root = d.getElementById('services');
    root.classList.add('active');
    root.innerHTML = '<div class="entity-workspace" data-section="events"><div class="detail-content"><div class="events-table-container"><button id="refresh"><span>Refresh</span></button></div></div></div>';
    w.currentInstanceInfo = { id: ID };
    w.__routes.push([`/b/${ID}/events`, (u, o, json) => json(200, serve(history(250))(u))]);
    await w.refreshServiceInstanceEvents(ID, { currentTarget: d.getElementById('refresh') });
    await sleep(250);
    expect(calls.filter((c) => c.includes(`/b/${ID}/events`)).pop()).toContain('paged=1');
    const host = root;
    expect(rows(host).length).toBe(100);
    more(host).click();
    await sleep(100);
    expect(rows(host).length).toBe(200);
    const input = d.getElementById('search-events-table-service-events');
    input.value = 'act-120';
    input.dispatchEvent(new w.Event('input'));
    const shown = rows(host).filter((r) => r.style.display !== 'none');
    expect(shown.length).toBe(1);
    expect(shown[0].querySelector('.event-action').textContent).toBe('act-120');
    // Rows added while a search is active are filtered by it too.
    more(host).click();
    await sleep(100);
    expect(rows(host).filter((r) => r.style.display !== 'none').length).toBe(1);
  } finally { w.close(); }
});

t('an event that moved down a page is not added twice', async () => {
  const all = history(250);
  const { w, host } = await mount(all);
  try {
    // One newer event arrives, so the second page starts with the last row of the first.
    w.__routes.length = 0;
    w.__routes.push([`/b/${ID}/events`, (u, o, json) => json(200, { events: all.slice(99, 199), has_more: true })]);
    more(host).click();
    await sleep(100);
    const ids = rows(host).map((r) => r.querySelector('.event-action').textContent);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids.length).toBe(199);
  } finally { w.close(); }
});

t('a bare array from an older broker still renders and offers no button', async () => {
  const { w, d } = boot();
  try {
    await sleep(50);
    const host = d.createElement('div');
    d.body.appendChild(host);
    w.__routes.push([`/b/${ID}/events`, (u, o, json) => json(200, history(5))]);
    host.innerHTML = await w.__T.fetchServiceDetail(ID, 'events');
    expect(rows(host).length).toBe(5);
    expect(more(host)).toBeNull();
  } finally { w.close(); }
});
