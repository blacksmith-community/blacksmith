const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

const css = fs.readFileSync(path.resolve(__dirname, '../../ui/forge-theme.css'), 'utf8');

// jsdom has no layout, so this reads the stylesheet. The four-column rule for
// the Summary cards must not apply below 700 px, where one column is wanted.
test('the four-column Summary layout applies only above the narrow breakpoint', () => {
  const rule = '.entity-metrics:has(> [data-last-change])';
  const at = css.indexOf(rule);
  expect(at).toBeGreaterThan(-1);
  const before = css.slice(0, at);
  const open = before.lastIndexOf('@media');
  expect(open).toBeGreaterThan(-1);
  const header = before.slice(open);
  expect(header).toMatch(/^@media \(min-width: 700px\) \{[\s\S]*$/);
});

test('the narrow layout keeps one column and no other rule widens it', () => {
  const narrow = css.indexOf('@media (max-width: 699.98px) {');
  const body = css.slice(narrow, css.indexOf('\n}', narrow));
  expect(body).toContain('.entity-metrics { grid-template-columns: 1fr;');
  const wide = css.match(/\.entity-metrics:has\([^)]*\)[^{]*\{[^}]*\}/g) || [];
  wide.forEach((r) => {
    const at = css.indexOf(r);
    expect(css.slice(css.lastIndexOf('@media', at), at)).toContain('@media (min-width: 700px) {');
  });
});
