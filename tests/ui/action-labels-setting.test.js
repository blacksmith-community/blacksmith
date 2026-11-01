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

const start = () => {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { w, d } = env;
  require('./dom-stubs.js').install(w, { dismiss: false });
  w.eval(fs.readFileSync(path.join(UI, 'action-help.js'), 'utf8'));
  d.dispatchEvent(new w.Event('DOMContentLoaded'));
  return env;
};

// The text-label setting lives in the Appearance dialog, under the themes.
t('the text labels checkbox sits under the theme choices and defaults to off', () => {
  const { w, d } = start();
  w.blacksmithActionHelp.openThemes();
  const dialog = d.getElementById('theme-choice-dialog');
  const box = dialog.querySelector('[data-action-label-toggle]');
  expect(box).not.toBeNull();
  expect(box.checked).toBe(false);
  expect(box.closest('label').textContent.trim()).toBe('Show text labels beside icon actions');
  const choices = dialog.querySelector('.theme-choices');
  expect(choices.compareDocumentPosition(box) & w.Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(choices.contains(box)).toBe(false);
});

t('the checkbox turns the labels on and off and shows the current state when reopened', () => {
  const { w, d } = start();
  w.blacksmithActionHelp.openThemes();
  const dialog = d.getElementById('theme-choice-dialog');
  const box = dialog.querySelector('[data-action-label-toggle]');
  box.checked = true; box.dispatchEvent(new w.Event('change', { bubbles: true }));
  expect(d.body.hasAttribute('data-action-labels')).toBe(true);
  dialog.close();
  d.body.removeAttribute('data-action-labels');
  w.blacksmithActionHelp.openThemes();
  expect(dialog.querySelector('[data-action-label-toggle]').checked).toBe(false);
  d.body.setAttribute('data-action-labels', '');
  dialog.close();
  w.blacksmithActionHelp.openThemes();
  expect(dialog.querySelector('[data-action-label-toggle]').checked).toBe(true);
  expect(d.querySelectorAll('[data-action-label-toggle]').length).toBe(1);
});

t('the Keyboard shortcuts dialog no longer carries the checkbox', () => {
  const { d } = start();
  const help = d.createElement('dialog'); help.id = 'keyboard-help-dialog';
  help.innerHTML = '<div class="keyboard-help-content"></div>';
  d.body.append(help);
  expect(help.querySelector('[data-action-label-toggle]')).toBeNull();
  expect(fs.readFileSync(path.join(UI, 'action-help.js'), 'utf8')).not.toContain('keyboard-help-content');
});
