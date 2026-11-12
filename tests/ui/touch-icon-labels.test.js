const { test, expect } = require('bun:test');
const { coarseRules, decl } = require('./touch-support.js');

const rules = coarseRules('action-help.css');
const label = (r) => /::after/.test(r.selector) && /attr\(data-action-help\)/.test(r.body);

test('on a coarse pointer an icon button keeps a fixed 44 pixel square by default', () => {
  const base = rules.find((r) => r.selector === '[data-icon-help]');
  expect(base).toBeDefined();
  expect(decl(base.body, 'min-width')).toBe('44px');
  expect(decl(base.body, 'min-height')).toBe('44px');
  expect(decl(base.body, 'width')).not.toBe('auto');
});

test('on a coarse pointer no icon label expands outside a toolbar with room', () => {
  const labels = rules.filter(label);
  expect(labels.length).toBeGreaterThan(0);
  for (const r of labels) {
    // The selector must be scoped to a toolbar, so a table cell or the page
    // header never receives the expanded label.
    expect(r.selector).toMatch(/^:is\([^)]*\) \[data-icon-help\]::after$/);
    expect(r.selector).toContain('.table-controls-container');
  }
});

test('the header and table cells are never in the label allow list', () => {
  for (const r of rules.filter(label)) {
    expect(r.selector.replace(/\.[\w-]+/g, '')).not.toMatch(/\b(header|table|td|th|tr)\b/);
  }
});
