const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { UI } = require('./harness.js');

const css = fs.readFileSync(path.join(UI, 'blacksmith.css'), 'utf8');
const html = fs.readFileSync(path.join(UI, 'index.html'), 'utf8');

// jsdom does not compute layout, so these checks read the declarations.
const rule = (selector, from = 0) => {
  const at = css.indexOf(`\n${selector} {`, from);
  if (at < 0) return null;
  return css.slice(css.indexOf('{', at) + 1, css.indexOf('}', at));
};

test('the instance log panel has a definite height of its own', () => {
  const content = rule('.instance-logs-content');
  expect(content).toMatch(/flex:\s*none;/);
  expect(content).toMatch(/height:\s*70dvh;/);
  expect(content).toMatch(/min-height:\s*320px;/);
});

test('the log container fills the panel and scrolls inside it', () => {
  const container = rule('.job-logs-container');
  expect(container).toMatch(/position:\s*static;/);
  expect(container).toMatch(/height:\s*100%;/);
  expect(container).not.toMatch(/position:\s*absolute/);
});

test('the VM tab row scrolls sideways and its tabs keep their width', () => {
  expect(rule('.instance-log-job-tabs')).toMatch(/overflow-x:\s*auto;/);
  const tab = rule('.instance-log-job-tab');
  expect(tab).toMatch(/flex:\s*0 0 auto;/);
  expect(tab).toMatch(/white-space:\s*nowrap;/);
  expect(css).toMatch(/@media \(max-width: 700px\) \{\s*\.instance-log-job-tab \{\s*padding:\s*10px 12px;/);
});

test('the stylesheet reference carries the current cache-buster', () => {
  expect(html).toContain('/blacksmith.css?v=20261112-oat-393');
});
