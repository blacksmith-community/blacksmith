const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

const UI = path.resolve(__dirname, '..', '..', 'ui');
const forge = fs.readFileSync(path.join(UI, 'forge-theme.css'), 'utf8');
const inspection = fs.readFileSync(path.join(UI, 'inspection-layout.css'), 'utf8');
const rule = (selector) => {
  const at = forge.indexOf('\n' + selector + ' {');
  return at < 0 ? '' : forge.slice(at, forge.indexOf('}', at));
};

const HEAD = 'table > thead > tr:first-child > th';

test('one shared rule gives every table header cell a stronger edge and a slight tint', () => {
  const shared = rule(HEAD);
  expect(shared).not.toBe('');
  expect(shared).toMatch(/inset 0 1px 0 var\(--table-head-edge\)/);
  expect(shared).toMatch(/inset 0 -1px 0 var\(--table-head-edge\)/);
  expect(shared).toMatch(/background-image:\s*linear-gradient\(var\(--table-head-tint\), var\(--table-head-tint\)\)/);
});

test('the first and last header cells round the top corners and draw the side edges', () => {
  const first = rule(HEAD + ':first-child');
  const last = rule(HEAD + ':last-child');
  expect(first).toMatch(/border-top-left-radius:\s*var\(--table-radius\)/);
  expect(first).toMatch(/inset 1px 0 0 var\(--table-head-edge\)/);
  expect(last).toMatch(/border-top-right-radius:\s*var\(--table-radius\)/);
  expect(last).toMatch(/inset -1px 0 0 var\(--table-head-edge\)/);
  const only = rule(HEAD + ':only-child');
  expect(only).toMatch(/border-top-left-radius/);
  expect(only).toMatch(/border-top-right-radius/);
});

test('the edge, tint, and radius tokens exist in both themes', () => {
  const light = forge.slice(forge.indexOf(':root {'), forge.indexOf('[data-theme="dark"] {'));
  const dark = forge.slice(forge.indexOf('[data-theme="dark"] {'), forge.indexOf(':root, [data-theme="dark"] {'));
  expect(light).toMatch(/--table-head-edge:/);
  expect(light).toMatch(/--table-head-tint:/);
  expect(light).toMatch(/--table-radius:\s*6px/);
  expect(dark).toMatch(/--table-head-edge:/);
  expect(dark).toMatch(/--table-head-tint:/);
});

test('the rounded header leaves the sticky position and the flush offset alone', () => {
  expect(rule('.main-content table thead th')).toMatch(/position:\s*sticky;\s*top:\s*0/);
  expect(rule('.job-logs-container .instance-logs-table thead th')).toMatch(/top:\s*0/);
  expect(forge).toMatch(/\.logs-table-container[^{]*\{[^}]*border-radius:\s*var\(--table-radius\)/);
});

test('the shared rule does not touch the task dialog facts table rules', () => {
  expect(inspection).not.toMatch(/table-head-edge|table-radius/);
});
