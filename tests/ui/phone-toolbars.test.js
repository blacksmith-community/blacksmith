const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

const css = fs.readFileSync(path.join(__dirname, '..', '..', 'ui', 'blacksmith.css'), 'utf8');

const block = () => {
  const start = css.indexOf('@media (max-width: 699.98px) { /* phone toolbars */');
  expect(start).toBeGreaterThan(-1);
  let depth = 0;
  for (let i = css.indexOf('{', start); i < css.length; i += 1) {
    if (css[i] === '{') depth += 1;
    if (css[i] === '}') { depth -= 1; if (depth === 0) return css.slice(start, i + 1); }
  }
  throw new Error('unterminated media block');
};

test('the Tasks Type and Scope selects take the room their values need', () => {
  const rules = block();
  expect(rules).toMatch(/compact-tasks-controls\.table-controls-container \.filter-group \{[^}]*flex: 1 1 100%/);
  expect(rules).toMatch(/compact-tasks-controls\.table-controls-container \.filter-group select \{[^}]*flex: 1 1 auto[^}]*min-width: 0/);
});

test('the five state checkboxes wrap into a compact row', () => {
  const rules = block();
  expect(rules).toMatch(/compact-tasks-controls\.table-controls-container \.checkbox-group \{[^}]*flex-wrap: wrap/);
  expect(rules).toMatch(/compact-tasks-controls\.table-controls-container \.checkbox-group \{[^}]*gap: 6px 12px/);
});

test('table toolbars wrap at every width so no button leaves the row', () => {
  const rules = css;
  expect(rules).toMatch(/\.table-controls-container \{[^}]*flex-wrap: wrap/);
  expect(rules).toMatch(/\.table-toolbar-actions \{[^}]*flex-wrap: wrap/);
  expect(rules).toMatch(/resurrection-toggle-container \{[^}]*flex: 1 1 auto[^}]*min-width: 0/);
});

test('the phone toolbar rules never shrink a control below the coarse-pointer size', () => {
  const rules = block();
  expect(rules).not.toMatch(/min-height:\s*(?!44)\d+px/);
  expect(rules).not.toMatch(/\b(width|height):\s*\d+px/);
});

test('the log file select fills its row under 700px instead of running past the edge', () => {
  const rules = block();
  expect(rules).toMatch(/logs-controls-row \.log-file-selector \{[^}]*flex: 1 1 100%[^}]*min-width: 0/);
  expect(rules).toMatch(/logs-controls-row \.log-file-selector select \{[^}]*width: 100%[^}]*max-width: 100%/);
});
