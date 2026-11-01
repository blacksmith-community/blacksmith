const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const UI = path.resolve(__dirname, '..', '..', 'ui');

const badge = (w, state, options) => {
  const host = w.document.createElement('div');
  host.innerHTML = w.__T.statusBadge(state, options);
  return host.firstElementChild;
};

t('every operational state renders sentence case text in one of five kinds', () => {
  const { w } = boot();
  try {
    const expected = {
      done: ['Done', 'success', 'success'],
      started: ['Started', 'active', null],
      finished: ['Finished', 'success', 'success'],
      in_progress: ['In progress', 'active', null],
      queued: ['Queued', 'neutral', 'secondary'],
      error: ['Error', 'error', 'danger'],
      cancelled: ['Cancelled', 'warning', 'warning'],
      processing: ['Processing', 'active', null]
    };
    Object.entries(expected).forEach(([state, [text, kind, variant]]) => {
      const el = badge(w, state);
      expect(el.textContent).toBe(text);
      expect(el.classList.contains('badge')).toBe(true);
      expect(el.classList.contains('status-badge')).toBe(true);
      expect(el.dataset.status).toBe(kind);
      expect(el.dataset.state).toBe(state);
      expect(el.getAttribute('data-variant')).toBe(variant);
    });
  } finally { w.close(); }
});

t('an unknown state reads in sentence case as a neutral badge', () => {
  const { w } = boot();
  try {
    const el = badge(w, 'WAITING_ON_DISK');
    expect(el.textContent).toBe('Waiting on disk');
    expect(el.dataset.status).toBe('neutral');
    expect(badge(w, '<img src=x>').querySelector('img')).toBeNull();
  } finally { w.close(); }
});

t('a config type is a neutral outlined category, not a status', () => {
  const { w } = boot();
  try {
    const host = w.document.createElement('div');
    host.innerHTML = ['cloud', 'cpi', 'resurrection', 'runtime'].map((type) => w.__T.configTypeBadge(type)).join('');
    const badges = Array.from(host.children);
    badges.forEach((el) => {
      expect(el.classList.contains('outline')).toBe(true);
      expect(el.classList.contains('status-badge')).toBe(false);
      expect(el.hasAttribute('data-variant')).toBe(false);
    });
    expect(badges.map((el) => el.textContent)).toEqual(['cloud', 'cpi', 'resurrection', 'runtime']);
  } finally { w.close(); }
});

t('the stylesheet no longer draws task states as capitalised pills', () => {
  const css = fs.readFileSync(path.join(UI, 'blacksmith.css'), 'utf8');
  expect(css).not.toMatch(/span\.task-state/);
  expect(css).not.toMatch(/span\.config-type/);
  expect(css).toMatch(/\.status-badge \{[^}]*white-space: nowrap/);
});

t('task event states and registration statuses use the one badge', () => {
  const { w, d } = boot();
  try {
    const host = d.createElement('div');
    host.innerHTML = w.__T.statusBadge('started') + w.__T.statusBadge('registering') + w.__T.statusBadge('created');
    const badges = [...host.querySelectorAll('.status-badge')];
    expect(badges.map(b => b.textContent)).toEqual(['Started', 'Registering', 'Created']);
    expect(badges.map(b => b.dataset.status)).toEqual(['active', 'active', 'neutral']);
    const card = w.__T.CFRegistrationManager.renderRegistrationCard({ id: 'r1', name: 'cf', status: 'failed', created_at: '2026-11-01T03:06:32Z' });
    expect(card).toContain('data-status="error"');
    expect(card).not.toContain('registration-status');
    const src = require('fs').readFileSync(require('path').join(__dirname, '..', '..', 'ui', 'blacksmith.js'), 'utf8');
    expect(src).not.toContain('class="event-state');
  } finally { w.close(); }
});
