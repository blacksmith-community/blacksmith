const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { UI } = require('./harness.js');

const css = fs.readFileSync(path.join(UI, 'blacksmith.css'), 'utf8');

// Return the declarations of the first rule whose selector list matches.
const rule = (selector, from = 0) => {
  const at = css.indexOf(`${selector} {`, from);
  if (at < 0) return null;
  return css.slice(css.indexOf('{', at) + 1, css.indexOf('}', at));
};
const padding = (block) => {
  const m = block && /(?:^|\n)\s*padding:\s*([^;]+);/.exec(block);
  return m ? m[1].trim() : null;
};

test('the certificate settings dialog pads its body, header, and actions like the shared dialogs', () => {
  const sharedBody = padding(rule('.dialog-modal-body'));
  const sharedHeader = padding(rule('.dialog-modal-header'));
  expect(sharedBody).toBe('20px');
  expect(padding(rule('.cert-settings-modal .modal-body'))).toBe(sharedBody);
  expect(padding(rule('.modal-header'))).toBe(sharedHeader);
  expect(rule('.cert-settings-modal .modal-body')).toMatch(/overflow-y:\s*auto/);
  expect(rule('.cert-settings-actions')).toMatch(/padding-top:\s*16px/);
});

test('the narrow layout matches the shared narrow dialog padding', () => {
  const narrow = css.slice(css.indexOf('.cert-settings-actions {'));
  expect(narrow).toMatch(/@media \(max-width: 480px\) \{\s*\.cert-settings-modal \.modal-header \{\s*padding: 12px 14px;/);
  expect(narrow).toMatch(/\.cert-settings-modal \.modal-body \{\s*padding: 12px 14px;/);
});
