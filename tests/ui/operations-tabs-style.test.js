const { test, expect, afterEach } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const forge = fs.readFileSync(path.resolve(__dirname, '..', '..', 'ui', 'forge-theme.css'), 'utf8');
const rule = (selector) => {
  const at = forge.indexOf(selector + ' {');
  return at < 0 ? '' : forge.slice(at, forge.indexOf('}', at));
};

const envs = [];
afterEach(() => { while (envs.length) envs.pop().w.close(); });

async function screen() {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { w, d } = env;
  w.__routes.push(
    ['/credentials', (u, o, json) => json(200, { host: 'h', password: 'p' })],
    ['/details', (u, o, json) => json(200, { service_name: 'redis' })],
    ['/config', (u, o, json) => json(404, {})]);
  const host = d.createElement('div');
  host.innerHTML = await w.__T.renderServiceTesting('i1');
  d.body.appendChild(host);
  return env;
}

t('the operation strip is a set of tab lists with one selected tab and one tab stop', async () => {
  const { d } = await screen();
  const lists = [...d.querySelectorAll('.operation-tab-group [role="tablist"]')];
  expect(lists.length).toBe(3);
  lists.forEach((list) => expect(d.getElementById(list.getAttribute('aria-labelledby'))).not.toBeNull());
  const tabs = [...d.querySelectorAll('.operation-tab')];
  tabs.forEach((tab) => expect(tab.getAttribute('role')).toBe('tab'));
  expect(tabs.map((tab) => tab.getAttribute('aria-selected'))).toEqual(tabs.map((tab, i) => String(i === 0)));
  expect(tabs.map((tab) => tab.getAttribute('tabindex'))).toEqual(tabs.map((tab, i) => (i === 0 ? '0' : '-1')));
  expect(tabs.some((tab) => tab.hasAttribute('aria-pressed'))).toBe(false);
  const panel = d.querySelector('.operation-content');
  expect(panel.getAttribute('role')).toBe('tabpanel');
  expect(panel.getAttribute('aria-labelledby')).toBe(tabs[0].id);
});

t('the arrow, Home, and End keys move focus across every tab and wrap', async () => {
  const { w, d } = await screen();
  const tabs = [...d.querySelectorAll('.operation-tab')];
  const strip = d.querySelector('.operation-tabs');
  const press = (from, key) => {
    from.focus();
    from.dispatchEvent(new w.KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
    return d.activeElement;
  };
  expect(press(tabs[0], 'ArrowRight')).toBe(tabs[1]);
  expect(press(tabs[3], 'ArrowRight')).toBe(tabs[4]);
  expect(press(tabs[tabs.length - 1], 'ArrowRight')).toBe(tabs[0]);
  expect(press(tabs[0], 'ArrowLeft')).toBe(tabs[tabs.length - 1]);
  expect(press(tabs[2], 'End')).toBe(tabs[tabs.length - 1]);
  expect(press(tabs[2], 'Home')).toBe(tabs[0]);
  expect(strip.querySelectorAll('[aria-selected="true"]').length).toBe(1);
});

t('unselected tabs look like buttons and the selected tab is plainly distinct', () => {
  const base = rule('.service-testing-container .operation-tab');
  expect(base).toMatch(/cursor:\s*pointer/);
  expect(base).toMatch(/border:\s*1px solid/);
  expect(rule('.service-testing-container .operation-tab:hover')).toMatch(/background/);
  const selected = rule('.service-testing-container .operation-tab:is(.active, [aria-selected="true"])');
  expect(selected).toMatch(/background:\s*var\(--primary\)/);
  expect(selected).toMatch(/font-weight:\s*700/);
  expect(rule('.service-testing-container .operation-tabs')).toMatch(/border(-bottom)?:\s*1px solid/);
});

t('the instance pane padding does not spread the groups, panes, or history apart', () => {
  const reset = forge.match(/#services \.detail-content \.service-testing-container :is\(([^)]*)\) \{\s*padding:\s*0;/);
  expect(reset).not.toBeNull();
  ['.testing-operations', '.operation-tabs', '.operation-tab-group', '.operation-content', '.operation-form', '.form-row', '.testing-history', '.history-header', '.history-entries'].forEach((name) => expect(reset[1]).toContain(name));
  expect(rule('.service-testing-container .testing-operations')).toMatch(/margin-bottom:\s*(8|10|12)px/);
  expect(rule('.service-testing-container .testing-history')).toMatch(/padding-top:\s*(8|10|12)px/);
  expect(rule('.service-testing-container .operation-form .form-row')).toMatch(/margin-bottom:\s*(8|10)px/);
});
