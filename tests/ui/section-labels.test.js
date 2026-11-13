const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const UI = path.resolve(__dirname, '..', '..', 'ui');

// Each group label names its list of links. It is plain text, never a
// control, so the rail's tab order holds only the links themselves.
const checkGroups = (groups, kind) => {
  groups.forEach((list) => {
    const label = list.ownerDocument.getElementById(list.getAttribute('aria-labelledby'));
    expect(label).not.toBeNull();
    expect(label.id.startsWith(`rail-group-${kind}-`)).toBe(true);
    expect(label.classList.contains('rail-group-label')).toBe(true);
    expect(label.hasAttribute('tabindex')).toBe(false);
    expect(label.matches('button, a, [role="tab"]')).toBe(false);
    expect(label.nextElementSibling).toBe(list);
    const links = Array.from(list.querySelectorAll('a.rail-link'));
    expect(links.length).toBeGreaterThan(0);
    links.forEach((link) => expect(link.getAttribute('href').startsWith('#/')).toBe(true));
  });
};

t('rail group labels name their lists without becoming controls', () => {
  // The router formats the instance links, as it does on every page load.
  const { w, d } = boot({ router: true });
  try {
    const broker = Array.from(d.querySelectorAll('#rail-sections-blacksmith ul.rail-list'));
    expect(broker.map((list) => d.getElementById(list.getAttribute('aria-labelledby')).textContent)).toEqual(['Overview', 'Operate', 'Configure']);
    checkGroups(broker, 'blacksmith');
    w.BlacksmithRail.sync({ screen: 'services', instance: 'i1', section: 'details' });
    const instance = Array.from(d.querySelectorAll('#rail-sections-services .rail-instance ul.rail-list'));
    expect(instance.map((list) => d.getElementById(list.getAttribute('aria-labelledby')).textContent)).toEqual(['Overview', 'Operate', 'Connect', 'Inspect']);
    checkGroups(instance, 'instance');
  } finally { w.blacksmithRouter?.destroy?.(); w.close(); }
});

test('headings are styled as muted uppercase labels apart from the links', () => {
  const css = fs.readFileSync(path.join(UI, 'routes.css'), 'utf8');
  const rule = css.match(/\.rail-group-label \{([^}]*)\}/)[1];
  expect(rule).toContain('text-transform: uppercase');
  expect(rule).toContain('letter-spacing');
  expect(rule).toContain('var(--muted-foreground)');
  expect(css).not.toMatch(/\.rail-group-label:(hover|active|focus)/);
  const option = css.match(/\.rail-list \.rail-link \{([^}]*)\}/)[1];
  expect(option).toMatch(/padding: 6px 10px 6px 35px/);
  expect(fs.readFileSync(path.join(UI, 'index.html'), 'utf8')).toContain('/forge-theme.css?v=20261112-oat-377');
  expect(fs.readFileSync(path.join(UI, 'index.html'), 'utf8')).toContain('/blacksmith.css?v=20261112-oat-418');
  expect(fs.readFileSync(path.join(UI, 'index.html'), 'utf8')).toContain('/blacksmith.js?v=20261112-oat-472');
});
