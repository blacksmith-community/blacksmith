const { test, expect } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');

const t = JSDOM ? test : test.skip;

const row = (id, state) => ({ id, state, description: `task ${id}`, started_at: '2026-10-31T10:00:00Z', timestamp: 1793000000 + id });
// The director's history, newest first. Every tenth task failed.
const history = (total) => Array.from({ length: total }, (_, i) => { const id = total - i; return row(id, id % 10 === 0 ? 'error' : 'done'); });

// A stand-in for the broker's list that pages by offset and answers with the
// object form, with counts over the rows from the newest through the page.
const serve = (all, shift = () => 0) => (u) => {
  const q = new URL(u, 'http://localhost').searchParams;
  const limit = Number(q.get('limit')) || 50;
  const offset = Number(q.get('offset')) || 0;
  const through = all.slice(0, offset + limit);
  const counts = { processing: 0, queued: 0, done: 0, error: 0, cancelled: 0 };
  through.forEach((task) => { counts[task.state] += 1; });
  const start = Math.max(0, offset - shift());
  return { tasks: all.slice(start, offset + limit), offset, limit, has_more: all.length > offset + limit, exact: true, state_counts: counts, window: through.length };
};

const mount = async (all, shift) => {
  const { w, d, calls } = boot();
  await sleep(50);
  const root = d.getElementById('blacksmith');
  root.classList.add('active');
  root.innerHTML = '<div class="entity-workspace" data-section="tasks"><div class="detail-content"></div></div>';
  w.__routes.push(['/b/tasks?', (u, o, json) => json(200, serve(all, shift)(u))]);
  const first = serve(all)('/b/tasks?type=recent&limit=200&counts=1');
  d.querySelector('#blacksmith .detail-content').innerHTML = w.__T.formatTasks(first.tasks, 'tasks', { stateCounts: first.state_counts, window: first.window, hasMore: first.has_more, exact: first.exact });
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
    host.innerHTML = w.__T.formatTasks([row(2, 'done'), row(1, 'done')], 'tasks', { hasMore: true, window: 2 });
    const button = host.querySelector('[data-tasks-load-more]');
    expect(button.tagName).toBe('BUTTON');
    expect(button.getAttribute('type')).toBe('button');
    expect(button.dataset.keepKey).toBe('tasks-load-more');
    const status = host.querySelector('.tasks-load-status');
    expect(status.getAttribute('role')).toBe('status');
    expect(status.getAttribute('aria-live')).toBe('polite');
    expect(status.textContent).toBe('Showing 2 tasks. Older tasks are available.');
    const done = host.cloneNode(false);
    done.innerHTML = w.__T.formatTasks([row(1, 'done')], 'tasks', { hasMore: false, window: 1 });
    expect(done.querySelector('[data-tasks-load-more]')).toBeNull();
  } finally { w.close(); }
});

t('the note says what the chip counts cover, and does not claim completeness it lacks', () => {
  const { w, d } = boot();
  try {
    const of = (meta) => { const host = d.createElement('div'); host.innerHTML = w.__T.formatTasks([row(1, 'done')], 'tasks', meta); return host.querySelector('.tasks-count-note').textContent; };
    expect(of({ window: 200, hasMore: true })).toContain('latest 200 tasks');
    expect(of({ window: 200, hasMore: true })).toContain('can still have older tasks');
    expect(of({ window: 5, hasMore: false, exact: true })).toBe('State counts cover all 5 tasks the director lists.');
    expect(of({ window: 5, hasMore: false, exact: false })).toContain('could not check every task');
    expect(of({ window: 5 })).toContain('latest 5 tasks');
    expect(of({})).toBe('State counts cover the tasks loaded here.');
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

t('the chip counts and the note describe exactly the rows the table holds after each load', async () => {
  const { w, d } = await mount(history(450));
  try {
    expect(count(d, 'error')).toBe('20');
    more(d).click();
    await sleep(200);
    expect(count(d, 'error') + '/' + count(d, 'done')).toBe('40/360');
    expect(Number(count(d, 'error')) + Number(count(d, 'done'))).toBe(rows(d).length);
    expect(note(d)).toContain('latest 400 tasks');
    more(d).click();
    await sleep(200);
    expect(count(d, 'error') + '/' + count(d, 'done')).toBe('45/405');
    expect(note(d)).toBe('State counts cover all 450 tasks the director lists.');
  } finally { w.close(); }
});

t('a task that moved down while paging is not shown twice, and the counts follow the rows', async () => {
  // Five newer tasks arrive before the second page is read, so five rows repeat.
  const { w, d } = await mount(history(450), () => 5);
  try {
    more(d).click();
    await sleep(200);
    const ids = rows(d).map((r) => r.querySelector('.task-id').textContent);
    expect(new Set(ids).size).toBe(ids.length);
    expect(Number(count(d, 'error')) + Number(count(d, 'done'))).toBe(ids.length);
  } finally { w.close(); }
});

t('a state chip still filters the rows that Load more added', async () => {
  const { w, d } = await mount(history(450));
  try {
    more(d).click();
    await sleep(200);
    const box = d.querySelector('#blacksmith .toolbar-chips input[value="done"]');
    box.checked = false;
    box.dispatchEvent(new w.Event('change', { bubbles: true }));
    const visible = rows(d).filter((r) => r.style.display !== 'none');
    expect(visible.length).toBe(40);
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
