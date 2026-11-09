const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

// The terminal's own rules in blacksmith.css. The sizing and visibility of
// .terminal-instance live in one place, the selection matches the modal's
// palette, and a failed terminal pads its message once.
const css = fs.readFileSync(path.resolve(__dirname, '../../ui/blacksmith.css'), 'utf8');
const rules = [];
css.replace(/\/\*[\s\S]*?\*\//g, '').replace(/([^{}]+)\{([^{}]*)\}/g, (m, sel, body) => { rules.push({ sel: sel.trim(), body }); return m; });
const rule = (sel) => rules.filter((r) => r.sel === sel);
const has = (body, prop) => new RegExp(`(^|[;\\s])${prop}\\s*:`).test(body);

test('the wterm rule sets only the theme and the frame, not size or visibility', () => {
  const [wterm] = rule('#terminal-modal .terminal-instance.wterm');
  expect(wterm).toBeDefined();
  ['width', 'height', 'display'].forEach((prop) => expect(has(wterm.body, prop)).toBe(false));
  expect(rule('#terminal-modal .terminal-instance.wterm.active')).toEqual([]);
  const [instance] = rule('.terminal-instance');
  ['width', 'height', 'display'].forEach((prop) => expect(has(instance.body, prop)).toBe(true));
});

test('selected terminal text uses the modal palette, not the wterm default', () => {
  const [selection] = rule('#terminal-modal .wterm ::selection');
  expect(selection).toBeDefined();
  expect(selection.body).toMatch(/background:\s*rgba\(255,\s*255,\s*255,\s*0\.3\)/);
});

test('a failed terminal pads its message once', () => {
  const [message] = rule('#terminal-modal .terminal-load-error');
  expect(message.body).toMatch(/padding:\s*0\s*;/);
  const [failed] = rule('#terminal-modal .terminal-instance.terminal-failed');
  expect(failed).toBeDefined();
  expect(failed.body).toMatch(/padding:\s*12px\s*;/);
});
