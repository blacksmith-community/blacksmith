const { test, expect } = require('bun:test');
const { main:{boot,idle,until,TEST_LIMIT} } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');

const t=JSDOM?(n,f,l)=>test(n,f,l===undefined?TEST_LIMIT:l):test.skip;

const ID = 'inst-1';
// The deployment's events, newest first, every one by a different action so a search can pick one out.
const history = (total) => Array.from({ length: total }, (_, i) => { const id = total - i; return { id: String(id), time: '2026-10-31T10:00:00Z', user: 'admin', action: `act-${id}`, object_type: 'deployment', object_name: 'valkey-1', task_id: String(1000 + id) }; });

// The broker's page of events. It lists `deep` events deep and says so at the end. The page only
// follows the ceiling flag, so a test can use a shallow ceiling and stay fast.
const serve = (all, deep = 2000) => (u) => {
  const q = new URL(u, 'http://localhost').searchParams;
  const limit = Number(q.get('limit')) || 100;
  const offset = Number(q.get('offset')) || 0;
  const room = Math.min(limit, deep - offset);
  if (room <= 0) return { events: [], offset, limit: 0, has_more: true, ceiling: true };
  const hasMore = all.length > offset + room;
  return { events: all.slice(offset, offset + room), offset, limit: room, has_more: hasMore, ceiling: hasMore && offset + room >= deep };
};

const mount = async (all, deep) => {
  const { w, d, calls } = boot();
  await idle(w);
  const host = d.createElement('div');
  d.body.appendChild(host);
  w.__routes.push([`/b/${ID}/events`, (u, o, json) => json(200, serve(all, deep)(u))]);
  host.innerHTML = await w.__T.fetchServiceDetail(ID, 'events');
  return { w, d, calls, host };
};
const rows = (host) => Array.from(host.querySelector('.events-table')?.tBodies[0]?.rows || []);
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
    await idle(w);
    expect(calls.filter((c) => c.includes(`/b/${ID}/events`)).pop()).toContain('offset=100');
    expect(rows(host).length).toBe(200);
    expect(rows(host)[100].querySelector('.event-action').textContent).toBe('act-150');
    expect(status(host).textContent).toBe('Showing 200 events. Older events are available.');
    more(host).click();
    await idle(w);
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
    await idle(w);
    const root = d.getElementById('services');
    root.classList.add('active');
    root.innerHTML = '<div class="entity-workspace" data-section="events"><div class="detail-content"><div class="events-table-container"><button id="refresh"><span>Refresh</span></button></div></div></div>';
    w.currentInstanceInfo = { id: ID };
    w.__routes.push([`/b/${ID}/events`, (u, o, json) => json(200, serve(history(250))(u))]);
    await w.refreshServiceInstanceEvents(ID, { currentTarget: d.getElementById('refresh') });
    await idle(w);
    expect(calls.filter((c) => c.includes(`/b/${ID}/events`)).pop()).toContain('paged=1');
    const host = root;
    expect(rows(host).length).toBe(100);
    more(host).click();
    await idle(w);
    expect(rows(host).length).toBe(200);
    const input = d.getElementById('search-events-table-service-events');
    input.value = 'act-120';
    input.dispatchEvent(new w.Event('input'));
    const shown = rows(host).filter((r) => r.style.display !== 'none');
    expect(shown.length).toBe(1);
    expect(shown[0].querySelector('.event-action').textContent).toBe('act-120');
    // Rows added while a search is active are filtered by it too.
    more(host).click();
    await idle(w);
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
    await idle(w);
    const ids = rows(host).map((r) => r.querySelector('.event-action').textContent);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids.length).toBe(199);
  } finally { w.close(); }
});

t('a bare array from an older broker still renders and offers no button', async () => {
  const { w, d } = boot();
  try {
    await idle(w);
    const host = d.createElement('div');
    d.body.appendChild(host);
    w.__routes.push([`/b/${ID}/events`, (u, o, json) => json(200, history(5))]);
    host.innerHTML = await w.__T.fetchServiceDetail(ID, 'events');
    expect(rows(host).length).toBe(5);
    expect(more(host)).toBeNull();
  } finally { w.close(); }
});

t('rows added by Load more fall into the sort the table already has', async () => {
  const { w, d, host } = await mount(history(250));
  try {
    w.__T.initializeAllTables();
    const header = Array.from(host.querySelectorAll('.events-table th')).find((th) => th.textContent === 'Task');
    header.click();
    await idle(w);
    const tasks = () => rows(host).map((r) => Number(r.querySelector('.event-task').textContent));
    const sorted = tasks();
    expect(sorted).toEqual([...sorted].sort((a, b) => a - b));
    expect(sorted[0]).toBe(1151);
    more(host).click();
    await idle(w);
    const after = tasks();
    expect(after.length).toBe(200);
    expect(after).toEqual([...after].sort((a, b) => a - b));
    expect(after[0]).toBe(1051);
  } finally { w.close(); }
});

t('at the broker ceiling the button is gone and the status says older events are not listed', async () => {
  const { w, host } = await mount(history(500), 300);
  try {
    for (let page = 1; page < 3; page++) {
      const before = rows(host).length;
      more(host).click();
      await until(() => rows(host).length !== before, 'the next page of events');
      await idle(w);
    }
    expect(rows(host).length).toBe(300);
    expect(more(host)).toBeNull();
    expect(status(host).textContent).toContain('Older events are not listed here');
    expect(status(host).getAttribute('role')).toBe('status');
  } finally { w.close(); }
});

t('a page that adds no new rows ends the list instead of leaving a button that loads nothing', async () => {
  const { w, host } = await mount(history(250));
  try {
    // The broker keeps answering the same page, as an old broker that clamps the offset would.
    w.__routes.unshift([`/b/${ID}/events`, (u, o, json) => json(200, { events: history(250).slice(0, 100), has_more: true })]);
    more(host).click();
    await idle(w);
    expect(rows(host).length).toBe(100);
    expect(more(host)).toBeNull();
    expect(status(host).textContent).toContain('not listed here');
  } finally { w.close(); }
});

t('a failed Load more says so in plain words and keeps the button', async () => {
  const { w, host } = await mount(history(250));
  try {
    w.__routes.unshift([`/b/${ID}/events`, (u, o, json) => json(500, { error: 'boom' })]);
    more(host).click();
    await until(() => host.querySelector('.events-load-error'), 'the Load more error');
    await idle(w);
    const note = host.querySelector('.events-load-error');
    expect(note.textContent).toBe('Older events could not be loaded.');
    expect(note.textContent).not.toContain('HTTP');
    expect(rows(host).length).toBe(100);
    expect(more(host).disabled).toBe(false);
  } finally { w.close(); }
});
