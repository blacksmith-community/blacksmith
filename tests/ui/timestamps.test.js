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
    expect(w.__T.certClockText(moment)).toBe('2026-10-31 23:06:32 EDT');
    expect(w.__T.certDay('2029-10-24T09:14:30Z')).toBe('2029-10-24');
  } finally { w.close(); }
});

t('no UI code formats a time with the browser locale', () => {
  const src = require('fs').readFileSync(require('path').join(__dirname, '..', '..', 'ui', 'blacksmith.js'), 'utf8');
  expect(src).not.toMatch(/\.toLocale(Time|Date)String\(\)/);
});
