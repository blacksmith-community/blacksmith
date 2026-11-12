const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

const UI = path.resolve(__dirname, '..', '..', 'ui');
const read = (file) => fs.readFileSync(path.join(UI, file), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
const rule = (css, selector) => {
  const out = [];
  css.replace(/([^{}]+)\{([^{}]*)\}/g, (m, sel, body) => {
    if (sel.split(',').map((s) => s.replace(/\s+/g, ' ').trim()).includes(selector)) out.push(body);
    return m;
  });
  return out.join(';');
};

// A container that hides its overflow throws away the columns it cannot
// fit. Each of these scrolls inside itself instead, so every column stays
// reachable at 390px and 768px.
test('the Configs table container scrolls sideways instead of clipping', () => {
  const body = rule(read('blacksmith.css'), '.configs-table-container');
  expect(body).toMatch(/overflow-x:\s*auto/);
  expect(body).not.toMatch(/overflow:\s*hidden/);
});

test('the Credentials box scrolls sideways instead of clipping', () => {
  const body = rule(read('blacksmith.css'), '.credentials-container');
  expect(body).toMatch(/overflow-x:\s*auto/);
  expect(body).not.toMatch(/overflow-x:\s*hidden/);
});

test('the task dialog summary scrolls sideways and wraps its cells at tablet width', () => {
  const css = read('inspection-layout.css');
  const body = rule(css, '#task-details-modal .task-summary-section');
  expect(body).toMatch(/overflow-x:\s*auto/);
  expect(body).not.toMatch(/overflow:\s*hidden/);
  const block = css.match(/@media \(max-width: 900px\)\s*\{([\s\S]*?\n\})/);
  expect(block).not.toBeNull();
  expect(block[1]).toMatch(/task-summary-table td:not\(\.task-meta-description\)[^{]*\{[^}]*white-space:\s*normal/);
});
