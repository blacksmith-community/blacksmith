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
    content.querySelector('.refresh-btn-logs').click();
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

test('the fetched time follows a Refresh that completes after the first mount', async () => {
  const { w, d } = boot();
  try {
    let now = Date.parse('2026-11-01T10:00:00Z');
    w.Date.now = () => now;
    const host = d.createElement('section');
    const content = d.createElement('div');
    content.className = 'detail-content';
    host.appendChild(content);
    d.body.appendChild(host);
    const microtask = () => new Promise((resolve) => setTimeout(resolve, 0));
    expect(w.__T.retainView(content, 'A')).toBe(false);
    content.innerHTML = '<div class="loading">Loading...</div>';
    now = Date.parse('2026-11-01T10:00:05Z');
    content.innerHTML = TOOLBAR;
    await microtask();
    // Leave, come back, and refresh much later.
    expect(w.__T.retainView(content, 'B')).toBe(false);
    content.innerHTML = '<p>Other</p>';
    expect(w.__T.retainView(content, 'A')).toBe(true);
    expect(content.querySelector('.data-fetched-note time').getAttribute('datetime')).toBe('2026-11-01T10:00:05.000Z');
    now = Date.parse('2026-11-01T10:30:00Z');
    content.innerHTML = '<div class="loading">Loading...</div>';
    now = Date.parse('2026-11-01T10:30:02Z');
    content.innerHTML = TOOLBAR;
    await microtask();
    expect(w.__T.retainView(content, 'B')).toBe(true);
    content.innerHTML = '<p>Other</p>';
    expect(w.__T.retainView(content, 'A')).toBe(true);
    expect(content.querySelector('.data-fetched-note time').getAttribute('datetime')).toBe('2026-11-01T10:30:02.000Z');
  } finally { w.close(); }
});
