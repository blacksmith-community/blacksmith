const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

const css = fs.readFileSync(path.resolve(__dirname, '..', '..', 'ui', 'blacksmith.css'), 'utf8');

// The block that applies from 700px up, found by its comment.
const block = () => {
  const start = css.indexOf('@media (min-width: 700px) { /* compact certificate header */');
  expect(start).toBeGreaterThan(-1);
  let depth = 0;
  for (let i = css.indexOf('{', start); i < css.length; i += 1) {
    if (css[i] === '{') depth += 1;
    if (css[i] === '}') { depth -= 1; if (depth === 0) return css.slice(start, i + 1); }
  }
  throw new Error('unterminated media block');
};

// At 1280 by 800 the first certificate row has to show without scrolling.
// The page keeps every control and count, and saves the height by tightening
// spacing and by sharing rows.
test('the Custom range button shares a row with the timeline', () => {
  const rules = block();
  expect(rules).toMatch(/#cert-fleet-main \{[^}]*display: grid[^}]*grid-template-columns: minmax\(0, 1fr\) auto/);
  expect(rules).toMatch(/> \.cert-timeline-wrap \{[^}]*grid-column: 1;/);
  expect(rules).toMatch(/> \.cert-range-wrap \{[^}]*grid-column: 2;/);
  // An opened range panel takes the full row, so its fields never squeeze.
  expect(rules).toMatch(/\.cert-range-wrap:has\(> \.cert-range:not\(\[hidden\]\)\) \{[^}]*grid-column: 1 \/ -1/);
});

test('the active filters share a row with the shown count', () => {
  const rules = block();
  expect(rules).toMatch(/> \.cert-chips \{[^}]*grid-column: 1;/);
  expect(rules).toMatch(/> \.cert-selection-line \{[^}]*grid-column: 2;/);
});

test('each filter label sits beside its field', () => {
  expect(block()).toMatch(/\.cert-field \{[^}]*flex-direction: row/);
});

test('the notices, horizon, and chips use tighter spacing', () => {
  const rules = block();
  expect(rules).toMatch(/\.cert-notice \{[^}]*padding: 4px 10px/);
  expect(rules).toMatch(/#cert-horizon \.cert-horizon-table :is\(th, td\) \{[^}]*padding-block: 1px/);
  expect(rules).toMatch(/\.cert-strips \{[^}]*margin-bottom: 4px/);
});

test('the compact rules never go below the coarse-pointer control size', () => {
  const rules = block();
  expect(rules).not.toMatch(/\bheight:\s*\d+px/);
  expect(css.slice(css.indexOf('/* Compact fleet header.'))).not.toMatch(/\(pointer: coarse\)/);
});

test('the compact rules leave the control heights alone', () => {
  expect(block()).not.toMatch(/min-height/);
});
