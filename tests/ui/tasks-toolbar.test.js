const { test, expect } = require('bun:test');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const toolbar = (w, d) => {
  const host = d.createElement('div');
  host.innerHTML = w.__T.formatTasks([{ id: 7, state: 'done', description: 'create release', started_at: '2026-10-31T10:00:00Z' }]);
  return host.querySelector('.compact-tasks-controls');
};

// The accessible name the browser would compute for a control with a label or a legend.
const nameOf = (el) => {
  const id = el.getAttribute('id');
  const label = id && el.getRootNode().querySelector(`label[for="${id}"]`) || el.closest('label');
  return (label ? label.textContent : '').replace(/\s+/g, ' ').trim();
};

t('the Tasks toolbar is one flat row of whole groups with the actions last', () => {
  const { w, d } = boot();
  try {
    const bar = toolbar(w, d);
    expect(bar.classList.contains('table-controls-container')).toBe(true);
    expect(bar.querySelector('.tasks-controls-row-1, .tasks-controls-row-2')).toBeNull();
    const kids = Array.from(bar.children).map((c) => c.tagName.toLowerCase() + '.' + ['search-filter-container', 'toolbar-field', 'toolbar-group', 'table-toolbar-actions'].find((k) => c.classList.contains(k)));
    expect(kids).toEqual(['div.search-filter-container', 'div.toolbar-field', 'div.toolbar-field', 'fieldset.toolbar-group', 'div.table-toolbar-actions']);
  } finally { w.close(); }
});

t('Type and Scope share one inline label pattern and the label is the accessible name', () => {
  const { w, d } = boot();
  try {
    const bar = toolbar(w, d);
    const fields = Array.from(bar.querySelectorAll('.toolbar-field'));
    expect(fields.length).toBe(2);
    fields.forEach((field) => {
      const label = field.firstElementChild;
      const select = field.lastElementChild;
      expect(label.tagName).toBe('LABEL');
      expect(select.tagName).toBe('SELECT');
      expect(label.getAttribute('for')).toBe(select.id);
      // A visible label that names the control, never a second aria-label that disagrees with it.
      expect(select.hasAttribute('aria-label')).toBe(false);
      expect(label.textContent.trim().endsWith(':')).toBe(false);
    });
    expect(nameOf(bar.querySelector('#task-type-filter'))).toBe('Type');
    expect(nameOf(bar.querySelector('#task-filter'))).toBe('Scope');
  } finally { w.close(); }
});

t('the search box has a hidden label named Search tasks', () => {
  const { w, d } = boot();
  try {
    const bar = toolbar(w, d);
    const input = bar.querySelector('#search-tasks-table');
    const label = bar.querySelector('label[for="search-tasks-table"]');
    expect(label.textContent).toBe('Search tasks');
    expect(label.classList.contains('visually-hidden')).toBe(true);
    expect(input.getAttribute('placeholder')).toBe('Search tasks...');
  } finally { w.close(); }
});

t('States is a fieldset with a legend over five checkboxes that stay checked', () => {
  const { w, d } = boot();
  try {
    const bar = toolbar(w, d);
    const group = bar.querySelector('fieldset.toolbar-group');
    expect(group.querySelector('legend').textContent).toBe('States');
    const boxes = Array.from(group.querySelectorAll('input[type="checkbox"]'));
    expect(boxes.map((b) => b.value)).toEqual(['processing', 'queued', 'done', 'error', 'cancelled']);
    expect(boxes.every((b) => b.checked)).toBe(true);
    // The page code finds the boxes through this selector.
    expect(bar.querySelectorAll('.checkbox-group input[type="checkbox"]').length).toBe(5);
    boxes.forEach((b) => expect(nameOf(b).length).toBeGreaterThan(0));
  } finally { w.close(); }
});

t('every toolbar control keeps its id and carries a unique keep key', () => {
  const { w, d } = boot();
  try {
    const bar = toolbar(w, d);
    ['search-tasks-table', 'task-type-filter', 'task-filter', 'refresh-tasks-btn'].forEach((id) => expect(bar.querySelector('#' + id)).not.toBeNull());
    const keys = Array.from(bar.querySelectorAll('input, select')).map((el) => el.getAttribute('data-keep-key'));
    expect(keys.every(Boolean)).toBe(true);
    expect(new Set(keys).size).toBe(keys.length);
  } finally { w.close(); }
});

t('no toolbar attribute carries command text or output', () => {
  const { w, d } = boot();
  try {
    const bar = toolbar(w, d);
    Array.from(bar.querySelectorAll('[title],[aria-label],[data-keep-key]')).forEach((el) => {
      expect(`${el.getAttribute('title')}${el.getAttribute('aria-label')}${el.getAttribute('data-keep-key')}`).not.toMatch(/create release/);
    });
  } finally { w.close(); }
});

// The stylesheets are read as text, the way the other layout checks read them.
const fs = require('fs');
const path = require('path');
const UI = path.resolve(__dirname, '..', '..', 'ui');
const forge = fs.readFileSync(path.join(UI, 'forge-theme.css'), 'utf8');
const rule = (css, selector) => {
  const at = css.indexOf('\n' + selector + ' {') + 1 || css.indexOf(selector + ' {');
  expect(at).toBeGreaterThan(-1);
  return css.slice(at, css.indexOf('}', at));
};
const media = (css, head) => {
  const start = css.indexOf(head);
  expect(start).toBeGreaterThan(-1);
  let depth = 0;
  for (let i = css.indexOf('{', start); i < css.length; i += 1) {
    if (css[i] === '{') depth += 1;
    if (css[i] === '}') { depth -= 1; if (depth === 0) return css.slice(start, i + 1); }
  }
  throw new Error('unterminated media block');
};

test('every control in the row is one height from one token', () => {
  expect(forge).toMatch(/\.table-controls-container \{ --toolbar-h: 2rem; \}/);
  ['.table-controls-container .toolbar-field select', '.table-controls-container .toolbar-chips label'].forEach((sel) => {
    const r = rule(forge, sel);
    expect(r).toMatch(/height: var\(--toolbar-h\)/);
    expect(r).toMatch(/min-height: var\(--toolbar-h\)/);
  });
  expect(rule(forge, '#blacksmith .compact-tasks-controls .table-search-input')).toMatch(/height: var\(--toolbar-h\)/);
  expect(forge).toMatch(/@media \(pointer: coarse\) \{ \.table-controls-container \{ --toolbar-h: 2\.75rem; \} \}/);
});

test('every label and legend shares one typographic rule', () => {
  const r = rule(forge, '.table-controls-container .toolbar-field > label, .table-controls-container fieldset.toolbar-group > legend');
  expect(r).toMatch(/white-space: nowrap/);
  expect(r).toMatch(/font-size: \.8125rem/);
});

test('a group stays whole and the legend sits inline beside its chips', () => {
  expect(rule(forge, '.toolbar-field')).toMatch(/flex: 0 0 auto/);
  expect(rule(forge, 'fieldset.toolbar-group')).toMatch(/display: flex[^}]*flex: 0 0 auto/);
  expect(rule(forge, 'fieldset.toolbar-group > legend')).toMatch(/float: left/);
});

test('a wide window keeps the whole row on one line and the search gives way first', () => {
  const wide = media(forge, '@media (min-width: 1280px)');
  expect(wide).toMatch(/\.compact-tasks-controls \{ flex-wrap: nowrap; \}/);
  expect(wide).toMatch(/search-filter-container \{ min-width: 8rem; \}/);
  expect(rule(forge, '#blacksmith .compact-tasks-controls .tasks-action-buttons')).toMatch(/flex: 0 0 auto[^}]*margin: 0 0 0 auto/);
});

test('the actions are the only flexible space and stay at the right end', () => {
  const r = rule(forge, '#blacksmith .compact-tasks-controls');
  expect(r).toMatch(/justify-content: flex-start/);
  expect(rule(forge, '#blacksmith .compact-tasks-controls .search-filter-container')).toMatch(/flex: 0 1 14rem/);
});

test('between a phone and a wide window the row wraps into two whole rows', () => {
  const mid = media(forge, '@media (min-width: 700px) {');
  expect(mid).toMatch(/tasks-action-buttons \{ order: 1; \}/);
  expect(mid).toMatch(/fieldset\.toolbar-group \{ order: 2; flex: 1 1 100%; \}/);
});

test('the phone layout keeps its full-width fields and wrapping chips', () => {
  const css = fs.readFileSync(path.join(UI, 'blacksmith.css'), 'utf8');
  expect(css).toMatch(/compact-tasks-controls\.table-controls-container \.toolbar-group \{[^}]*flex: 1 1 100%/);
  expect(css).toMatch(/compact-tasks-controls\.table-controls-container \.toolbar-chips \{[^}]*min-width: 0/);
});

const tasksOf = (states) => states.map((state, i) => ({ id: i + 1, state, description: 'task', started_at: '2026-10-31T10:00:00Z' }));
const chips = (bar) => Object.fromEntries(Array.from(bar.querySelectorAll('.toolbar-chips input')).map((i) => [i.value, i]));

t('each state chip shows how many loaded tasks are in that state', () => {
  const { w, d } = boot();
  try {
    const host = d.createElement('div');
    host.innerHTML = w.__T.formatTasks(tasksOf(['done', 'done', 'error', 'processing']));
    const byState = chips(host);
    const shown = Object.fromEntries(Object.entries(byState).map(([k, i]) => [k, i.closest('label').querySelector('.chip-count').textContent]));
    expect(shown).toEqual({ processing: '1', queued: '0', done: '2', error: '1', cancelled: '0' });
  } finally { w.close(); }
});

t('an unpressed chip with no tasks is disabled and dimmed, and a pressed one stays enabled', () => {
  const { w, d } = boot();
  try {
    const host = d.createElement('div');
    d.getElementById('blacksmith').appendChild(host);
    host.innerHTML = w.__T.formatTasks(tasksOf(['done', 'error']));
    const byState = chips(host);
    byState.cancelled.checked = false;
    w.__T.syncTaskStateChips();
    expect(byState.cancelled.disabled).toBe(true);
    expect(byState.cancelled.getAttribute('aria-disabled')).toBe('true');
    expect(byState.cancelled.closest('label').classList.contains('chip-dim')).toBe(true);
    expect(byState.queued.disabled).toBe(false);
    expect(byState.queued.hasAttribute('aria-disabled')).toBe(false);
    expect(byState.done.disabled).toBe(false);
    byState.done.checked = false;
    w.__T.syncTaskStateChips();
    expect(byState.done.disabled).toBe(false);
  } finally { w.close(); }
});

// These drive refreshTasksTable the way the Refresh button does: through the
// page's own request, its own rebuild of the table, and its own handlers.
const { sleep } = require('./certificates/harness.js').main;
const row = (id, state) => ({ id, state, description: `task ${id}`, started_at: '2026-10-31T10:00:00Z', timestamp: 1793000000 + id });
const mountTasks = async (opts) => {
  const { w, d, calls } = boot();
  await sleep(50);
  const root = d.getElementById('blacksmith');
  root.classList.add('active');
  root.innerHTML = '<div class="entity-workspace" data-section="tasks"><div class="detail-content"></div></div>';
  w.__routes.push(['/b/tasks?', (u, o, json) => json(200, opts.body(u))]);
  d.querySelector('#blacksmith .detail-content').innerHTML = w.__T.formatTasks(opts.first || []);
  return { w, d, calls };
};
const press = (d, state, on) => {
  const box = d.querySelector(`#blacksmith .toolbar-chips input[value="${state}"]`);
  box.checked = on;
  box.dispatchEvent(new d.defaultView.Event('change', { bubbles: true }));
  return box;
};
const shown = (d) => Array.from(d.querySelectorAll('.tasks-table tbody tr')).filter((r) => r.style.display !== 'none').map((r) => r.textContent.trim().replace(/\s+/g, ' '));

// A stand-in for the broker's list: it filters by states= and cuts to limit the
// way the handler does, and answers with counts when they are asked for.
const serve = (all) => (u) => {
  const q = new URL(u, 'http://localhost').searchParams;
  const states = (q.get('states') || '').split(',').filter(Boolean);
  const limit = Number(q.get('limit')) || 50;
  const counts = { processing: 0, queued: 0, done: 0, error: 0, cancelled: 0 };
  all.forEach((task) => { counts[task.state] += 1; });
  const rows = all.filter((task) => !states.length || states.includes(task.state)).slice(0, limit);
  return q.get('counts') === '1' ? { tasks: rows, state_counts: counts, window: all.length } : rows;
};

t('a refresh asks for the whole window with counts and no states, and the chips count from state_counts', async () => {
  const { w, d, calls } = await mountTasks({ body: serve([row(1, 'error'), row(2, 'error'), row(3, 'done'), row(4, 'done'), row(5, 'done')]) });
  try {
    press(d, 'done', false); press(d, 'processing', false);
    await w.__T.refreshTasksTable();
    await sleep(200);
    const url = calls.filter((c) => c.includes('/b/tasks?')).pop();
    expect(url).toContain('counts=1');
    expect(url).toContain('limit=200');
    expect(url).not.toMatch(/[?&]states=/);
    const count = (s) => d.querySelector(`#blacksmith .toolbar-chips input[value="${s}"]`).closest('label').querySelector('.chip-count').textContent;
    expect(count('done')).toBe('3');
    expect(count('error')).toBe('2');
    expect(d.querySelector('#blacksmith .tasks-count-note').textContent).toContain('latest 5');
    expect(shown(d).length).toBe(2);
  } finally { w.close(); }
});

t('a chip that reads Done 3 shows 3 rows after it is unpressed, refreshed, and pressed again', async () => {
  const { w, d } = await mountTasks({ body: serve([row(1, 'error'), row(2, 'done'), row(3, 'done'), row(4, 'done')]) });
  try {
    press(d, 'done', false);
    await w.__T.refreshTasksTable();
    await sleep(200);
    expect(shown(d).length).toBe(1);
    press(d, 'done', true);
    expect(d.querySelector('#blacksmith .toolbar-chips input[value="done"]').closest('label').querySelector('.chip-count').textContent).toBe('3');
    expect(shown(d).length).toBe(4);
  } finally { w.close(); }
});

t('a refresh that gets a bare array still counts its own rows', async () => {
  // A plain array, whatever the query asks for, so the fallback in readTasksResponse is the only way to read it.
  const { w, d } = await mountTasks({ body: () => [row(1, 'done'), row(2, 'error')] });
  try {
    await w.__T.refreshTasksTable();
    await sleep(200);
    expect(d.querySelector('#blacksmith .toolbar-chips input[value="done"]').closest('label').querySelector('.chip-count').textContent).toBe('1');
  } finally { w.close(); }
});

t('typing a search never brings back a row the state chips hide, and a refresh keeps it so', async () => {
  const { w, d } = await mountTasks({ body: serve([row(1, 'done'), row(2, 'error'), row(3, 'error')]) });
  try {
    await w.__T.refreshTasksTable();
    await sleep(200);
    ['processing', 'queued', 'done', 'cancelled'].forEach((s) => press(d, s, false));
    expect(shown(d).length).toBe(2);
    const input = d.getElementById('search-tasks-table');
    input.value = 'task';
    input.dispatchEvent(new w.Event('input'));
    expect(shown(d).length).toBe(2);
    expect(shown(d).join('|')).not.toContain('task 1');
    await w.__T.refreshTasksTable();
    await sleep(200);
    expect(shown(d).length).toBe(2);
    // With the search box empty, only the refresh itself can hide the done row again.
    d.getElementById('search-tasks-table').value = '';
    await w.__T.refreshTasksTable();
    await sleep(200);
    expect(shown(d).length).toBe(2);
    press(d, 'done', true);
    expect(shown(d).length).toBe(3);
    input.value = '';
  } finally { w.close(); }
});

t('unpressing a chip whose state has no tasks disables it at once', async () => {
  const { w, d } = await mountTasks({ body: serve([row(1, 'done')]) });
  try {
    await w.__T.refreshTasksTable();
    await sleep(200);
    const box = press(d, 'cancelled', false);
    expect(box.disabled).toBe(true);
    expect(box.closest('label').classList.contains('chip-dim')).toBe(true);
    expect(press(d, 'done', false).disabled).toBe(false);
  } finally { w.close(); }
});
