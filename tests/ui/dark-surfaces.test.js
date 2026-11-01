const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

const UI = path.resolve(__dirname, '..', '..', 'ui');
const read = (name) => fs.readFileSync(path.join(UI, name), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
const theme = read('forge-theme.css');
const blacksmith = read('blacksmith.css');

const block = (css, selector) => {
  const at = css.indexOf(`${selector} {`);
  expect(at).toBeGreaterThan(-1);
  return css.slice(at, css.indexOf('}', at));
};

const hex = (value) => {
  const n = parseInt(value.slice(1), 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255].map((c) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
};
const lum = (value) => { const [r, g, b] = hex(value); return 0.2126 * r + 0.7152 * g + 0.0722 * b; };
const ratio = (a, b) => { const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x); return (hi + 0.05) / (lo + 0.05); };

test('the dark theme defines its surface tokens in one block', () => {
  const dark = block(theme, '[data-theme="dark"]');
  const tokens = {};
  for (const name of ['page', 'nav', 'content', 'raised', 'border', 'muted-text']) {
    const m = dark.match(new RegExp(`--surface-${name}:\\s*(#[0-9a-f]{6})`, 'i'));
    expect(m).not.toBeNull();
    tokens[name] = m[1];
  }
  expect(new Set([tokens.page, tokens.nav, tokens.content, tokens.raised]).size).toBe(4);
  expect(ratio(tokens['muted-text'], tokens.raised)).toBeGreaterThanOrEqual(4.5);
  expect(dark).toMatch(/--background:\s*var\(--surface-page\)/);
  expect(dark).toMatch(/--card:\s*var\(--surface-content\)/);
  expect(dark).toMatch(/--secondary:\s*var\(--surface-raised\)/);
  expect(dark).toMatch(/--border:\s*var\(--surface-border\)/);
  expect(dark).toMatch(/--muted-foreground:\s*var\(--surface-muted-text\)/);
});

test('the Tasks toolbar has no one-off near-black background', () => {
  expect(blacksmith).not.toMatch(/#0d1117/i);
  const rule = block(blacksmith, '[data-theme="dark"] .compact-tasks-controls');
  expect(rule).toMatch(/var\(--surface-/);
});

test('the dark side navigation and sub-tabs carry a fill and the accent edge', () => {
  const nav = block(theme, '[data-theme="dark"] #blacksmith .entity-navigation .detail-tab.active, [data-theme="dark"] #services .entity-navigation .detail-tab.active');
  expect(nav).toMatch(/background:\s*var\(--surface-raised\)/);
  expect(nav).toMatch(/border-left-color:\s*var\(--primary\)/);
  for (const tab of ['manifest-tab-btn', 'cert-tab-btn']) {
    const rule = block(theme, `[data-theme="dark"] .${tab}.active`);
    expect(rule).toMatch(/background:\s*var\(--surface-raised\)/);
    expect(rule).toMatch(/box-shadow:[^;]*var\(--primary\)/);
  }
});

test('dark panels, nested cards, and tables sit on different surfaces', () => {
  const metric = block(theme, '[data-theme="dark"] .entity-metric.card');
  expect(metric).toMatch(/background:\s*var\(--surface-raised\)/);
  const cell = block(theme, '[data-theme="dark"] .main-content table td');
  expect(cell).toMatch(/background:\s*var\(--surface-page\)/);
  expect(theme).toMatch(/\[data-theme="dark"\] #blacksmith \.entity-navigation[^{]*\{[^}]*var\(--surface-nav\)/);
});
