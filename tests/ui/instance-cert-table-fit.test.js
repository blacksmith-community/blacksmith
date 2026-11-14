const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

const css = fs.readFileSync(path.resolve(__dirname, '../../ui/blacksmith.css'), 'utf8');
const rule = (selector) => {
  const at = css.indexOf('\n' + selector + ' {');
  expect(at).toBeGreaterThan(-1);
  return css.slice(at, css.indexOf('}', at));
};

// jsdom has no layout, so this checks the rules that keep the Certificate
// column from taking the width of the whole table.
test('the Certificate column wraps long text and leaves room for Expires', () => {
  const name = rule('.cert-ov-table .cert-ov-name');
  expect(name).toMatch(/overflow-wrap:\s*anywhere/);
  expect(name).toMatch(/width:\s*100%/);
  expect(name).toMatch(/max-width:\s*0/);
});

test('every other column of the certificate overview table stays on one line', () => {
  const rest = rule('.cert-ov-table td:not(.cert-ov-name)');
  expect(rest).toMatch(/white-space:\s*nowrap/);
});
