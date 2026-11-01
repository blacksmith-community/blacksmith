const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');

const UI = path.resolve(__dirname, '..', '..', 'ui');
const read = (name) => fs.readFileSync(path.join(UI, name), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
const FILES = ['forge-theme.css', 'blacksmith.css', 'console.css', 'routes.css', 'view-guidance.css', 'action-help.css', 'inspection-layout.css'];

const hit = (css, marker) => {
  const rule = css.match(new RegExp(`button:is\\(([^)]*${marker}[^)]*)\\) \\{([^}]*)\\}`));
  expect(rule).not.toBeNull();
  return { classes: rule[1].split(',').map((c) => c.trim()), body: rule[2] };
};

test('icon-only buttons have at least a 24 by 24 hit area', () => {
  const { classes, body } = hit(read('forge-theme.css'), '\\.secret-copy');
  for (const name of ['.copy-btn-inline', '.secret-toggle', '.secret-copy', '.entity-copy', '.ssh-btn', '.delete-resurrection-btn']) {
    expect(classes).toContain(name);
  }
  expect(body).toContain('min-width: 24px');
  expect(body).toContain('min-height: 24px');
});

test('toolbar icon buttons have at least a 32 by 32 hit area', () => {
  const { classes, body } = hit(read('forge-theme.css'), '\\.refresh-btn-logs');
  for (const name of ['.refresh-btn-logs', '.copy-btn-logs', '.copy-deployment-names-btn', '.clear-filters-btn', '.vm-expand']) {
    expect(classes).toContain(name);
  }
  expect(body).toContain('min-width: 32px');
  expect(body).toContain('min-height: 32px');
});

test('checkbox labels are a full-height, pointer-cursor click target', () => {
  const css = read('forge-theme.css');
  const rule = css.match(/:is\(\.checkbox-group, \.filter-row\) label:has\(> input\[type="checkbox"\]\)[^{]*\{([^}]*)\}/);
  expect(rule).not.toBeNull();
  expect(rule[1]).toContain('min-height: 24px');
  expect(rule[1]).toContain('cursor: pointer');
  expect(rule[1]).toContain('display: inline-flex');
  expect(css).toMatch(/label\.checkbox-label\b/);
});

test('muted secondary text is at least 12 pixels', () => {
  const px = (value) => {
    const m = value.trim().match(/^([\d.]+)(px|pt|rem)$/);
    if (!m) return null;
    const n = parseFloat(m[1]);
    return m[2] === 'px' ? n : m[2] === 'pt' ? (n * 4) / 3 : n * 16;
  };
  const small = [];
  for (const file of FILES) {
    for (const m of read(file).matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      const size = m[2].match(/(?<![-\w])font-size:\s*([^;]+)/);
      const muted = /color:\s*var\(--(muted-foreground|text-secondary|text-muted|text-tertiary|faint-foreground)/.test(m[2]);
      if (size && muted && px(size[1]) !== null && px(size[1]) < 12) small.push(`${file} ${m[1].trim().slice(0, 60)}`);
    }
  }
  expect(small).toEqual([]);
});

const luminance = (hex) => {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
    .map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};
const ratio = (a, b) => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
};

test('muted text keeps 4.5 to 1 against every surface in both Anvil themes', () => {
  const css = read('forge-theme.css');
  for (const [, selector] of [['light', /:root \{([^}]*)\}/], ['dark', /\[data-theme="dark"\] \{([^}]*)\}/]]) {
    const raw = Object.fromEntries([...css.match(selector)[1].matchAll(/--([\w-]+):\s*(#[0-9a-f]{6}|var\(--[\w-]+\))/g)].map((m) => [m[1], m[2]]));
    const resolve = (v) => (v.startsWith('var(') ? resolve(raw[v.slice(6, -1)]) : v);
    const tokens = Object.fromEntries(Object.entries(raw).map(([k, v]) => [k, resolve(v)]));
    for (const surface of ['background', 'card', 'secondary', 'muted']) {
      expect(ratio(tokens['muted-foreground'], tokens[surface])).toBeGreaterThanOrEqual(4.5);
    }
    expect(ratio(tokens['faint-foreground'], tokens.faint)).toBeGreaterThanOrEqual(4.5);
    if (tokens['surface-nav']) expect(ratio(tokens['muted-foreground'], tokens['surface-nav'])).toBeGreaterThanOrEqual(4.5);
  }
});
