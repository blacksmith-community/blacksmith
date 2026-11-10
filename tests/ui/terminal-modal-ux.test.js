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
  expect(declares(blacksmith, '#terminal-modal .terminal-tabset', 'grid-template-rows: 40px 12px minmax(0, 1fr) 12px')).toBe(true);
  expect(declares(blacksmith, '#terminal-modal .terminal-instance.wterm', 'padding-block: 0')).toBe(true);
});

test('the dialog has no title row, only one top row of tabs and controls', () => {
  const html = read('index.html');
  const dialog = html.slice(html.indexOf('<dialog id="terminal-modal"'), html.indexOf('</dialog>', html.indexOf('<dialog id="terminal-modal"')));
  expect(dialog).not.toMatch(/terminal-title|terminal-header(?!-)|<h2/);
  expect(dialog).toMatch(/<bs-tabs class="terminal-tabset"[^>]*>\s*<div class="terminal-tabs[^"]*" id="terminal-tabs" role="tablist"[^>]*><\/div>\s*<div class="terminal-header-controls">/);
  expect(dialog).not.toMatch(/terminal-container|terminal-top/);
  expect(dialog).toMatch(/id="terminal-keyboard-help"/);
  expect(blacksmith.some((r) => /terminal-title|\.terminal-header\s*$/.test(r.sel))).toBe(false);
});

test('the top row is a fixed 40px grid row with the tabs taking the free width', () => {
  expect(declares(blacksmith, '#terminal-modal .terminal-tabset', 'display: grid')).toBe(true);
  expect(declares(blacksmith, '#terminal-modal .terminal-tabset', 'grid-template-columns: minmax(0, 1fr) auto')).toBe(true);
  expect(declares(blacksmith, '#terminal-modal .terminal-tabset > [role="tablist"]', 'height: 40px')).toBe(true);
  expect(declares(blacksmith, '#terminal-modal .terminal-tabset > [role="tablist"]', 'flex-wrap: nowrap')).toBe(true);
  expect(declares(blacksmith, '#terminal-modal .terminal-tabset > [role="tablist"]', 'min-width: 120px')).toBe(true);
  expect(declares(blacksmith, '#terminal-modal .terminal-tabset > [role="tablist"]', 'overflow-x: auto')).toBe(true);
  expect(declares(blacksmith, '#terminal-modal .terminal-tabset > .terminal-header-controls', 'height: 40px')).toBe(true);
  expect(declares(blacksmith, '.terminal-header-controls', 'flex-shrink: 0')).toBe(true);
});

test('the tabs round only their top corners and the selected one joins the terminal', () => {
  expect(declares(blacksmith, '#terminal-modal .terminal-tabset .terminal-tab', 'border-radius: var(--radius-large) var(--radius-large) 0 0')).toBe(true);
  expect(declares(blacksmith, '#terminal-modal .terminal-tabset .terminal-tab.active', 'background: var(--terminal-frame-bg, #000)')).toBe(true);
});

test('a tab carries the full deployment name in its tooltip', () => {
  const js = read('blacksmith.js');
  const at = js.indexOf('createTab(sessionKey, context, panel) {');
  expect(js.slice(at, at + 1800)).toMatch(/tab\.title\s*=|select\.title\s*=/);
});
