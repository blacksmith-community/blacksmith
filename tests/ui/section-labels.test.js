const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const UI = path.resolve(__dirname, '..', '..', 'ui');

t('sidebar group labels describe tabs without interrupting the tablist', () => {
  const { w, d } = boot();
  try {
    const host = d.createElement('div');
    host.innerHTML = w.__T.renderBlacksmithTemplate({});
    const groups = Array.from(host.querySelectorAll('.entity-section-group'));
    expect(groups.length).toBe(3);
    groups.forEach((group) => {
      const label = group.querySelector('.entity-group-label');
      expect(label).not.toBeNull();
      const isHeading = /^H[1-6]$/.test(label.tagName) || label.getAttribute('role') === 'heading';
      expect(isHeading).toBe(true);
      expect(label.getAttribute('aria-hidden')).toBe('true');
      expect(label.hasAttribute('tabindex')).toBe(false);
      expect(label.hasAttribute('data-tab')).toBe(false);
      expect(label.matches('button, a, .detail-tab')).toBe(false);
      expect(group.getAttribute('role')).toBe('presentation');
      group.querySelectorAll('.detail-tab').forEach(tab => expect(tab.getAttribute('aria-describedby')).toBe(label.id));
      const options = Array.from(group.querySelectorAll('.detail-tab'));
      expect(options.length).toBeGreaterThan(0);
      options.forEach((o) => expect(o.tagName).toBe('BUTTON'));
    });
  } finally { w.close(); }
});

test('headings are styled as muted uppercase labels apart from the options', () => {
  const css = fs.readFileSync(path.join(UI, 'forge-theme.css'), 'utf8');
  const rule = css.match(/\.entity-group-label \{([^}]*)\}/)[1];
  expect(rule).toContain('text-transform: uppercase');
  expect(rule).toContain('letter-spacing');
  expect(rule).toContain('var(--muted-foreground)');
  expect(rule).toContain('pointer-events: none');
  expect(css).not.toMatch(/\.entity-group-label:(hover|active|focus)/);
  const option = css.match(/\.entity-navigation \.detail-tab \{([^}]*)\}/)[1];
  expect(option).toMatch(/padding: 8px 8px 8px 16px/);
  expect(fs.readFileSync(path.join(UI, 'index.html'), 'utf8')).toContain('/forge-theme.css?v=20261028-oat-305');
  expect(fs.readFileSync(path.join(UI, 'index.html'), 'utf8')).toContain('/blacksmith.css?v=20261028-oat-295');
  expect(fs.readFileSync(path.join(UI, 'index.html'), 'utf8')).toContain('/blacksmith.js?v=20261028-oat-307');
});
