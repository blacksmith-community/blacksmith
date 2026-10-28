const { test, expect, afterEach } = require('bun:test');
const { JSDOM } = require('./certificates/support.js');
const { install } = require('./dom-stubs.js');
const { watchUncaught } = require('./watch-uncaught.js');

const t = JSDOM ? test : test.skip;
const wins = [];
afterEach(() => { wins.splice(0).forEach((w) => w.close()); });

const make = (html, options) => {
  const w = watchUncaught(new JSDOM('<!doctype html><body>' + html + '</body>', { url: 'http://localhost/', pretendToBeVisual: true }).window);
  wins.push(w);
  const stubs = install(w, options);
  return { w, d: w.document, stubs, key: (target, k) => target.dispatchEvent(new w.KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true })) };
};
const record = (el, types) => { const seen = []; types.forEach((type) => el.addEventListener(type, (e) => seen.push([type, e.oldState, e.newState, e.cancelable]))); return seen; };

t('showModal opens a dialog as a modal and close clears it and queues one close event', async () => {
  const { d, stubs } = make('<dialog id="x"></dialog>');
  const dialog = d.getElementById('x');
  const seen = record(dialog, ['close']);
  dialog.showModal();
  expect(dialog.hasAttribute('open')).toBe(true);
  expect(stubs.isModal(dialog)).toBe(true);
  dialog.close('done');
  expect(dialog.hasAttribute('open')).toBe(false);
  expect(stubs.isModal(dialog)).toBe(false);
  expect(dialog.returnValue).toBe('done');
  expect(seen).toEqual([]);
  await stubs.settle();
  expect(seen.map((s) => s[0])).toEqual(['close']);
  dialog.close();
  await stubs.settle();
  expect(seen.length).toBe(1);
});

t('showModal on an open dialog throws InvalidStateError and show opens without a modal', () => {
  const { d, stubs } = make('<dialog id="x"></dialog><dialog id="y"></dialog>');
  const x = d.getElementById('x');
  x.showModal();
  expect(() => x.showModal()).toThrow(/already open/);
  const y = d.getElementById('y');
  y.show();
  expect(y.hasAttribute('open')).toBe(true);
  expect(stubs.isModal(y)).toBe(false);
});

t('Escape fires a cancelable cancel on a modal and closes it unless cancel is prevented', async () => {
  const { d, stubs, key } = make('<dialog id="x"></dialog>');
  const dialog = d.getElementById('x');
  const seen = record(dialog, ['cancel', 'close']);
  dialog.showModal();
  dialog.addEventListener('cancel', (e) => e.preventDefault(), { once: true });
  key(d.body, 'Escape');
  expect(dialog.hasAttribute('open')).toBe(true);
  key(d.body, 'Escape');
  await stubs.settle();
  expect(dialog.hasAttribute('open')).toBe(false);
  expect(seen.map((s) => s[0])).toEqual(['cancel', 'cancel', 'close']);
  expect(seen[0][3]).toBe(true);
});

t('showPopover fires a cancelable beforetoggle at once and a toggle event later', async () => {
  const { d, stubs } = make('<div id="p" popover></div>');
  const p = d.getElementById('p');
  const seen = record(p, ['beforetoggle', 'toggle']);
  p.showPopover();
  expect(stubs.isOpen(p)).toBe(true);
  expect(seen).toEqual([['beforetoggle', 'closed', 'open', true]]);
  await stubs.settle();
  expect(seen[1]).toEqual(['toggle', 'closed', 'open', false]);
  p.hidePopover();
  expect(stubs.isOpen(p)).toBe(false);
  expect(seen[2]).toEqual(['beforetoggle', 'open', 'closed', false]);
  await stubs.settle();
  expect(seen[3]).toEqual(['toggle', 'open', 'closed', false]);
});

t('a prevented beforetoggle keeps the popover closed, and show and hide repeat as no-ops', async () => {
  const { d, stubs } = make('<div id="p" popover></div>');
  const p = d.getElementById('p');
  const seen = record(p, ['toggle']);
  p.addEventListener('beforetoggle', (e) => e.preventDefault(), { once: true });
  p.showPopover();
  expect(stubs.isOpen(p)).toBe(false);
  p.showPopover();
  p.showPopover();
  p.hidePopover();
  p.hidePopover();
  await stubs.settle();
  expect(seen.map((s) => s[2])).toEqual(['closed']);
});

t('a show then hide before the task runs fires one toggle that keeps the first oldState', async () => {
  const { d, stubs } = make('<div id="p" popover></div>');
  const p = d.getElementById('p');
  const seen = record(p, ['toggle']);
  p.showPopover();
  p.hidePopover();
  await stubs.settle();
  expect(seen).toEqual([['toggle', 'closed', 'closed', false]]);
});

t('a popover method on an element without the popover attribute throws NotSupportedError', () => {
  const { d } = make('<div id="p"></div>');
  expect(() => d.getElementById('p').showPopover()).toThrow(/popover attribute/);
});

t('togglePopover flips the state, takes force, and returns whether it is open', () => {
  const { d, stubs } = make('<div id="p" popover></div>');
  const p = d.getElementById('p');
  expect(p.togglePopover()).toBe(true);
  expect(p.togglePopover()).toBe(false);
  expect(p.togglePopover(true)).toBe(true);
  expect(p.togglePopover(true)).toBe(true);
  expect(stubs.isOpen(p)).toBe(true);
});

t('showing an auto popover closes the others, but a manual popover stays and an ancestor stays', () => {
  const { d, stubs } = make('<div id="a" popover><div id="b" popover></div></div><div id="c" popover></div><div id="m" popover="manual"></div>');
  const [a, b, c, m] = ['a', 'b', 'c', 'm'].map((id) => d.getElementById(id));
  m.showPopover();
  a.showPopover();
  b.showPopover();
  expect([stubs.isOpen(a), stubs.isOpen(b)]).toEqual([true, true]);
  c.showPopover();
  expect([stubs.isOpen(a), stubs.isOpen(b), stubs.isOpen(c), stubs.isOpen(m)]).toEqual([false, false, true, true]);
});

t('a popovertarget button toggles, shows, or hides its popover on click', () => {
  const { d, stubs } = make('<button id="t" popovertarget="p"></button><button id="s" popovertarget="p" popovertargetaction="show"></button><button id="h" popovertarget="p" popovertargetaction="hide"></button><div id="p" popover></div>');
  const p = d.getElementById('p');
  d.getElementById('t').click();
  expect(stubs.isOpen(p)).toBe(true);
  d.getElementById('t').click();
  expect(stubs.isOpen(p)).toBe(false);
  d.getElementById('s').click();
  d.getElementById('s').click();
  expect(stubs.isOpen(p)).toBe(true);
  d.getElementById('h').click();
  expect(stubs.isOpen(p)).toBe(false);
});

t('Escape and a click outside close an auto popover, and a click inside or on its invoker does not dismiss it', () => {
  const { d, stubs, key } = make('<button id="t" popovertarget="p"></button><div id="p" popover><button id="in"></button></div><div id="out"></div>');
  const p = d.getElementById('p');
  d.getElementById('t').click();
  d.getElementById('in').click();
  expect(stubs.isOpen(p)).toBe(true);
  d.getElementById('out').click();
  expect(stubs.isOpen(p)).toBe(false);
  p.showPopover();
  key(d.body, 'Escape');
  expect(stubs.isOpen(p)).toBe(false);
});

t('manual popovers ignore Escape and outside clicks', () => {
  const { d, stubs, key } = make('<div id="p" popover="manual"></div><div id="out"></div>');
  const p = d.getElementById('p');
  p.showPopover();
  key(d.body, 'Escape');
  d.getElementById('out').click();
  expect(stubs.isOpen(p)).toBe(true);
});

t('dismiss false turns off Escape and outside click handling', () => {
  const { d, stubs, key } = make('<div id="p" popover></div><div id="out"></div>', { dismiss: false });
  const p = d.getElementById('p');
  p.showPopover();
  key(d.body, 'Escape');
  d.getElementById('out').click();
  expect(stubs.isOpen(p)).toBe(true);
});

t('installing twice returns the first stubs and a later dismiss option replaces the earlier one', () => {
  const { w, d, stubs, key } = make('<div id="p" popover></div>');
  const again = install(w, { dismiss: false });
  expect(again).toBe(stubs);
  const p = d.getElementById('p');
  p.showPopover();
  key(d.body, 'Escape');
  expect(stubs.isOpen(p)).toBe(true);
  install(w, { dismiss: true });
  key(d.body, 'Escape');
  expect(stubs.isOpen(p)).toBe(false);
  expect(install(w)).toBe(stubs);
  p.showPopover();
  key(d.body, 'Escape');
  expect(stubs.isOpen(p)).toBe(false);
});

t('the popover property reflects the attribute', () => {
  const { d } = make('<div id="a" popover></div><div id="b" popover="manual"></div><div id="c"></div>');
  expect(d.getElementById('a').popover).toBe('auto');
  expect(d.getElementById('b').popover).toBe('manual');
  expect(d.getElementById('c').popover).toBeNull();
  d.getElementById('c').popover = 'manual';
  expect(d.getElementById('c').getAttribute('popover')).toBe('manual');
});
