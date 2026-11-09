const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

// The terminal modal's tab strip, header, and minimized bar sit inside
// Oat's dialog styling, so each rule here undoes one inherited default.
const read = (f) => fs.readFileSync(path.resolve(__dirname, '../../ui', f), 'utf8');
const parse = (css) => {
  const rules = [];
  css.replace(/\/\*[\s\S]*?\*\//g, '').replace(/([^{}]+)\{([^{}]*)\}/g, (m, sel, body) => { rules.push({ sel: sel.trim(), body }); return m; });
  return rules;
};
const blacksmith = parse(read('blacksmith.css'));
const forge = parse(read('forge-theme.css'));
const find = (rules, sel) => rules.filter((r) => r.sel === sel);
const declares = (rules, sel, decl) => find(rules, sel).some((r) => new RegExp(`(^|[;\\s])${decl.replace(/[()]/g, '\\$&').replace(/:\s*/, '\\s*:\\s*')}\\s*(;|$)`).test(r.body));

test('the tab strip drops the padding Oat gives a dialog child', () => {
  expect(declares(blacksmith, '.terminal-tabs', 'padding: 0')).toBe(true);
});

test('terminal tabs are compact: 4px vertical padding and no button height floor', () => {
  expect(declares(forge, '.terminal-tab', 'padding: 4px 12px')).toBe(true);
  expect(declares(forge, '.terminal-tab > button', 'min-height: 0')).toBe(true);
});
