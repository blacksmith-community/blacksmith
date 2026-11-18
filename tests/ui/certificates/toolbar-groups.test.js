const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');
const fs = require('fs');
const path = require('path');

const { TEST_LIMIT } = H.main;
const t = JSDOM ? (n, f, l) => test(n, f, l === undefined ? TEST_LIMIT : l) : test.skip;
const { boot, idle, now, day, iso } = H.fleet;

const cert = (id, days, extra = {}) => Object.assign({
  id, key: `/secret/${id}`, variable: `var_${id}`, kind: 'leaf', common_name: `cn-${id}`, issuer: 'CN=Services CA',
  owners: [{ kind: 'instance', instance_id: `inst-${id}`, instance_name: `owner ${id}`, service_name: 'valkey', plan_name: 'small' }],
  affected_instances: 1, effective_not_after: iso(now + days * day), days_left: days, expiry: days < 7 ? 'critical' : 'ok', health: 'ok', attention: false,
  observations: [], findings: [], coverage: { expected_listeners: 1, observed_listeners: 1, failed_listeners: 0, complete: true, stale: false },
  rotation: { allowed: false, reasons: [] },
}, extra);

const open = async () => {
  const env = await boot();
  env.server.certs = [cert('a', 3, { attention: true }), cert('b', 200)];
  await env.w.blacksmithNav.switchTab('certificates');
  await idle(env);
  return env;
};

const css = fs.readFileSync(path.join(H.UI, 'blacksmith.css'), 'utf8');
const rule = (selector) => {
  const out = [];
  css.replace(/([^{}]+)\{([^{}]*)\}/g, (m, sel, body) => { if (sel.split(',').map((x) => x.trim()).includes(selector)) out.push(body); return m; });
  return out.join(';');
};

t('the toggles sit in two visibly labelled groups', async () => {
  const { d, w } = await open();
  const groups = Array.from(d.querySelectorAll('#cert-strips > [role="group"]'));
  expect(groups.length).toBe(2);
  const names = groups.map((g) => d.getElementById(g.getAttribute('aria-labelledby')).textContent);
  expect(names).toEqual(['Needs attention', 'Expiry']);
  expect(Array.from(groups[0].querySelectorAll('.cert-strip-label')).map((l) => l.textContent)).toEqual(['Needs attention', 'Health errors', 'Deployment mismatch']);
  expect(Array.from(groups[1].querySelectorAll('.cert-strip-label')).map((l) => l.textContent)).toEqual(['Expired', 'Expiry critical', 'Expiry warning', 'Expiry OK', 'Unknown']);
  w.close();
});

t('a pressed toggle shows a check icon as well as its background', async () => {
  const { d, w } = await open();
  const buttons = Array.from(d.querySelectorAll('#cert-strips .cert-strip-btn'));
  const pressed = buttons.filter((b) => b.getAttribute('aria-pressed') === 'true');
  expect(pressed.length).toBeGreaterThan(0);
  pressed.forEach((b) => {
    const mark = b.querySelector('.cert-strip-check');
    expect(mark.hidden).toBe(false);
    expect(mark.querySelector('svg')).not.toBeNull();
  });
  buttons.filter((b) => b.getAttribute('aria-pressed') !== 'true').forEach((b) => expect(b.querySelector('.cert-strip-check').hidden).toBe(true));
  w.close();
});

t('the next-expiry fact is plain muted text outside the groups, with no border', async () => {
  const { d, w } = await open();
  const info = d.getElementById('cert-next-expiry');
  expect(info.closest('[role="group"]')).toBeNull();
  expect(info.parentElement.id).toBe('cert-strips');
  const body = rule('.cert-strip-info');
  expect(body).not.toMatch(/border/);
  expect(body).not.toMatch(/background/);
  expect(body).toMatch(/color: var\(--text-secondary\)/);
  w.close();
});
