const { test, expect } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');

const t = JSDOM ? test : test.skip;

const { row, history, serve } = require('./tasks-stub.js');

const mount = async (all, shift, deep) => {
  const { w, d, calls } = boot();
  await sleep(50);
  const root = d.getElementById('blacksmith');
  root.classList.add('active');
  root.innerHTML = '<div class="entity-workspace" data-section="tasks"><div class="detail-content"></div></div>';
  w.__routes.push(['/b/tasks?', (u, o, json) => json(200, serve(all, shift, deep)(u))]);
  const first = serve(all, undefined, deep)('/b/tasks?type=recent&limit=200&counts=1');
  d.querySelector('#blacksmith .detail-content').innerHTML = w.__T.formatTasks(first.tasks, 'tasks', { stateCounts: first.state_counts, capped: first.state_counts_capped, hasMore: first.has_more, exact: first.exact, ceiling: first.ceiling });
  return { w, d, calls };
};
const rows = (d) => Array.from(d.querySelectorAll('.tasks-table tbody tr')).filter((r) => r.querySelector('.task-id'));
const more = (d) => d.querySelector('[data-tasks-load-more]');
const count = (d, s) => d.querySelector(`#blacksmith .toolbar-chips input[value="${s}"]`).closest('label').querySelector('.chip-count').textContent;
const note = (d) => d.querySelector('#blacksmith .tasks-count-note').textContent;

t('a table with more rows to load shows a polite status and a Load more button', () => {
  const { w, d } = boot();
  try {
    const host = d.createElement('div');
    host.innerHTML = w.__T.formatTasks([row(2, 'done'), row(1, 'done')], 'tasks', { hasMore: true });
    const button = host.querySelector('[data-tasks-load-more]');
    expect(button.tagName).toBe('BUTTON');
    expect(button.getAttribute('type')).toBe('button');
    expect(button.dataset.keepKey).toBe('tasks-load-more');
    const status = host.querySelector('.tasks-load-status');
    expect(status.getAttribute('role')).toBe('status');
    expect(status.getAttribute('aria-live')).toBe('polite');
    expect(status.textContent).toBe('Showing 2 tasks. Older tasks are available.');
    const done = host.cloneNode(false);
    done.innerHTML = w.__T.formatTasks([row(1, 'done')], 'tasks', { hasMore: false });
    expect(done.querySelector('[data-tasks-load-more]')).toBeNull();
  } finally { w.close(); }
});

t('the note explains only the 200+ cap and an unknown count, and is absent when there is nothing to explain', () => {
  const { w, d } = boot();
  try {
    const of = (meta) => { const host = d.createElement('div'); host.innerHTML = w.__T.formatTasks([row(1, 'done')], 'tasks', meta); return host.querySelector('.tasks-count-note')?.textContent ?? null; };
    const counts = { processing: 0, queued: 0, done: 200, error: 3, cancelled: 0 };
    expect(of({ stateCounts: counts, capped: ['done'] })).toBe('A count stops at 200, so 200+ means that state has at least that many tasks.');
    expect(of({ stateCounts: counts, capped: [] })).toBeNull();
    expect(of({ stateCounts: counts, capped: [], unknown: ['queued'] })).toBe('A question mark means the broker could not read that count.');
    expect(of({})).toBeNull();
    const text = of({ stateCounts: counts, capped: ['done'] });
    expect(text).not.toContain('latest');
    expect(text).not.toContain('window');
  } finally { w.close(); }
});

t('a capped chip reads 200+, an uncounted chip reads a question mark and never 0, and neither is dimmed', () => {
  const { w, d } = boot();
  try {
    const host = d.createElement('div');
    d.getElementById('blacksmith').appendChild(host);
    host.innerHTML = w.__T.formatTasks([row(1, 'done')], 'tasks', { stateCounts: { processing: 0, done: 200, error: 4 }, capped: ['done'], unknown: ['queued'] });
    const shown = (s) => host.querySelector(`input[value="${s}"]`).closest('label').querySelector('.chip-count').textContent;
    expect(shown('done')).toBe('200+');
    expect(shown('error')).toBe('4');
    expect(shown('queued')).toBe('?');
    expect(shown('cancelled')).toBe('?');
    ['queued', 'done'].forEach((s) => { host.querySelector(`input[value="${s}"]`).checked = false; });
    w.__T.syncTaskStateChips();
    expect(host.querySelector('input[value="queued"]').disabled).toBe(false);
    expect(host.querySelector('input[value="done"]').disabled).toBe(false);
    host.querySelector('input[value="processing"]').checked = false;
    w.__T.syncTaskStateChips();
    expect(host.querySelector('input[value="processing"]').disabled).toBe(true);
  } finally { w.close(); }
});

t('a failed Load more says loading more tasks failed and re-enables the button', async () => {
  const { w, d } = await mount(history(450));
  try {
    w.__routes.unshift(['/b/tasks?', (u, o, json) => json(500, { error: 'x' })]);
    more(d).click();
    await sleep(200);
    const text = d.querySelector('#blacksmith .view-refresh-error').textContent;
    expect(text).toContain('Loading more tasks failed.');
    expect(text).not.toContain('The refresh failed.');
    expect(more(d).disabled).toBe(false);
  } finally { w.close(); }
});

t('Load more asks for the next page by offset and the table then holds every row loaded', async () => {
  const { w, d, calls } = await mount(history(450));
  try {
    expect(rows(d).length).toBe(200);
    more(d).click();
    await sleep(200);
    const url = calls.filter((c) => c.includes('/b/tasks?')).pop();
    expect(url).toContain('offset=200');
    expect(url).toContain('limit=200');
    expect(url).toContain('counts=1');
    expect(url).not.toMatch(/[?&]states=/);
    expect(rows(d).length).toBe(400);
    expect(rows(d)[0].querySelector('.task-id').textContent).toBe('450');
    expect(rows(d)[399].querySelector('.task-id').textContent).toBe('51');
    expect(more(d)).not.toBeNull();
    more(d).click();
    await sleep(200);
    expect(rows(d).length).toBe(450);
    expect(more(d)).toBeNull();
  } finally { w.close(); }
});

t('chip counts come from the broker and stay the same as more rows are loaded', async () => {
  const { w, d } = await mount(history(450));
  try {
    expect(count(d, 'error') + '/' + count(d, 'done')).toBe('45/200+');
    expect(note(d)).toBe('A count stops at 200, so 200+ means that state has at least that many tasks.');
    more(d).click();
    await sleep(200);
    expect(rows(d).length).toBe(400);
    expect(count(d, 'error') + '/' + count(d, 'done')).toBe('45/200+');
    more(d).click();
    await sleep(200);
    expect(rows(d).length).toBe(450);
    expect(count(d, 'error') + '/' + count(d, 'done')).toBe('45/200+');
  } finally { w.close(); }
});

t('a task that moved down while paging is not shown twice', async () => {
  // Five newer tasks arrive before the second page is read, so five rows repeat.
  const { w, d } = await mount(history(450), () => 5);
  try {
    more(d).click();
    await sleep(200);
    const ids = rows(d).map((r) => r.querySelector('.task-id').textContent);
    expect(new Set(ids).size).toBe(ids.length);
  } finally { w.close(); }
});

// Presses one state chip alone, as the Summary link and a reader both do.
const pressOnly = (d, state) => {
  d.querySelectorAll('#blacksmith .toolbar-chips input[type="checkbox"]').forEach((box) => { box.checked = box.value === state; });
};
const lastTasksUrl = (calls) => calls.filter((c) => c.includes('/b/tasks?')).pop();
const ids = (d) => rows(d).map((r) => r.querySelector('.task-id').textContent);

t('a pressed chip asks the broker for its state, and Load more pages through that exact result', async () => {
  const { w, d, calls } = await mount(history(2500));
  try {
    pressOnly(d, 'error');
    await w.__T.refreshTasksTable();
    await sleep(150);
    const url = lastTasksUrl(calls);
    expect(url).toContain('states=error');
    expect(url).toContain('counts=1');
    expect(url).toContain('limit=200');
    expect(rows(d).length).toBe(200);
    expect(rows(d).every((r) => r.querySelector('.status-badge').dataset.state === 'error')).toBe(true);
    // Each chip counts its state through the listing that fills its rows.
    expect(count(d, 'error') + '/' + count(d, 'done')).toBe('200+/200+');
    expect(note(d)).toContain('200+ means');
    more(d).click();
    await sleep(200);
    const next = lastTasksUrl(calls);
    expect(next).toContain('states=error');
    expect(next).toContain('offset=200');
    expect(rows(d).length).toBe(250);
    expect(rows(d)[249].querySelector('.task-id').textContent).toBe('10');
    expect(count(d, 'error') + '/' + count(d, 'done')).toBe('200+/200+');
    expect(more(d)).toBeNull();
  } finally { w.close(); }
});

t('a pressed chip still shows its rows after a refresh, a keeping refresh, and a change of chip', async () => {
  const { w, d, calls } = await mount(history(450));
  try {
    pressOnly(d, 'error');
    await w.__T.refreshTasksTable();
    await sleep(150);
    expect(rows(d).length).toBe(45);
    await w.__T.refreshTasksTable({ keep: true });
    await sleep(150);
    expect(rows(d).length).toBe(45);
    await w.__T.refreshTasksTable();
    await sleep(150);
    expect(rows(d).length).toBe(45);
    expect(d.querySelector('#blacksmith .toolbar-chips input[value="error"]').checked).toBe(true);
    expect(d.querySelector('#blacksmith .toolbar-chips input[value="done"]').checked).toBe(false);
    // Pressing the done chip as well asks for both states, through the chip's own handler.
    const done = d.querySelector('#blacksmith .toolbar-chips input[value="done"]');
    done.checked = true;
    done.dispatchEvent(new w.Event('change', { bubbles: true }));
    await sleep(200);
    expect(decodeURIComponent(lastTasksUrl(calls))).toContain('states=done,error');
    expect(rows(d).length).toBe(200);
  } finally { w.close(); }
});

t('an error task behind more than 200 newer tasks shows up when the Summary link is followed', async () => {
  // The Summary row links to this address, and the router carries the state to the Tasks page.
  const all = history(450, [7]);
  const { w, d, calls } = boot({
    noDialogs: true,
    router: true,
    url: 'http://localhost/#/broker/tasks?state=error',
    routes: [
      ['/b/tasks?', (u, o, json) => json(200, serve(all)(u))],
      ['/b/service-filter-options', (u, o, json) => json(200, { options: ['blacksmith'] })],
      ['/b/status', (u, o, json) => json(200, { env: 'lab', deployment: 'blacksmith', instances: {}, plans: {} })],
      ['/v2/catalog', (u, o, json) => json(200, { services: [] })],
    ],
  });
  try {
    await sleep(700);
    expect(lastTasksUrl(calls)).toContain('states=error');
    expect(ids(d)).toEqual(['7']);
    expect(d.querySelector('#blacksmith .toolbar-chips input[value="error"]').checked).toBe(true);
    expect(d.querySelector('#blacksmith .toolbar-chips input[value="done"]').checked).toBe(false);
  } finally { w.close(); }
});

t('a keeping refresh reads every page the reader loaded and stays on them', async () => {
  const { w, d, calls } = await mount(history(900));
  try {
    more(d).click();
    await sleep(200);
    more(d).click();
    await sleep(200);
    expect(rows(d).length).toBe(600);
    const before = calls.length;
    await w.__T.refreshTasksTable({ keep: true });
    await sleep(250);
    const urls = calls.slice(before).filter((c) => c.includes('/b/tasks?'));
    expect(urls.length).toBe(3);
    expect(urls[0]).not.toContain('offset=');
    expect(urls[0]).toContain('counts=1');
    // Only the first page's counts are shown, so the later pages do not ask for them.
    expect(urls[1]).not.toContain('counts=');
    expect(urls[2]).not.toContain('counts=');
    expect(urls[2]).toContain('offset=400');
    expect(rows(d).length).toBe(600);
    expect(more(d)).not.toBeNull();
  } finally { w.close(); }
});

t('a chip counts the error task behind 200 newer tasks, and it equals the rows pressing it lists', async () => {
  const { w, d } = await mount(history(450, [7, 9, 11]));
  try {
    expect(count(d, 'error')).toBe('3');
    pressOnly(d, 'error');
    await w.__T.refreshTasksTable();
    await sleep(150);
    expect(rows(d).length).toBe(3);
    expect(count(d, 'error')).toBe('3');
  } finally { w.close(); }
});

t('a chip with a known count of zero is dimmed, and an unpressed one with tasks is not', async () => {
  const { w, d } = await mount(history(450, [7]));
  try {
    const box = (s) => d.querySelector(`#blacksmith .toolbar-chips input[value="${s}"]`);
    box('cancelled').checked = false;
    box('error').checked = false;
    w.__T.syncTaskStateChips();
    expect(count(d, 'cancelled')).toBe('0');
    expect(box('cancelled').disabled).toBe(true);
    expect(box('error').disabled).toBe(false);
  } finally { w.close(); }
});

t('at the broker ceiling the button is gone and the status says older tasks are not listed', async () => {
  const { w, d } = await mount(history(500), undefined, 300);
  try {
    more(d).click();
    await sleep(150);
    expect(rows(d).length).toBe(300);
    expect(more(d)).toBeNull();
    const status = d.querySelector('.tasks-load-status');
    expect(status.textContent).toContain('Older tasks are not listed here');
    expect(status.getAttribute('role')).toBe('status');
  } finally { w.close(); }
});

t('a plain refresh goes back to the first page and its server counts', async () => {
  const { w, d, calls } = await mount(history(450));
  try {
    more(d).click();
    await sleep(200);
    await w.__T.refreshTasksTable();
    await sleep(200);
    const url = calls.filter((c) => c.includes('/b/tasks?')).pop();
    expect(url).not.toContain('offset=');
    expect(rows(d).length).toBe(200);
    expect(more(d)).not.toBeNull();
  } finally { w.close(); }
});

t('a pressed chip with an inexact answer says the list may be incomplete', () => {
  const { w, d } = boot();
  try {
    const host = d.createElement('div');
    host.innerHTML = w.__T.formatTasks([row(7, 'error')], 'tasks', { exact: false, hasMore: false });
    const text = host.querySelector('.tasks-count-note').textContent;
    expect(text).toContain('may be incomplete');
    expect(text).not.toContain('latest');
  } finally { w.close(); }
});

t('a refresh whose answer names an uncounted state shows a question mark on that chip and never 0', async () => {
  const { w, d } = await mount(history(40));
  try {
    w.__routes.unshift(['/b/tasks?', (u, o, json) => {
      const body = serve(history(40))(u);
      delete body.state_counts.queued;
      return json(200, { ...body, state_counts_unknown: ['queued'] });
    }]);
    await w.__T.refreshTasksTable();
    await sleep(150);
    expect(count(d, 'queued')).toBe('?');
    expect(count(d, 'done')).toBe('36');
    expect(d.querySelector('#blacksmith .toolbar-chips input[value="queued"]').disabled).toBe(false);
    expect(note(d)).toBe('A question mark means the broker could not read that count.');
  } finally { w.close(); }
});

t('a narrowed Scope asks the broker for its team with counts, and the chips show the counts the broker answers', async () => {
  const { w, d, calls } = await mount(history(40));
  try {
    const scope = d.getElementById('task-filter');
    scope.innerHTML = '<option value="blacksmith">All</option><option value="valkey">valkey</option>';
    scope.value = 'valkey';
    // The broker counts after its team filter, so its answer is the scope's own.
    w.__routes.unshift(['/b/tasks?', (u, o, json) => json(200, u.includes('team=valkey')
      ? serve(history(6, [3, 4]))(u)
      : serve(history(40))(u))]);
    await w.__T.refreshTasksTable();
    await sleep(150);
    const url = lastTasksUrl(calls);
    expect(url).toContain('team=valkey');
    expect(url).toContain('counts=1');
    expect(count(d, 'error') + '/' + count(d, 'done')).toBe('2/4');
    expect(rows(d).length).toBe(6);
  } finally { w.close(); }
});
