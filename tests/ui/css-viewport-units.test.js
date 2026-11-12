const { test, expect } = require('bun:test');
const { sheets, rules } = require('./css-support.js');

// Splits a rule body into [property, value] pairs in source order.
const declarations = (body) => body.split(';').map((d) => d.trim()).filter(Boolean).map((d) => {
  const i = d.indexOf(':');
  return [d.slice(0, i).trim().toLowerCase(), d.slice(i + 1).trim()];
});

const usesVh = (value) => /\d(\.\d+)?vh\b/.test(value);
const toDvh = (value) => value.replace(/(\d)vh\b/g, '$1dvh');

// The mobile browser toolbar changes the visible height, and vh ignores it.
// Each vh height keeps its own declaration as a fallback and is followed by
// the same declaration in dvh.
test('every vh length is followed by the same declaration in dvh', () => {
  const missing = [];
  for (const file of sheets()) {
    for (const r of rules(file)) {
      const list = declarations(r.body);
      list.forEach(([prop, value], i) => {
        if (!usesVh(value)) return;
        const next = list[i + 1];
        const paired = next && next[0] === prop && (next[1] === toDvh(value) || next[1].startsWith('round(down, ') && next[1].includes('dvh'));
        if (!paired) missing.push(`${file}: ${r.selector} { ${prop}: ${value} }`);
      });
    }
  }
  expect(missing).toEqual([]);
});

test('the sheets still declare viewport heights, so the pairing test has something to check', () => {
  let count = 0;
  for (const file of sheets()) for (const r of rules(file)) for (const [, value] of declarations(r.body)) if (usesVh(value)) count++;
  expect(count).toBeGreaterThan(40);
});
