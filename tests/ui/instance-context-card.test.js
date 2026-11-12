const { test, expect, afterEach } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const css = fs.readFileSync(path.resolve(__dirname, '../../ui/forge-theme.css'), 'utf8');
const rule = (selector) => {
  const at = css.indexOf('\n' + selector + ' {');
  expect(at).toBeGreaterThan(-1);
  return css.slice(at, css.indexOf('}', at));
};

const envs = [];
afterEach(() => { while (envs.length) envs.pop().w.close(); });

const ID = '0a1b2c3d-1111-2222-3333-444455556666';
const summary = (vault) => {
  const env = boot();
  envs.push(env);
  env.w.__T.renderServiceDetail(ID, { service_id: 'valkey', plan: { name: 'small' }, vm_count: 2, vm_status: 'running' }, vault);
  const host = env.d.createElement('div');
  host.innerHTML = env.w.serviceInstanceDetailsContent[ID];
  env.d.body.appendChild(host);
  return host;
};
const VAULT = { instance_id: ID, instance_name: 'Application cache', deployment_name: 'valkey-small-' + ID, organization_name: 'example-org', space_name: 'dev' };

t('the Instance context card is a table of two rows of three labelled fields', () => {
  const card = summary(VAULT).querySelector('article.entity-context');
  expect(card.querySelector('dl')).toBeNull();
  const table = card.querySelector('table.entity-facts-table');
  expect(table).not.toBeNull();
  const rows = [...table.querySelectorAll('tr')];
  expect(rows.length).toBe(2);
  expect(rows.map((r) => [...r.querySelectorAll('th')].map((h) => h.textContent))).toEqual([
    ['Name', 'Service', 'Deployment'], ['Organization', 'Space', 'Instance ID'],
  ]);
  rows.forEach((r) => {
    expect(r.querySelectorAll('td').length).toBe(3);
    r.querySelectorAll('th').forEach((h) => expect(h.getAttribute('scope')).toBe('row'));
  });
});

t('each value cell leads with its copy icon and keeps its label and value', () => {
  const card = summary(VAULT).querySelector('article.entity-context');
  const cells = [...card.querySelectorAll('td')];
  expect(cells.length).toBe(6);
  const labels = ['Name', 'Service', 'Deployment', 'Organization', 'Space', 'Instance ID'];
  const values = ['Application cache', 'valkey', 'valkey-small-' + ID, 'example-org', 'dev', ID];
  cells.forEach((cell, i) => {
    const box = cell.firstElementChild;
    expect(box.classList.contains('copy-first')).toBe(true);
    const button = box.firstElementChild;
    expect(button.tagName).toBe('BUTTON');
    expect(button.dataset.entityCopy).toBe(values[i]);
    expect(button.getAttribute('aria-label')).toBe('Copy ' + labels[i]);
    expect(button.getAttribute('title')).toBe('Copy ' + labels[i]);
    expect(box.lastElementChild.textContent).toBe(values[i]);
  });
});

t('a field with no value keeps an empty slot where the copy icon would be', () => {
  const card = summary(Object.assign({}, VAULT, { space_name: '' })).querySelector('article.entity-context');
  const cell = [...card.querySelectorAll('td')][4];
  const box = cell.firstElementChild;
  expect(box.querySelector('button')).toBeNull();
  expect(box.firstElementChild.classList.contains('copy-slot')).toBe(true);
  expect(box.firstElementChild.getAttribute('aria-hidden')).toBe('true');
  expect(box.lastElementChild.textContent).toBe('Not reported');
});

t('the card fills a rounded box with minimal padding and bold, comfortably padded cells', () => {
  const card = rule('.entity-context.card.entity-context-table');
  expect(card).toMatch(/padding:\s*[0-4]px/);
  expect(card).toMatch(/border-radius:\s*\d+px/);
  expect(card).toMatch(/overflow:\s*hidden/);
  const cells = rule('.entity-context-table .entity-facts-table :is(th, td)');
  const pad = cells.match(/padding:\s*(\d+)px\s+(\d+)px/);
  expect(pad).not.toBeNull();
  expect(Number(pad[1])).toBeGreaterThanOrEqual(6);
  expect(Number(pad[2])).toBeLessThanOrEqual(10);
  expect(rule('.entity-context-table .entity-facts-table th')).toMatch(/font-weight:\s*(700|600|bold)/);
});

t('on a phone the fields stack as labelled pairs without horizontal scroll', () => {
  const phone = css.slice(css.indexOf('@media (max-width: 700px)'));
  expect(phone).toMatch(/\.entity-facts-table tr\s*\{[^}]*display:\s*grid/);
  expect(phone).toMatch(/\.entity-facts-table tr\s*\{[^}]*grid-template-columns:\s*auto minmax\(0, 1fr\)/);
  expect(phone).toMatch(/\.entity-context-table h3\s*\{[^}]*padding/);
});

t('the broker Deployment context uses the same compact table', () => {
  const env = boot();
  envs.push(env);
  const host = env.d.createElement('div');
  host.innerHTML = env.w.__T.renderBlacksmithOverview({ deployment: 'ocfp-lab-blacksmith', env: 'ocfp-lab', instances: {}, plans: {} });
  const card = host.querySelector('article.entity-context');
  expect(card.classList.contains('entity-context-table')).toBe(true);
  const row = card.querySelector('tr');
  expect([...row.querySelectorAll('th')].map((h) => h.textContent)).toEqual(['Deployment', 'Environment']);
  expect(row.querySelector('td:last-child').getAttribute('colspan')).toBe('3');
  card.querySelectorAll('td').forEach((cell) => expect(cell.firstElementChild.firstElementChild.tagName).toBe('BUTTON'));
});
