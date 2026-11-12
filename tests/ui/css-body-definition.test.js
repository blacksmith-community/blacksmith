const { test, expect } = require('bun:test');
const { sheets, rules, decl } = require('./css-support.js');

// A selector list that names the bare body element, with no class, child, or state.
const isBodyRule = (r) => r.selector.split(',').some((s) => s.trim() === 'body');

const bodyRules = () => sheets().flatMap((file) => rules(file).filter((r) => !r.at && isBodyRule(r)).map((r) => ({ file, ...r })));

test('the page body is defined once, in blacksmith.css', () => {
  const found = bodyRules();
  expect(found.map((r) => r.file)).toEqual(['blacksmith.css']);
});

test('the single body rule carries the page typography, color, and layout', () => {
  const [rule] = bodyRules();
  expect(rule).toBeDefined();
  for (const prop of ['font-family', 'font-size', 'line-height', 'color', 'background', 'margin', 'padding', 'display', 'flex-direction', 'min-height']) {
    expect(decl(rule.body, prop)).not.toBeNull();
  }
  expect(decl(rule.body, 'font-family')).toContain('-apple-system');
  expect(decl(rule.body, 'color')).toBe('var(--text-primary)');
  expect(decl(rule.body, 'background')).toBe('var(--background)');
});

test('the body does not fade its colors when the theme changes', () => {
  const [rule] = bodyRules();
  expect(decl(rule.body, 'transition')).toBe('none');
});
