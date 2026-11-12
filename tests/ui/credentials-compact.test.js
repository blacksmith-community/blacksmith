const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const forge = fs.readFileSync(path.resolve(__dirname, '..', '..', 'ui', 'forge-theme.css'), 'utf8');
const ruleFor = (selector) => {
  const at = forge.indexOf(selector);
  if (at < 0) return '';
  return forge.slice(at, forge.indexOf('}', at));
};

t('credential rows lead with the copy icon and keep the controls aligned', () => {
  const { w, d } = boot();
  try {
    const host = d.createElement('div');
    d.body.appendChild(host);
    host.innerHTML = w.__T.formatCredentials({ username: 'admin', host: 'cache.example' });
    const cells = [...host.querySelectorAll('td.cred-value .credential-value-layout')];
    expect(cells.length).toBe(2);
    cells.forEach((c) => expect(c.firstElementChild.tagName).toBe('BUTTON'));
  } finally { w.close(); }
});

t('the instance pane padding does not inflate credential cells', () => {
  const rule = ruleFor('#services .detail-content :is(.credentials-container, .credential-value-layout, .credential-value-text)');
  expect(rule).not.toBe('');
  expect(rule).toMatch(/padding:\s*0\b/);
});

t('credential cells keep four to six pixels of vertical padding and bold keys', () => {
  const cells = ruleFor('.credentials-table :is(th, td)');
  expect(cells).toMatch(/padding:\s*(4|5|6)px\s+\d+px/);
  expect(ruleFor('.credentials-table td.cred-key')).toMatch(/font-weight:\s*700/);
});

t('the connection target is a compact list of bold inline labels', () => {
  const row = ruleFor('.connection-operation .connection-target > div');
  expect(row).toMatch(/display:\s*flex/);
  expect(ruleFor('.connection-operation .connection-target dt')).toMatch(/font-weight:\s*700/);
  expect(ruleFor('.connection-operation .connection-target {')).toMatch(/gap:\s*2px/);
});
