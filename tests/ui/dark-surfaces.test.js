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

const surfaces = (css, selector) => {
  const out = {};
  for (const name of ['page', 'nav', 'content', 'raised', 'border', 'muted-text']) {
    const m = block(css, selector).match(new RegExp(`--surface-${name}:\\s*(#[0-9a-f]{6})`, 'i'));
    expect(m).not.toBeNull();
    out[name] = m[1];
  }
  return out;
};

test('the dark theme defines its surface tokens in one block', () => {
  const dark = block(theme, '[data-theme="dark"]');
  const tokens = surfaces(theme, '[data-theme="dark"]');
  // Each surface is visibly separate from the page, and the raised one most of all.
  expect(ratio(tokens.content, tokens.page)).toBeGreaterThanOrEqual(1.2);
  expect(ratio(tokens.nav, tokens.page)).toBeGreaterThanOrEqual(1.2);
  expect(ratio(tokens.raised, tokens.page)).toBeGreaterThan(ratio(tokens.content, tokens.page));
  expect(ratio(tokens.raised, tokens.page)).toBeGreaterThan(ratio(tokens.nav, tokens.page));
  for (const surface of ['page', 'nav', 'content', 'raised']) {
    expect(ratio(tokens['muted-text'], tokens[surface])).toBeGreaterThanOrEqual(4.5);
  }
  // The border is drawn on the page and on the content surface, so it clears 3:1 on both.
  expect(ratio(tokens.border, tokens.page)).toBeGreaterThanOrEqual(3);
  expect(ratio(tokens.border, tokens.content)).toBeGreaterThanOrEqual(3);
  expect(dark).toMatch(/--background:\s*var\(--surface-page\)/);
  expect(dark).toMatch(/--card:\s*var\(--surface-content\)/);
  expect(dark).toMatch(/--secondary:\s*var\(--surface-raised\)/);
  expect(dark).toMatch(/--border:\s*var\(--surface-border\)/);
  expect(dark).toMatch(/--muted-foreground:\s*var\(--surface-muted-text\)/);
});

test('high-contrast dark redefines the surface tokens with its own values', () => {
  const tokens = surfaces(theme, ':root[data-contrast="high"][data-theme="dark"]');
  expect(tokens.page).toBe('#000000');
  expect(tokens.content).toBe('#000000');
  expect(ratio(tokens.border, tokens.page)).toBeGreaterThanOrEqual(3);
  expect(ratio(tokens['muted-text'], tokens.page)).toBeGreaterThanOrEqual(7);
});

test('the dark clear-search icon and bar colors come from tokens', () => {
  const rule = block(blacksmith, '[data-theme="dark"] .clear-search-btn');
  expect(rule).toMatch(/color:\s*var\(--surface-muted-text\)/);
  const dark = blacksmith.split('\n').filter((l) => /^\s*(color|background|border-color):\s*#(6e7681|8b949e)/i.test(l));
  expect(dark).toEqual([]);
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

test('the expanded VM detail row takes a dark fill that beats the generic table cell', () => {
  const rule = block(theme, '[data-theme="dark"] .main-content .vms-table tr.row-detail > td');
  expect(rule).toMatch(/background:\s*var\(--surface-raised\)/);
  const base = blacksmith.match(/\.vms-table tr\.row-detail > td \{[^}]*\}/);
  expect(base).not.toBeNull();
  // The detail rule counts more classes and attributes than the generic dark cell rule, so it wins.
  const weight = (selector) => (selector.match(/[.[]/g) || []).length;
  expect(weight('[data-theme="dark"] .main-content .vms-table tr.row-detail > td')).toBeGreaterThan(weight('[data-theme="dark"] .main-content table td'));
});
