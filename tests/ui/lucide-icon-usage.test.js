const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

const ui = path.resolve(__dirname, '../../ui');
const vendored = path.join(ui, 'vendor', 'lucide', 'icons');
const read = (f) => fs.readFileSync(path.join(ui, f), 'utf8');
const files = fs.readdirSync(vendored).filter((f) => f.endsWith('.svg')).map((f) => f.slice(0, -4)).sort();

const sourceFiles = () => fs.readdirSync(ui).filter((f) => f.endsWith('.js') && f !== 'icons.js' && f !== 'xterm.min.js');
const usedNames = () => {
  const names = new Set();
  sourceFiles().forEach((f) => {
    read(f).replace(/blacksmithIcon\(\s*'([^']+)'/g, (m, name) => { names.add(name); return m; });
  });
  read('index.html').replace(/class="lucide lucide-([a-z0-9-]+)/g, (m, name) => { names.add(name); return m; });
  return names;
};

test('every name the UI asks for exists in the map', () => {
  const used = usedNames();
  expect(used.size).toBeGreaterThan(10);
  used.forEach((name) => expect(files).toContain(name));
});

test('every vendored icon is used somewhere in the UI', () => {
  const used = usedNames();
  expect(files.filter((name) => !used.has(name))).toEqual([]);
});

test('no hand-pasted Feather markup comes back', () => {
  const pages = sourceFiles().concat('index.html');
  pages.forEach((f) => {
    const src = read(f);
    expect(f + ' has an svg literal' + (/<svg\b(?![^>]*\bclass="lucide )/.test(src) ? ' without a lucide class' : '')).toBe(f + ' has an svg literal');
    expect(src).not.toMatch(/<polyline\b/);
    expect(src).not.toMatch(/<line\s+x1=/);
    expect(src).not.toContain('<rect x="9" y="9" width="13" height="13"');
  });
});

test('icon glyphs and emoji no longer stand in for icons in the chrome', () => {
  sourceFiles().concat('index.html').forEach((f) => {
    const src = read(f);
    expect(f + ' ' + (src.match(/[\u2713\u2717\u2715\u274c\u2705\u26a0\u2139\u24d8\u{1f504}\u{1f534}\u{1f50d}\u2328\u21bb\u25b8\u25be\u00d7]/u) || ['clean'])[0]).toBe(f + ' clean');
    expect(src).not.toMatch(/&times;|&#10003;|&#10007;|&#8635;|&#8987;|&#10006;|&#8214;/);
  });
});

