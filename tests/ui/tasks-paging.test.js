const { test, expect } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');

const t = JSDOM ? test : test.skip;

const { row, history, serve } = require('./tasks-stub.js');

const mount = async (all, shift) => {
  const { w, d, calls } = boot();
  await sleep(50);
  const root = d.getElementById('blacksmith');
  root.classList.add('active');
  root.innerHTML = '<div class="entity-workspace" data-section="tasks"><div class="detail-content"></div></div>';
  w.__routes.push(['/b/tasks?', (u, o, json) => json(200, serve(all, shift)(u))]);
  const first = serve(all)('/b/tasks?type=recent&limit=200&counts=1');
  d.querySelector('#blacksmith .detail-content').innerHTML = w.__T.formatTasks(first.tasks, 'tasks', { stateCounts: first.state_counts, window: first.window, hasMore: first.has_more, exact: first.exact, ceiling: first.ceiling });
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
    // The chip counts are the latest window's, and the note says so.
    expect(count(d, 'error') + '/' + count(d, 'done')).toBe('20/180');
    expect(note(d)).toContain('latest 200 tasks of every state');
    more(d).click();
    await sleep(200);
    const next = lastTasksUrl(calls);
    expect(next).toContain('states=error');
    expect(next).toContain('offset=200');
    expect(rows(d).length).toBe(250);
    expect(rows(d)[249].querySelector('.task-id').textContent).toBe('10');
    expect(count(d, 'error') + '/' + count(d, 'done')).toBe('20/180');
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
    expect(urls[2]).toContain('offset=400');
    expect(rows(d).length).toBe(600);
    expect(more(d)).not.toBeNull();
  } finally { w.close(); }
});

t('a chip with none in the latest window stays pressable while older tasks exist', async () => {
  const { w, d } = await mount(history(450, [7]));
  try {
    const error = d.querySelector('#blacksmith .toolbar-chips input[value="error"]');
    w.__T.syncTaskStateChips();
    expect(count(d, 'error')).toBe('0');
    expect(error.disabled).toBe(false);
  } finally { w.close(); }
});

t('at the broker ceiling the button is gone and the status says older tasks are not listed', async () => {
  const { w, d } = await mount(history(2500));
  try {
    for (let page = 1; page < 10; page++) {
      more(d).click();
      await sleep(120);
    }
    expect(rows(d).length).toBe(2000);
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
