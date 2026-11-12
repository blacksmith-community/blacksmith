const { test, expect } = require('bun:test');
const { sheets, rules, decl } = require('./css-support.js');

// Font sizes follow the reader's default text size, so they are written in rem.
// The one pixel size we keep is the 16px text-control rule in touch.css, because
// iOS Safari zooms the page on focus when a control is under exactly 16px.
const ALLOWED = [
  { file: 'touch.css', at: '@media (pointer: coarse)', value: '16px', selectorHas: 'input[type="text"]' }
];

const pixelFontSizes = () => {
  const found = [];
  for (const file of sheets()) {
    for (const r of rules(file)) {
      for (const m of r.body.matchAll(/(?:^|;)\s*font-size\s*:\s*([^;]+)/gi)) {
        const value = m[1].replace(/\s*!important/, '').trim();
        if (/(^|[\s(,*+/-])[\d.]+px\b/.test(value)) found.push({ file, at: r.at, selector: r.selector, value });
      }
    }
  }
  return found;
};

test('font sizes are written in rem, except the iOS text control rule', () => {
  const stray = pixelFontSizes().filter((f) => !ALLOWED.some((a) => a.file === f.file && a.at === f.at && a.value === f.value && f.selector.includes(a.selectorHas)));
  expect(stray.map((f) => `${f.file}: ${f.selector} { font-size: ${f.value} }`)).toEqual([]);
});

test('the iOS text control rule keeps its 16px size', () => {
  const kept = pixelFontSizes();
  expect(kept.length).toBe(ALLOWED.length);
  expect(kept[0].value).toBe('16px');
  const rule = rules('touch.css').find((r) => r.at === '@media (pointer: coarse)' && r.selector.includes('input[type="text"]') && decl(r.body, 'font-size'));
  expect(rule.body).toContain('font-size: 16px !important');
});

test('the stylesheets declare many rem font sizes, so the guard has something to protect', () => {
  let rem = 0;
  for (const file of sheets()) for (const r of rules(file)) for (const m of r.body.matchAll(/font-size\s*:\s*[\d.]+rem/gi)) rem++;
  expect(rem).toBeGreaterThan(150);
});
