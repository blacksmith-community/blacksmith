const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { UI, coarseRules, decl } = require('./touch-support.js');

const rules = coarseRules('touch.css');

test('touch.css is linked after every other stylesheet', () => {
  const html = fs.readFileSync(path.join(UI, 'index.html'), 'utf8');
  const sheets = [...html.matchAll(/<link rel="stylesheet"[^>]*href="\/([^"?]+)/g)].map((m) => m[1]);
  expect(sheets[sheets.length - 1]).toBe('touch.css');
});

test('on a coarse pointer every text input, select, and textarea is at least 16 pixels', () => {
  const rule = rules.find((r) => /\bselect\b/.test(r.selector) && /\btextarea\b/.test(r.selector) && decl(r.body, 'font-size'));
  expect(rule).toBeDefined();
  expect(decl(rule.body, 'font-size')).toBe('16px');
  // The rule has to beat id-scoped component rules such as #services #instance-search.
  expect(rule.body).toContain('!important');
  // Checkboxes, radios, and buttons are not text entry, so they stay out of it.
  for (const type of ['text', 'search', 'email', 'url', 'password', 'number', 'tel']) {
    expect(rule.selector).toContain(`input[type="${type}"]`);
  }
  expect(rule.selector).toContain('input:not([type])');
});
