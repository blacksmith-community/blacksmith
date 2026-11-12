const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

const ui = path.resolve(__dirname, '../../ui');
const read = (f) => fs.readFileSync(path.join(ui, f), 'utf8');

// A scroll container pins its sticky header at the top of its content box,
// so any padding on the container leaves a gap that rows scroll through. The
// instance detail pane pads every div inside it with an id selector, which
// beats the plain class rule that clears the padding on the log container.
test('the pane padding rule is the id-selector rule that pads every div', () => {
  expect(read('blacksmith.css')).toMatch(/#services \.detail-content div\s*\{\s*padding:\s*8px;/);
});

test('the instance log scroll container clears padding above the sticky header', () => {
  const css = read('forge-theme.css');
  const rule = css.match(/(^|\n)(#services[^{}]*\.job-logs-container\s*>\s*\.logs-table-container)\s*\{([^}]*)\}/);
  expect(rule).not.toBeNull();
  expect(rule[3]).toMatch(/padding:\s*0\b/);
  expect(rule[3]).not.toMatch(/border-top\s*:\s*[1-9]/);
});

test('the log table header cells stay pinned flush at the top', () => {
  const css = read('forge-theme.css');
  expect(css).toMatch(/\.job-logs-container \.instance-logs-table thead th\s*\{[^}]*top:\s*0;/);
  expect(css).toMatch(/\.main-content table thead th\s*\{[^}]*position:\s*sticky;\s*top:\s*0;/);
});
