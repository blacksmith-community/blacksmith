const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { pinZone } = require('./fixed-zone.js');

pinZone('America/New_York');

const TOOLBAR = '<div class="table-toolbar"><div class="search-filter-container"></div><div class="table-toolbar-actions"><button class="refresh-btn-logs" aria-label="Refresh tasks"></button><button class="copy-btn-logs"></button></div></div><table><tbody><tr><td>row</td></tr></tbody></table>';

// Mounts view A, leaves it for view B, and comes back to A.
const roundTrip = (w, d, html) => {
  const host = d.createElement('section');
  const content = d.createElement('div');
  content.className = 'detail-content';
  host.appendChild(content);
  d.body.appendChild(host);
  expect(w.__T.retainView(content, 'A')).toBe(false);
  content.innerHTML = html;
  expect(w.__T.retainView(content, 'B')).toBe(false);
  content.innerHTML = '<p>Other</p>';
  expect(w.__T.retainView(content, 'A')).toBe(true);
  return { host, content };
};

test('a restored view shows no restored-view sentence', () => {
  const { w, d } = boot();
  try {
    const { host } = roundTrip(w, d, TOOLBAR);
    expect(host.querySelector('.retained-view-note')).toBeNull();
    expect(host.textContent).not.toContain('Restored your view');
    expect(host.textContent).not.toContain('Data may have changed');
  } finally { w.close(); }
});

test('restored table data says when it was fetched, before the Refresh button', () => {
  const { w, d } = boot();
  try {
    const before = Date.now();
    const { content } = roundTrip(w, d, TOOLBAR);
    const note = content.querySelector('.table-toolbar .data-fetched-note');
    expect(note).not.toBeNull();
    expect(note.textContent).toMatch(/^Data fetched \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/);
    const time = note.querySelector('time');
    expect(time.getAttribute('title')).toBe(time.getAttribute('datetime'));
    // It is the fetch time, which is no later than the moment the view was left.
    expect(Date.parse(time.getAttribute('datetime'))).toBeLessThanOrEqual(Date.now());
    expect(Date.parse(time.getAttribute('datetime'))).toBeGreaterThanOrEqual(before - 1000);
    expect(note.nextElementSibling.classList.contains('table-toolbar-actions')).toBe(true);
  } finally { w.close(); }
});

test('a view with no table toolbar shows nothing when restored', () => {
  const { w, d } = boot();
  try {
    const { content } = roundTrip(w, d, '<p>Summary</p>');
    expect(content.querySelector('.data-fetched-note')).toBeNull();
    expect(content.parentElement.querySelector('.retained-view-note')).toBeNull();
  } finally { w.close(); }
});

test('Refresh removes the fetched line and a new view starts without one', () => {
  const { w, d } = boot();
  try {
    const { content } = roundTrip(w, d, TOOLBAR);
    clickButton(w, content.querySelector('.refresh-btn-logs'));
    expect(content.querySelector('.data-fetched-note')).toBeNull();
  } finally { w.close(); }
});

test('the fetched line is muted text of at least 12 pixels', () => {
  const css = fs.readFileSync(path.join(__dirname, '..', '..', 'ui', 'routes.css'), 'utf8');
  const rule = css.match(/\.data-fetched-note \{([^}]*)\}/);
  expect(rule).not.toBeNull();
  expect(rule[1]).toContain('var(--muted-foreground');
  expect(rule[1]).toMatch(/font-size: \.8125rem|font-size: 12px|font-size: \.75rem/);
});

// Mounts the broker VMs section in the real panel, so its Refresh button is the real one.
const mountVms = (w, d, getNow) => {
  const panel = d.getElementById('blacksmith');
  panel.classList.add('active');
  let content = panel.querySelector('.detail-content');
  if (!content) {
    content = d.createElement('div');
    content.className = 'detail-content';
    panel.appendChild(content);
  }
  panel.querySelectorAll('.detail-tab').forEach((tab) => tab.classList.toggle('active', tab.dataset.tab === 'vms'));
  expect(w.__T.retainView(content, 'vms')).toBe(false);
  content.innerHTML = w.__T.formatVMs([{ id: 'vm', job_name: 'blacksmith', index: 0, state: 'running', job_state: 'running' }]);
  return content;
};
const stubFetch = (w) => {
  w.fetch = () => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([{ id: 'vm', job_name: 'refreshed-job', index: 0, state: 'running', job_state: 'running' }]), text: () => Promise.resolve('') });
};
// The harness window does not run inline handler attributes, so this runs the button's own
// onclick text on a real click event, as a browser would.
const clickButton = (w, button) => {
  button.addEventListener('click', (event) => new w.Function('event', button.getAttribute('onclick')).call(button, event));
  button.click();
};
const settle = (ms = 20) => new Promise((resolve) => setTimeout(resolve, ms));
const fetchedAtOf = (content) => content.querySelector('.data-fetched-note time')?.getAttribute('datetime');

test('the real Refresh button moves the fetched time that a restored view shows', async () => {
  const { w, d } = boot();
  try {
    let now = Date.parse('2026-11-01T10:00:05Z');
    w.Date.now = () => now;
    await settle(200);
    stubFetch(w);
    const content = mountVms(w, d);
    w.__T.markFetched(content);
    now = Date.parse('2026-11-01T10:30:00Z');
    clickButton(w, content.querySelector('.refresh-btn-logs'));
    await settle(50);
    expect(content.textContent).toContain('refreshed-job');
    expect(w.__T.retainView(content, 'other')).toBe(false);
    content.innerHTML = '<p>Other</p>';
    expect(w.__T.retainView(content, 'vms')).toBe(true);
    expect(fetchedAtOf(content)).toBe('2026-11-01T10:30:00.000Z');
  } finally { w.close(); }
});

test('a load that finishes while its container is detached is stamped', async () => {
  const { w, d } = boot();
  try {
    let now = Date.parse('2026-11-01T10:00:05Z');
    w.Date.now = () => now;
    const content = mountVms(w, d);
    w.__T.markFetched(content);
    content.parentElement.removeChild(content);
    now = Date.parse('2026-11-01T10:45:00Z');
    w.__T.markFetched(content);
    d.getElementById('blacksmith').appendChild(content);
    expect(w.__T.retainView(content, 'other')).toBe(false);
    content.innerHTML = '<p>Other</p>';
    expect(w.__T.retainView(content, 'vms')).toBe(true);
    expect(fetchedAtOf(content)).toBe('2026-11-01T10:45:00.000Z');
  } finally { w.close(); }
});

test('a child added after the load does not move the fetched time', async () => {
  const { w, d } = boot();
  try {
    let now = Date.parse('2026-11-01T10:00:05Z');
    w.Date.now = () => now;
    const content = mountVms(w, d);
    w.__T.markFetched(content);
    now = Date.parse('2026-11-01T10:20:00Z');
    const toast = d.createElement('div');
    toast.className = 'toast';
    content.appendChild(toast);
    await settle();
    expect(w.__T.retainView(content, 'other')).toBe(false);
    content.innerHTML = '<p>Other</p>';
    expect(w.__T.retainView(content, 'vms')).toBe(true);
    expect(fetchedAtOf(content)).toBe('2026-11-01T10:00:05.000Z');
  } finally { w.close(); }
});
