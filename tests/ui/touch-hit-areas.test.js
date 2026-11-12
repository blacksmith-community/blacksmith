const { test, expect } = require('bun:test');
const { coarseRules, decl } = require('./touch-support.js');

const rules = coarseRules('touch.css');
const px = (v) => (v ? parseFloat(v) : 0);
// Returns the declarations that apply to a selector fragment, merged.
const forClass = (name) => rules.filter((r) => r.selector.includes(name)).map((r) => r.body).join(';');

const SQUARES = [
  '.copy-btn-inline', '.copy-btn-logs', '.copy-btn-manifest', '.copy-btn', '.secret-toggle', '.secret-copy',
  '.entity-copy', '.ssh-btn', '.delete-resurrection-btn', '.refresh-btn-logs', '.copy-deployment-names-btn',
  '.clear-filters-btn', '.vm-expand', '.tab-close', '.cert-horizon-btn', '.task-link', '.config-link', '.bosh-task-link',
  // The instance VMs toolbar: the resurrection button, plus the refresh and copy buttons named above.
  '.resurrection-action',
];

for (const name of SQUARES) {
  test(`on a coarse pointer ${name} has a 44 by 44 pixel hit area`, () => {
    const body = forClass(name);
    expect(px(decl(body, 'min-width'))).toBeGreaterThanOrEqual(44);
    expect(px(decl(body, 'min-height'))).toBeGreaterThanOrEqual(44);
  });
}

test('on a coarse pointer buttons, summaries, and selects are at least 44 pixels tall', () => {
  const rule = rules.find((r) => /(^|[:(, ])button\b/.test(r.selector) && /\bselect\b/.test(r.selector) && /\bsummary\b/.test(r.selector) && decl(r.body, 'min-height'));
  expect(rule).toBeDefined();
  expect(px(decl(rule.body, 'min-height'))).toBeGreaterThanOrEqual(44);
  // Component rules set 24 to 32 pixel minimums behind ids, so this has to win.
  expect(rule.body).toContain('!important');
});

test('the main tab strip and the breadcrumb are left to their own rules', () => {
  for (const r of rules) {
    expect(r.selector).not.toMatch(/(^|[ ,>])\.tabs(\s|$|,)/);
    expect(r.selector).not.toMatch(/(^|[ ,>])\.route-context\s*[a-z.]/);
  }
  const generic = rules.find((r) => /\bsummary\b/.test(r.selector) && decl(r.body, 'min-height'));
  expect(generic.selector).toMatch(/:not\(:is\(\.tabs, \.route-context\) \*\)/);
});

test('a label around a checkbox is a 44 pixel target and the box itself is 24 pixels', () => {
  const label = rules.find((r) => /^label:has\(/.test(r.selector) && /checkbox/.test(r.selector));
  expect(label).toBeDefined();
  expect(px(decl(label.body, 'min-height'))).toBeGreaterThanOrEqual(44);
  expect(px(decl(label.body, 'min-width'))).toBeGreaterThanOrEqual(44);
  expect(decl(label.body, 'display')).toBe('inline-flex');
  const box = rules.find((r) => /input\[type="checkbox"\]/.test(r.selector) && decl(r.body, 'width'));
  expect(box).toBeDefined();
  expect(px(decl(box.body, 'width'))).toBeGreaterThanOrEqual(24);
  expect(px(decl(box.body, 'height'))).toBeGreaterThanOrEqual(24);
});

test('on a coarse pointer a button or select is never narrower than 44 pixels', () => {
  const rule = rules.find((r) => /\bbutton\b/.test(r.selector) && /\bselect\b/.test(r.selector) && decl(r.body, 'min-width'));
  expect(rule).toBeDefined();
  expect(px(decl(rule.body, 'min-width'))).toBeGreaterThanOrEqual(44);
  expect(rule.body).toContain('!important');
});

test('on a coarse pointer a link that leads a list item or stands alone is 44 pixels tall', () => {
  const rule = rules.find((r) => /li > a\[href\]:first-child, p > a\[href\]:only-child/.test(r.selector));
  expect(rule).toBeDefined();
  expect(px(decl(rule.body, 'min-height'))).toBeGreaterThanOrEqual(44);
  expect(decl(rule.body, 'display')).toBe('inline-flex');
  expect(rule.selector).toContain(':not(:is(.tabs, .route-context) *)');
});
