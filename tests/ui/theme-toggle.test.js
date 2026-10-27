const { test, expect, afterEach } = require('bun:test');
const fs = require('fs');
const path = require('path');
const H = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');

const t = JSDOM ? test : test.skip;
const { boot } = H.main;
const UI = path.resolve(__dirname, '..', '..', 'ui');

const envs = [];
afterEach(() => { envs.splice(0).forEach((e) => { expect(e.alerts).toEqual([]); e.w.close(); }); });

// Boots the page and the dialog helper, then fires DOMContentLoaded the way a
// browser does. The setup hook runs before that event.
const start = (setup) => {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { w, d } = env;
  w.HTMLDialogElement.prototype.showModal = function () { this.setAttribute('open', ''); };
  w.HTMLDialogElement.prototype.close = function () { this.removeAttribute('open'); this.dispatchEvent(new w.Event('close')); };
  if (setup) setup(w);
  w.eval(fs.readFileSync(path.join(UI, 'action-help.js'), 'utf8'));
  d.dispatchEvent(new w.Event('DOMContentLoaded'));
  return env;
};
const isOpen = (d) => !!d.querySelector('#theme-choice-dialog[open]');

t('the theme button opens its dialog when storage is blocked', () => {
  const { w, d } = start((win) => {
    win.Storage.prototype.getItem = function () { throw new win.Error('SecurityError: storage blocked'); };
  });
  d.getElementById('themeToggle').click();
  expect(isOpen(d)).toBe(true);
});

t('the theme button opens its dialog when storage cannot be written', () => {
  const { w, d } = start((win) => {
    win.Storage.prototype.setItem = function () { throw new win.Error('QuotaExceededError'); };
  });
  d.getElementById('themeToggle').click();
  expect(isOpen(d)).toBe(true);
  expect(() => w.blacksmithTheme.setPreference('dark')).not.toThrow();
});

t('the theme button opens its dialog when matchMedia has no listener API', () => {
  const { d } = start((win) => {
    win.matchMedia = () => ({ matches: false });
  });
  d.getElementById('themeToggle').click();
  expect(isOpen(d)).toBe(true);
});

t('an old matchMedia with addListener still subscribes', () => {
  let added = 0;
  start((win) => {
    win.matchMedia = () => ({ matches: false, addListener() { added++; } });
  });
  expect(added).toBe(1);
});

t('the theme button opens its dialog on every click', () => {
  const { d } = start();
  const button = d.getElementById('themeToggle');
  for (let i = 0; i < 3; i++) {
    button.click();
    expect(isOpen(d)).toBe(true);
    d.getElementById('theme-choice-dialog').close();
    expect(isOpen(d)).toBe(false);
  }
});
