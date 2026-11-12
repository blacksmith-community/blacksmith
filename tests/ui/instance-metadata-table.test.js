const { test, expect, afterEach } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const css = fs.readFileSync(path.resolve(__dirname, '../../ui/forge-theme.css'), 'utf8');
const envs = [];
afterEach(() => { while (envs.length) envs.pop().w.close(); });

const ID = '0a1b2c3d-1111-2222-3333-444455556666';
const metadata = (vault) => {
  const env = boot();
  envs.push(env);
  env.w.__T.renderServiceDetail(ID, { service_id: 'valkey', plan: { name: 'small' } }, vault);
  const host = env.d.createElement('div');
  host.innerHTML = env.w.serviceInstanceDetailsContent[ID];
  env.d.body.appendChild(host);
  return host.querySelector('details.entity-technical');
};
const VAULT = { instance_id: ID, instance_name: 'Application cache', plan_id: 'small', created_at: '2026-10-31T23:06:32Z', note: '' };

t('each metadata value cell leads with its copy icon', () => {
  const rows = [...metadata(VAULT).querySelectorAll('tbody tr')];
  expect(rows.length).toBeGreaterThan(3);
  const withValue = rows.filter((r) => r.querySelector('button'));
  expect(withValue.length).toBeGreaterThan(3);
  withValue.forEach((r) => {
    const box = r.querySelector('td.info-value').firstElementChild;
    expect(box.classList.contains('copy-first')).toBe(true);
    expect(box.firstElementChild.tagName).toBe('BUTTON');
    expect(box.firstElementChild.getAttribute('aria-label')).toBe('Copy ' + r.querySelector('th').textContent);
    expect(box.lastElementChild.classList.contains('copy-first-text')).toBe(true);
  });
});

t('a metadata row without a value keeps an empty slot the width of the icon', () => {
  const row = [...metadata(VAULT).querySelectorAll('tbody tr')].find((r) => r.querySelector('th').textContent === 'note');
  const box = row.querySelector('td.info-value').firstElementChild;
  expect(box.querySelector('button')).toBeNull();
  expect(box.firstElementChild.classList.contains('copy-slot')).toBe(true);
  expect(box.lastElementChild.textContent).toBe('Not reported');
});

t('the creation time keeps its ISO title on the value text', () => {
  const row = [...metadata(VAULT).querySelectorAll('tbody tr')].find((r) => r.querySelector('th').textContent === 'created at');
  expect(row.querySelector('.copy-first-text').getAttribute('title')).toBe('2026-10-31T23:06:32.000Z');
});

t('the key column sizes to its content on the instance and broker screens', () => {
  const at = css.indexOf(':is(#blacksmith, #services) .entity-technical .service-info-table th.info-key {');
  expect(at).toBeGreaterThan(-1);
  const body = css.slice(at, css.indexOf('}', at));
  expect(body).toMatch(/width:\s*1%/);
  expect(body).toMatch(/white-space:\s*nowrap/);
  expect(body).toMatch(/font-weight:\s*(700|600|bold)/);
  expect(css).toMatch(/:is\(#blacksmith, #services\) \.entity-technical \.service-info-table \{[^}]*table-layout:\s*auto[^}]*width:\s*100%/);
});

t('the instance table stays in its box on a phone', () => {
  expect(css).not.toMatch(/@media \(max-width: 540px\) \{ #blacksmith \.entity-technical \.service-info-table th\.info-key \{ width: 8rem; \} \}/);
});

t('the metadata table fills the panel to its right edge', () => {
  const at = css.indexOf(':is(#blacksmith, #services) .entity-technical .service-info-table {');
  expect(at).toBeGreaterThan(-1);
  const body = css.slice(at, css.indexOf('}', at));
  expect(body).toMatch(/width:\s*100%/);
  expect(body).toMatch(/max-width:\s*none/);
  expect(body).toMatch(/margin:\s*0/);
  expect(css).toMatch(/\.entity-technical\s*\{[^}]*overflow:\s*hidden/);
});
