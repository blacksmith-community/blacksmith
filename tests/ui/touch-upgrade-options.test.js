const { test, expect } = require('bun:test');
const { narrowRules, decl } = require('./touch-support.js');

// forge-theme.css gives the first column of this table a 20 percent width with
// an id-scoped selector, which outranks the phone rule that stacks the rows, so
// the "System" heading was squeezed to about 60 pixels and broke mid-word.
test('the stacked component heading keeps its full width and never splits a word', () => {
  const rule = narrowRules('touch.css').find((r) => /batch-upgrade-options-table/.test(r.selector) && /th:first-child/.test(r.selector));
  expect(rule).toBeDefined();
  expect(decl(rule.body, 'width')).toBe('100%');
  expect(decl(rule.body, 'overflow-wrap')).toBe('normal');
  expect(decl(rule.body, 'word-break')).toBe('normal');
});
