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

test('the terminal header keeps its controls inside a narrow dialog', () => {
  expect(declares(blacksmith, '.terminal-title-details', 'flex-wrap: wrap')).toBe(true);
  expect(declares(blacksmith, '.terminal-title-seg', 'overflow-wrap: anywhere')).toBe(true);
  expect(declares(blacksmith, '.terminal-header-controls', 'flex-shrink: 0')).toBe(true);
  expect(declares(blacksmith, '.terminal-title', 'min-width: 0')).toBe(true);
  expect(declares(blacksmith, '.terminal-title', 'flex: 1 1 auto')).toBe(true);
});

test('the minimized bar lays out its sections and resets the tab buttons', () => {
  const html = read('index.html');
  const bar = html.slice(html.indexOf('id="minimized-terminal-bar"'));
  expect(bar).toMatch(/<div class="left-section">\s*<img/);
  expect(bar).toMatch(/<div class="right-section">\s*<button class="terminal-control-btn"/);
  expect(declares(forge, '.minimized-terminal-tab > button', 'min-height: 0')).toBe(true);
  expect(declares(forge, '.minimized-terminal-tab > button', 'background: transparent')).toBe(true);
  expect(declares(forge, '.minimized-terminal-tab > button', 'border: 0')).toBe(true);
  expect(declares(forge, '.minimized-terminal-tab > button', 'color: inherit')).toBe(true);
  expect(declares(forge, '.minimized-terminal-tab', 'padding: 2px 8px')).toBe(true);
});

test('the terminal host height is a whole number of rows', () => {
  const [host] = find(blacksmith, '#terminal-modal .terminal-instance');
  expect(host).toBeDefined();
  expect(host.body).toMatch(/height:\s*100%\s*;\s*height:\s*round\(down,\s*100%,\s*var\(--term-row-height,\s*17px\)\)\s*;/);
  expect(host.body).not.toMatch(/round\(down,\s*100%,\s*1px\)/);
});

test('the block padding sits on the container so the host holds only rows', () => {
  expect(declares(blacksmith, '#terminal-modal .terminal-container', 'padding-block: 12px')).toBe(true);
  expect(declares(blacksmith, '#terminal-modal .terminal-instance.wterm', 'padding-block: 0')).toBe(true);
});
