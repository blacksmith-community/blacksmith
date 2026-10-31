const { test, expect, afterEach } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const ui = path.resolve(__dirname, '../../ui');
const vendored = path.join(ui, 'vendor', 'lucide', 'icons');
const read = (f) => fs.readFileSync(path.join(ui, f), 'utf8');
const files = fs.readdirSync(vendored).filter((f) => f.endsWith('.svg')).map((f) => f.slice(0, -4)).sort();
const { blacksmithIcon, sources } = require(path.join(ui, 'icons.js'));

const envs = [];
afterEach(() => { envs.splice(0).forEach((env) => { expect(env.alerts).toEqual([]); env.w.close(); }); });

test('every map entry matches its vendored file byte for byte', () => {
  expect(Object.keys(sources).sort()).toEqual(files);
  files.forEach((name) => expect(sources[name]).toBe(fs.readFileSync(path.join(vendored, name + '.svg'), 'utf8')));
});

test('the vendored set carries the license and a README naming its source', () => {
  expect(fs.readFileSync(path.join(ui, 'vendor', 'lucide', 'LICENSE'), 'utf8')).toContain('ISC License');
  const readme = fs.readFileSync(path.join(ui, 'vendor', 'lucide', 'README.md'), 'utf8');
  ['lucide-static', '1.65.0', 'https://lucide.dev', 'ISC'].forEach((text) => expect(readme).toContain(text));
});

test('blacksmithIcon hides an unlabeled icon and names a labeled one', () => {
  const hidden = blacksmithIcon('copy', { size: 14 });
  expect(hidden).toContain('aria-hidden="true"');
  expect(hidden).toContain('width="14" height="14"');
  expect(hidden).toContain('stroke="currentColor"');
  expect(hidden).not.toContain('aria-label');
  const named = blacksmithIcon('refresh-cw', { label: 'Refresh "now"', className: 'spinning' });
  expect(named).toContain('role="img" aria-label="Refresh &quot;now&quot;"');
  expect(named).not.toContain('aria-hidden');
  expect(named).toContain('class="lucide lucide-refresh-cw spinning"');
  expect(named).toContain('width="16" height="16"');
  expect(() => blacksmithIcon('not-a-real-icon')).toThrow(/Unknown icon/);
});

test('index.html loads icons.js with a cache key before blacksmith.js', () => {
  const html = read('index.html');
  const icons = html.indexOf('src="/icons.js?v=');
  expect(icons).toBeGreaterThan(-1);
  expect(icons).toBeLessThan(html.indexOf('src="/blacksmith.js?v='));
});

t('the page script reads the helper the harness loads first', () => {
  const env = boot({ noDialogs: true });
  envs.push(env);
  expect(env.errors).toEqual([]);
  expect(typeof env.w.blacksmithIcon).toBe('function');
});
