const fs = require('fs');
const path = require('path');
const { test, expect } = require('bun:test');
const { UI, sheets } = require('./css-support.js');

// The named set of max-width breakpoints, written in the comment at the top of
// blacksmith.css. CSS custom properties cannot be used inside a media query, so
// the comment and this test are what keep the set in one place.
const NAMED = [1099.98, 900, 768, 699.98, 480];
// Widths that a few rules keep for themselves, each marked in the source with a
// comment that starts "Own breakpoint".
const OWN = [800, 750, 600, 540];

const maxWidths = () => {
  const found = [];
  for (const file of sheets()) {
    const lines = fs.readFileSync(path.join(UI, file), 'utf8').split('\n');
    lines.forEach((line, i) => {
      if (!/@media/.test(line)) return;
      for (const m of line.matchAll(/max-width:\s*([\d.]+)px/g)) found.push({ file, line: i + 1, width: parseFloat(m[1]), before: lines[i - 1] || '', text: line });
    });
  }
  return found;
};

test('every max-width media query uses a named breakpoint or marks its own', () => {
  const stray = maxWidths().filter((q) => !NAMED.includes(q.width) && !OWN.includes(q.width));
  expect(stray.map((q) => `${q.file}:${q.line} max-width ${q.width}px`)).toEqual([]);
});

test('a rule at its own breakpoint says why on the line before it', () => {
  const unmarked = maxWidths().filter((q) => OWN.includes(q.width) && !/\/\*\s*Own breakpoint/.test(q.before + q.text));
  expect(unmarked.map((q) => `${q.file}:${q.line} max-width ${q.width}px`)).toEqual([]);
});

test('blacksmith.css lists the breakpoint set in a comment at the top', () => {
  const head = fs.readFileSync(path.join(UI, 'blacksmith.css'), 'utf8').slice(0, 2500);
  expect(head).toMatch(/\/\*[\s\S]*Breakpoints[\s\S]*\*\//);
  for (const width of NAMED) expect(head).toContain(String(width));
});

test('the named breakpoints are all still in use', () => {
  const used = new Set(maxWidths().map((q) => q.width));
  for (const width of NAMED) expect(used.has(width)).toBe(true);
});
