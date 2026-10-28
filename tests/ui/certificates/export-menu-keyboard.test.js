const { test, expect, afterEach } = require('bun:test');
const fs = require('fs');
const path = require('path');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');
const { install } = require('../dom-stubs.js');

const t = JSDOM ? test : test.skip;
const { boot, sleep } = H.main;
const OAT = fs.readFileSync(path.resolve(__dirname, '..', '..', '..', 'ui', 'vendor', 'oat', 'oat.min.js'), 'utf8');

const envs = [];
afterEach(() => {
  while (envs.length) { const env = envs.pop(); try { expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// Boots the certificate fleet with the dialog and popover stubs and Oat's own
// script, so the menu behaves the same whether it is hand built or an
// ot-dropdown. The harness captures the console before boot, and afterEach
// asserts that nothing reached console.error. The menu counts as open when it is not hidden or its popover shows.
const start = async () => {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { w, d } = env;
  const stubs = install(w);
  // jsdom cannot navigate, so a download link click is cancelled after the page code sees it.
  d.addEventListener('click', (e) => { if (e.target.closest && e.target.closest('a[href]')) e.preventDefault(); });
  w.requestAnimationFrame = (fn) => setTimeout(fn, 0);
  w.eval(OAT);
  const T = w.__T;
  const mk = (i) => ({ id: 'c' + i, key: '/s/a' + i, variable: 'a' + i, kind: 'leaf', days_left: 3, expiry: 'critical', health: 'ok', owners: [{ kind: 'instance', instance_id: 'i' + i, instance_name: 'n' + i }], rotation: { allowed: true, reasons: [] }, findings: [] });
  w.__routes.push(['/b/config/ssh/ui-terminal-status', (u, o, j) => j(200, {})], ['/b/status', (u, o, j) => j(200, { env: 'test', deployment: 'blacksmith', instances: {}, plans: {} })], ['/v2/catalog', (u, o, j) => j(200, { services: [] })], ['/b/bosh/stemcells', (u, o, j) => j(200, [])], ['/b/bosh/releases', (u, o, j) => j(200, [])], ['/b/upgrade/settings', (u, o, j) => j(200, {})], ['/b/upgrade/tasks', (u, o, j) => j(200, [])],
    [/certificates\/inventory/, (u, o, j) => j(200, { certificates: [mk(1), mk(2)], capabilities: {}, settings: { warning_days: 30, critical_days: 7 }, scan: { state: 'idle' }, summary: { certificates: 2, needs_attention: 0 }, filtered: { certificates: 2 } })]);
  w.serviceInstances = {};
  await sleep(50);
  w.plansData = { services: [] };
  d.getElementById('certificates').classList.add('active');
  await T.CertsFleet.init();
  await sleep(200);
  const btn = d.getElementById('cert-export-btn');
  const menu = d.getElementById('cert-export-menu');
  const isOpen = () => (menu.hasAttribute('popover') ? stubs.isOpen(menu) : !menu.hidden);
  const key = (target, k) => target.dispatchEvent(new w.KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
  const csv = d.getElementById('cert-export-csv');
  const json = d.getElementById('cert-export-json');
  const note = d.getElementById('cert-export-note');
  const block = async () => {
    const st = T.CertsFleet.state;
    st.attention = true; st.health = ['error'];
    const service = T.CertsFleet.view.refs.service;
    service.dispatchEvent(new w.Event('change'));
    await sleep(300);
  };
  const unblock = async () => {
    const st = T.CertsFleet.state;
    st.attention = false; st.health = [];
    T.CertsFleet.view.refs.service.dispatchEvent(new w.Event('change'));
    await sleep(300);
  };
  return { env, w, d, stubs, T, btn, menu, csv, json, note, isOpen, key, block, unblock, settle: () => stubs.settle() };
};

t('a click opens the menu, marks the button expanded, and focuses the first item', async () => {
  const x = await start();
  x.btn.click(); await x.settle();
  expect(x.isOpen()).toBe(true);
  expect(x.btn.getAttribute('aria-expanded')).toBe('true');
  expect(x.d.activeElement).toBe(x.csv);
});

t('the arrow keys move between the items and wrap at both ends', async () => {
  const x = await start();
  x.btn.click(); await x.settle();
  x.key(x.csv, 'ArrowDown');
  expect(x.d.activeElement).toBe(x.json);
  x.key(x.json, 'ArrowDown');
  expect(x.d.activeElement).toBe(x.csv);
  x.key(x.csv, 'ArrowUp');
  expect(x.d.activeElement).toBe(x.json);
  x.key(x.json, 'ArrowUp');
  expect(x.d.activeElement).toBe(x.csv);
});

t('Escape closes the menu and returns focus to the button that opened it', async () => {
  const x = await start();
  x.btn.click(); await x.settle();
  x.key(x.csv, 'Escape'); await x.settle();
  expect(x.isOpen()).toBe(false);
  expect(x.btn.getAttribute('aria-expanded')).toBe('false');
  expect(x.d.activeElement).toBe(x.btn);
});

t('Escape in the menu does not reach a listener on the document', async () => {
  const x = await start();
  let seen = false;
  x.d.addEventListener('keydown', () => { seen = true; });
  x.btn.click(); await x.settle();
  x.key(x.csv, 'Escape'); await x.settle();
  expect(seen).toBe(false);
});

t('choosing an item closes the menu and returns focus to the button', async () => {
  const x = await start();
  x.btn.click(); await x.settle();
  x.json.click(); await x.settle();
  expect(x.isOpen()).toBe(false);
  expect(x.btn.getAttribute('aria-expanded')).toBe('false');
  expect(x.d.activeElement).toBe(x.btn);
});

t('a click outside closes the menu', async () => {
  const x = await start();
  x.btn.click(); await x.settle();
  x.d.body.click(); await x.settle();
  expect(x.isOpen()).toBe(false);
  expect(x.btn.getAttribute('aria-expanded')).toBe('false');
});

t('the button reopens the menu after it closed', async () => {
  const x = await start();
  x.btn.click(); await x.settle();
  x.key(x.csv, 'Escape'); await x.settle();
  x.btn.click(); await x.settle();
  expect(x.isOpen()).toBe(true);
  expect(x.d.activeElement).toBe(x.csv);
});

t('items keep their labels, order, and download files', async () => {
  const x = await start();
  const links = [...x.menu.querySelectorAll('a')];
  expect(links.map((a) => a.textContent.trim())).toEqual(['Download CSV', 'Download JSON']);
  expect(links.every((a) => a.hasAttribute('download'))).toBe(true);
  expect(x.csv.getAttribute('href')).toMatch(/^\/b\/certificates\/inventory\?.*format=csv/);
  expect(x.json.getAttribute('href')).toMatch(/^\/b\/certificates\/inventory\?.*format=json/);
});

t('when export is blocked both items are disabled and out of the list Oat navigates', async () => {
  const x = await start();
  await x.block();
  [x.csv, x.json].forEach((a) => {
    expect(a.getAttribute('aria-disabled')).toBe('true');
    expect(a.hasAttribute('href')).toBe(false);
  });
  expect(x.menu.querySelectorAll('[role="menuitem"]').length).toBe(0);
  x.btn.focus();
  x.btn.click(); await x.settle();
  expect(x.isOpen()).toBe(true);
  expect(x.d.activeElement).toBe(x.btn);
  ['ArrowDown', 'ArrowUp', 'Home', 'End'].forEach((k) => { x.key(x.btn, k); x.key(x.csv, k); x.key(x.json, k); });
  expect(x.d.activeElement).toBe(x.btn);
});

t('after export is blocked and then allowed again the menu focuses the first item and the arrow keys work', async () => {
  const x = await start();
  await x.block();
  await x.unblock();
  expect(x.csv.getAttribute('role')).toBe('menuitem');
  expect(x.csv.hasAttribute('href')).toBe(true);
  x.btn.click(); await x.settle();
  expect(x.d.activeElement).toBe(x.csv);
  x.key(x.csv, 'ArrowDown');
  expect(x.d.activeElement).toBe(x.json);
  x.key(x.json, 'ArrowDown');
  expect(x.d.activeElement).toBe(x.csv);
});

// Tab closes the menu and focus moves on in the tab order, as in the menu
// button pattern. Shift+Tab moves to the previous element.
const tabbables = (d, menu, btn) => [...d.querySelectorAll('a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')]
  .filter((el) => !menu.contains(el) && el !== btn && !el.contains(btn) && !el.closest('[hidden]'));
const around = (x) => {
  const list = tabbables(x.d, x.menu, x.btn);
  const at = list.findIndex((el) => x.btn.compareDocumentPosition(el) & x.w.Node.DOCUMENT_POSITION_FOLLOWING);
  return { next: list[at], previous: list[at - 1] };
};
t('Tab on an item closes the menu and moves focus to the next element after the button', async () => {
  const x = await start();
  const { next } = around(x);
  expect(next).toBeTruthy();
  x.btn.click(); await x.settle();
  const tab = new x.w.KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
  x.csv.dispatchEvent(tab);
  expect(tab.defaultPrevented).toBe(true);
  await x.settle();
  expect(x.isOpen()).toBe(false);
  expect(x.btn.getAttribute('aria-expanded')).toBe('false');
  expect(x.d.activeElement).toBe(next);
});

t('Shift+Tab on an item closes the menu and moves focus to the element before the button', async () => {
  const x = await start();
  const { previous } = around(x);
  expect(previous).toBeTruthy();
  x.btn.click(); await x.settle();
  const tab = new x.w.KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true });
  x.json.dispatchEvent(tab);
  expect(tab.defaultPrevented).toBe(true);
  await x.settle();
  expect(x.isOpen()).toBe(false);
  expect(x.d.activeElement).toBe(previous);
});

t('reopening the menu right after Tab does not let the late focus move steal focus from the menu', async () => {
  const x = await start();
  x.btn.click(); await x.settle();
  x.csv.dispatchEvent(new x.w.KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }));
  x.btn.click();
  await x.settle();
  expect(x.isOpen()).toBe(true);
  expect(x.d.activeElement).toBe(x.csv);
});

t('Tab with the menu closed is left alone', async () => {
  const x = await start();
  const tab = new x.w.KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
  x.btn.dispatchEvent(tab);
  expect(tab.defaultPrevented).toBe(false);
});

t('the blocked note is outside the menu items and clicking a disabled item or the note keeps the menu open', async () => {
  const x = await start();
  await x.block();
  x.btn.click(); await x.settle();
  expect(x.note.hidden).toBe(false);
  expect(x.note.textContent).toMatch(/Export is unavailable/);
  expect(x.note.closest('[role="menuitem"]')).toBeNull();
  expect(x.note.closest('a')).toBeNull();
  x.csv.click(); x.note.click(); await x.settle();
  expect(x.isOpen()).toBe(true);
});

t('the note is hidden when export is allowed', async () => {
  const x = await start();
  expect(x.note.hidden).toBe(true);
  expect(x.note.textContent).toBe('');
});

t('the menu is an ot-dropdown popover whose note sits outside the menu role and disabled items carry no menuitem role', async () => {
  const x = await start();
  expect(x.btn.parentElement.tagName.toLowerCase()).toBe('ot-dropdown');
  expect(x.btn.getAttribute('popovertarget')).toBe('cert-export-menu');
  expect(x.menu.hasAttribute('popover')).toBe(true);
  const group = x.menu.querySelector('[role="menu"]');
  expect(group).not.toBeNull();
  expect(group.contains(x.note)).toBe(false);
  expect([...group.querySelectorAll('[role="menuitem"]')]).toEqual([x.csv, x.json]);
  await x.block();
  expect(x.csv.hasAttribute('role')).toBe(false);
  expect(x.json.hasAttribute('role')).toBe(false);
});
