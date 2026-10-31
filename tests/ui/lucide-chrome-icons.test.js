const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

const ui = path.resolve(__dirname, '../../ui');
const read = (f) => fs.readFileSync(path.join(ui, f), 'utf8');
const vendoredText = (name) => fs.readFileSync(path.join(ui, 'vendor', 'lucide', 'icons', name + '.svg'), 'utf8');
const shapes = (svg) => svg.replace(/^[\s\S]*?<svg[^>]*>/, '').replace(/<\/svg>[\s\S]*$/, '').replace(/\s*\/>/g, '/>').replace(/>\s+</g, '><').trim();
const attr = (svg, name) => (svg.match(new RegExp('\\s' + name + '="([^"]*)"')) || [])[1];

test('every static svg in index.html carries the shapes and style of its vendored file', () => {
  const found = read('index.html').match(/<svg\b[\s\S]*?<\/svg>/g) || [];
  expect(found.length).toBeGreaterThan(10);
  found.forEach((svg) => {
    const name = (svg.match(/class="lucide lucide-([a-z0-9-]+)/) || [])[1];
    expect(name).toBeTruthy();
    const source = vendoredText(name);
    expect(name + ' ' + shapes(svg)).toBe(name + ' ' + shapes(source));
    ['viewBox', 'fill', 'stroke', 'stroke-width', 'stroke-linecap', 'stroke-linejoin'].forEach((a) => expect(name + ' ' + a + '=' + attr(svg, a)).toBe(name + ' ' + a + '=' + attr(source, a)));
  });
});

test('sort arrows are drawn with Lucide icons whose data URIs match the vendored files', () => {
  const css = read('blacksmith.css');
  const marked = [...css.matchAll(/\/\* lucide-icon: ([a-z0-9-]+) \*\/\s*--[a-z0-9-]+:\s*url\("data:image\/svg\+xml,([^"]+)"\)/g)];
  expect(marked.map((m) => m[1]).sort()).toEqual(['arrow-down', 'arrow-up', 'arrow-up-down']);
  marked.forEach((m) => expect(decodeURIComponent(m[2])).toBe(vendoredText(m[1])));
  expect(css).not.toMatch(/content:\s*['"][↕↑↓⇅▲▼]['"]/);
  expect(css).toMatch(/th\.sortable::after[^}]*mask-image:\s*var\(--icon-arrow-up-down\)/);
  expect(css).toMatch(/th\.sort-asc::after[^}]*mask-image:\s*var\(--icon-arrow-up\)/);
  expect(css).toMatch(/th\.sort-desc::after[^}]*mask-image:\s*var\(--icon-arrow-down\)/);
  expect(css).toMatch(/\.sort-icon\.unsorted::after[^}]*mask-image:\s*var\(--icon-arrow-up-down\)/);
  expect(css).toMatch(/\.sort-icon\.asc::after[^}]*mask-image:\s*var\(--icon-arrow-up\)/);
  expect(css).toMatch(/\.sort-icon\.desc::after[^}]*mask-image:\s*var\(--icon-arrow-down\)/);
});

test('the registration step marks are Lucide check and x in the markup, not CSS glyphs', () => {
  const css = read('blacksmith.css');
  expect(css).not.toMatch(/\.step-icon::after/);
  expect(css).not.toMatch(/content:\s*["'][✓✗]["']/);
  expect(css).toMatch(/\.progress-step\.success \.step-icon \.lucide-check/);
  expect(css).toMatch(/\.progress-step\.error \.step-icon \.lucide-x/);
  const pages = { 'index.html': read('index.html'), 'blacksmith.js': read('blacksmith.js') };
  Object.entries(pages).forEach(([file, src]) => {
    const icons = src.match(/<div class="step-icon">[\s\S]*?<\/div>/g) || [];
    expect(file + ' ' + icons.length).toBe(file + ' 5');
    icons.forEach((markup) => {
      if (file === 'index.html') {
        expect(markup).toMatch(/lucide-check/);
        expect(markup).toMatch(/lucide-x/);
        expect(markup).toContain('aria-hidden="true"');
      } else {
        expect(markup).toContain("blacksmithIcon('check'");
        expect(markup).toContain("blacksmithIcon('x'");
      }
    });
  });
  expect(pages['blacksmith.js']).not.toContain('<div class="step-icon"></div>');
});

test('Back to Jobs shows a Lucide arrow-left beside its text', () => {
  const src = read('blacksmith.js');
  expect(src).not.toMatch(/&#8592;|\\u2190|←/);
  expect(src).toMatch(/class="back-to-list"[^>]*>\$\{blacksmithIcon\('arrow-left'[^}]*\}\)\}\s*Back to Jobs/);
  expect(src).toMatch(/const back = rotateEl\('button', 'back-to-list'\);[\s\S]{0,200}blacksmithIcon\('arrow-left'/);
});
