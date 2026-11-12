const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const UI = path.resolve(__dirname, '..', '..', 'ui');
const forge = fs.readFileSync(path.join(UI, 'forge-theme.css'), 'utf8');
const guidance = fs.readFileSync(path.join(UI, 'view-guidance.css'), 'utf8');

const render = () => {
  const { w, d } = boot();
  const host = d.createElement('div');
  d.body.appendChild(host);
  host.innerHTML = w.__T.renderPlanDetail(
    { name: 'valkey', id: 'valkey', description: 'Managed cache' },
    { name: 'small', description: 'Single node cache', blacksmith: { instances: 3, limit: 100 } });
  return { w, host };
};

t('the plan details values lead with their copy icons and the others keep a slot', () => {
  const { w, host } = render();
  try {
    const rows = [...host.querySelectorAll('.plan-details-table tbody tr')];
    expect(rows.map((r) => r.querySelector('th').textContent)).toEqual(['Service', 'Plan', 'Description', 'Current instances', 'Instance limit']);
    rows.forEach((r, i) => {
      const box = r.querySelector('td.info-value').firstElementChild;
      expect(box.classList.contains('copy-first')).toBe(true);
      if (i < 2) {
        expect(box.firstElementChild.tagName).toBe('BUTTON');
        expect(box.firstElementChild.getAttribute('aria-label')).toBe('Copy ' + r.querySelector('th').textContent.toLowerCase());
      } else {
        expect(box.querySelector('button')).toBeNull();
        expect(box.firstElementChild.classList.contains('copy-slot')).toBe(true);
      }
      expect(box.lastElementChild.textContent).not.toBe('');
    });
  } finally { w.close(); }
});

t('the plan details key column is as wide as its keys, in bold', () => {
  const rule = forge.match(/#blacksmith \.plan-details-table \.info-key \{([^}]*)\}/);
  expect(rule).not.toBeNull();
  expect(rule[1]).toMatch(/width:\s*1%/);
  expect(rule[1]).toMatch(/white-space:\s*nowrap/);
  expect(rule[1]).toMatch(/font-weight:\s*(700|600|bold)/);
  expect(guidance).not.toMatch(/#blacksmith \.plan-details-table \.info-key \{ width: 7rem; \}/);
});
