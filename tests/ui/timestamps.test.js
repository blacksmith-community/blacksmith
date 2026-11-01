const { test, expect } = require('bun:test');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const { pinZone } = require('./fixed-zone.js');

pinZone('America/New_York');
const t = JSDOM ? test : test.skip;

const moment = '2026-11-01T03:06:32Z';

t('a timestamp reads as local time with the short zone name', () => {
  const { w } = boot();
  try {
    expect(w.__T.formatStamp(moment)).toBe('2026-10-31 23:06:32 EDT');
    expect(w.__T.formatStamp('2026-12-01T03:06:32Z')).toBe('2026-11-30 22:06:32 EST');
  } finally { w.close(); }
});

t('a missing or invalid time reads as nothing', () => {
  const { w } = boot();
  try {
    expect(w.__T.formatStamp('')).toBe('');
    expect(w.__T.formatStamp(null)).toBe('');
    expect(w.__T.formatStamp('not a time')).toBe('');
  } finally { w.close(); }
});

t('the table cell markup carries the full ISO value in its title', () => {
  const { w } = boot();
  try {
    const html = w.__T.stampHtml(moment);
    expect(html).toContain('2026-10-31 23:06:32 EDT');
    expect(html).toContain('title="2026-11-01T03:06:32.000Z"');
    expect(w.__T.stampHtml('')).toContain('empty-value');
  } finally { w.close(); }
});

t('a certificate timestamp and a scan line use the same format', () => {
  const { w } = boot();
  try {
    expect(w.__T.certTimeText(moment)).toBe('2026-10-31 23:06:32 EDT');
    expect(w.__T.certDay('2029-10-24T09:14:30Z')).toBe('2029-10-24');
  } finally { w.close(); }
});

t('no UI code formats a time with the browser locale', () => {
  const fs = require('fs');
  const path = require('path');
  for (const file of ['blacksmith.js', 'navigation.js', 'action-help.js', 'routes.js', 'view-guidance.js']) {
    const src = fs.readFileSync(path.join(__dirname, '..', '..', 'ui', file), 'utf8');
    // Date and time methods are never allowed on any receiver, with or without arguments.
    expect(src).not.toMatch(/\.toLocale(Time|Date)String\s*\(/);
    // toLocaleString is for counts only, called with no arguments on a number.
    const calls = src.split('\n').filter((line) => /\.toLocaleString\s*\(/.test(line));
    const counts = /(\.length|\b(?:loaded|total|rendered)|\.total_lines|\.totalLines|\bNumber\([^)]*\))\.toLocaleString\(\)/g;
    const other = calls.filter((line) => line.replace(counts, '').match(/\.toLocaleString\s*\(/));
    expect(other).toEqual([]);
  }
});

t('formatDay is the local calendar day and stampIso is the full ISO value', () => {
  const { w } = boot();
  try {
    expect(w.__T.formatDay('2026-11-01T03:06:32Z')).toBe('2026-10-31');
    expect(w.__T.formatDay(new Date('2026-12-01T03:06:32Z'))).toBe('2026-11-30');
    expect(w.__T.formatDay('')).toBe('');
    expect(w.__T.formatDay('nope')).toBe('');
    expect(w.__T.stampIso('2026-11-01T03:06:32Z')).toBe('2026-11-01T03:06:32.000Z');
    expect(w.__T.stampIso(null)).toBe('');
    expect(w.__T.stampIso('nope')).toBe('');
  } finally { w.close(); }
});

t('a certificate day falls on the local day, the same one the stamp beside it shows', () => {
  const { w } = boot();
  try {
    // 02:00 UTC is still the day before in New York.
    expect(w.__T.certDay('2029-10-25T02:00:00Z')).toBe('2029-10-24');
    expect(w.__T.certTimeText('2029-10-25T02:00:00Z')).toBe('2029-10-24 22:00:00 EDT');
    expect(w.__T.certIso('2029-10-25T02:00:00Z')).toBe('2029-10-25T02:00:00.000Z');
  } finally { w.close(); }
});

t('setStamp writes the stamp with its ISO title, and the empty value for none', () => {
  const { w, d } = boot();
  try {
    const el = d.createElement('dd');
    w.__T.setStamp(el, moment);
    expect(el.textContent).toBe('2026-10-31 23:06:32 EDT');
    expect(el.getAttribute('title')).toBe('2026-11-01T03:06:32.000Z');
    w.__T.setStamp(el, null);
    expect(el.querySelector('.empty-value')).not.toBeNull();
    expect(el.hasAttribute('title')).toBe(false);
  } finally { w.close(); }
});

t('sorting a timestamp column orders rows by time, in both directions, across months', () => {
  const { w, d } = boot();
  try {
    const rows = [['b', '2026-11-02T10:00:00Z'], ['a', '2026-10-05T10:00:00Z'], ['c', '2026-12-01T10:00:00Z'], ['n', '']];
    const table = d.createElement('table');
    table.innerHTML = '<thead><tr><th>Name</th><th>Started</th></tr></thead><tbody>'
      + rows.map(([n, v]) => `<tr><td>${n}</td><td>${w.__T.timestampHtml(v)}</td></tr>`).join('') + '</tbody>';
    d.body.appendChild(table);
    const th = table.querySelectorAll('th')[1];
    const names = () => [...table.querySelectorAll('tbody tr')].map((r) => r.cells[0].textContent).filter((n) => n !== 'n');
    w.__T.sortTableByColumn(table, 1, th);
    expect(names()).toEqual(['a', 'b', 'c']);
    w.__T.sortTableByColumn(table, 1, th);
    expect(names()).toEqual(['c', 'b', 'a']);
  } finally { w.close(); }
});

t('a registration card dates itself with the ISO value in a title', () => {
  const { w } = boot();
  try {
    const html = w.__T.CFRegistrationManager.renderRegistrationCard({ id: 'r1', name: 'cf', status: 'active', created_at: '2026-11-01T03:06:32Z' });
    expect(html).toContain('<time datetime="2026-11-01T03:06:32.000Z" title="2026-11-01T03:06:32.000Z">2026-10-31</time>');
  } finally { w.close(); }
});

t('the instance creation time uses the shared formatter with an ISO title', () => {
  const { w } = boot();
  try {
    const render = (details) => {
      w.__T.renderServiceDetail('i1', Object.assign({ service_id: 'redis', plan: { name: 'small' } }, details));
      const host = w.document.createElement('div');
      host.innerHTML = w.serviceInstanceDetailsContent.i1;
      const row = Array.from(host.querySelectorAll('tr')).find((tr) => tr.querySelector('th').textContent === 'created at');
      return row.querySelector('.copy-wrapper > span');
    };
    const created = render({ created_at: moment });
    expect(created.textContent).toBe('2026-10-31 23:06:32 EDT');
    expect(created.getAttribute('title')).toBe('2026-11-01T03:06:32.000Z');
    const epoch = render({ created: Date.parse(moment) / 1000 });
    expect(epoch.textContent).toBe('2026-10-31 23:06:32 EDT');
    const none = render({});
    expect(none.textContent).toBe('—');
    expect(none.hasAttribute('title')).toBe(false);
  } finally { w.close(); }
});

t('the endpoint empty state writes its button in sentence case', () => {
  const fs = require('fs');
  const path = require('path');
  const src = fs.readFileSync(path.join(__dirname, '..', '..', 'ui', 'blacksmith.js'), 'utf8');
  expect(src).toContain('>Connect now</button>');
  expect(src).not.toContain('Connect Now');
});
