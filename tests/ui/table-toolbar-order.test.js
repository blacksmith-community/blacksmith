const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const UI = path.resolve(__dirname, '..', '..', 'ui');

const screens = (w) => ({
  tasks: { html: w.__T.formatTasks([{ id: 7, state: 'done', description: 'create release', started_at: '2026-10-31T10:00:00Z' }]), noun: 'tasks' },
  vms: { html: w.__T.formatVMs([{ id: 'vm', job_name: 'blacksmith', index: 0, state: 'running', job_state: 'running' }], 'inst-1'), noun: 'VMs' },
  events: { html: w.__T.formatEvents([{ id: '1', time: '2026-10-31T10:00:00Z', user: 'admin', action: 'create', object_type: 'deployment', object_name: 'x' }]), noun: 'events' },
  configs: { html: w.__T.formatConfigs({ configs: [{ id: '1', type: 'cloud', name: 'default', created_at: '2026-10-25' }] }), noun: 'configs' }
});

t('every table toolbar ends with Refresh and then Copy, on the right', () => {
  const { w, d } = boot();
  try {
    Object.entries(screens(w)).forEach(([name, { html, noun }]) => {
      const host = d.createElement('div');
      host.innerHTML = html;
      const group = host.querySelector('.table-toolbar-actions');
      expect(group).not.toBeNull();
      const buttons = Array.from(group.querySelectorAll('button'));
      expect(buttons.map((b) => b.className.split(' ')[0])).toEqual(['refresh-btn-logs', 'copy-btn-logs']);
      expect(buttons[1].getAttribute('aria-label')).toBe(`Copy shown ${noun}`);
      expect(buttons[0].getAttribute('aria-label')).toBe(`Refresh ${noun}`);
      // The group is the last child of its toolbar, after the search and any filters.
      const toolbar = group.parentElement;
      expect(toolbar.lastElementChild).toBe(group);
      expect(toolbar.querySelector('.search-filter-container')).not.toBeNull();
      expect(name).toBeTruthy();
    });
  } finally { w.close(); }
});

t('the Tasks and Configs refresh buttons keep the ids their handlers bind', () => {
  const { w, d } = boot();
  try {
    const all = screens(w);
    const tasks = d.createElement('div');
    tasks.innerHTML = all.tasks.html;
    expect(tasks.querySelector('#refresh-tasks-btn.refresh-btn-logs')).not.toBeNull();
    const configs = d.createElement('div');
    configs.innerHTML = all.configs.html;
    expect(configs.querySelector('#refresh-configs-btn.refresh-btn-logs')).not.toBeNull();
  } finally { w.close(); }
});

t('Refresh and Copy share one box so they line up', () => {
  const css = fs.readFileSync(path.join(UI, 'forge-theme.css'), 'utf8');
  const rule = css.match(/:is\(#blacksmith, #services\) \.table-toolbar-actions :is\(\.refresh-btn-logs, \.copy-btn-logs\) \{([^}]*)\}/);
  expect(rule).not.toBeNull();
  expect(rule[1]).toContain('width: 32px');
  expect(rule[1]).toContain('height: 32px');
  expect(css).toMatch(/\.table-toolbar-actions \{[^}]*margin-left: auto/);
});

t('every other table toolbar uses the same helper, so Refresh comes before Copy', () => {
  const src = fs.readFileSync(path.join(UI, 'blacksmith.js'), 'utf8');
  // The helper writes the only copy and refresh buttons for the toolbar tables.
  const raw = src.split('\n').filter((line) => /<button[^>]*class="(copy|refresh)-btn-logs"/.test(line));
  expect(raw.length).toBe(2);
  expect(raw[0]).toContain('refresh-btn-logs');
  expect(raw[1]).toContain('copy-btn-logs');
  // Every caller is pinned by noun, so a caller that loses its copy button fails.
  const callers = [...src.matchAll(/\$\{tableToolbarActions\(\{[^\n]*?noun: '([^']+)'/g)].map((m) => m[1]).sort();
  expect(callers).toEqual([
    'configs', 'debug logs', 'deployment logs', 'events', 'instances', 'logs', 'logs', 'marketplace services', 'service instances', 'service plans',
    'task events', 'task events', 'task events', 'task events', 'tasks', 'VMs',
  ].sort());
  expect(src.match(/\$\{tableToolbarActions\(/g).length).toBe(callers.length);
});

t('a copy button that copies only the shown rows says so and announces the same words', async () => {
  const { w, d } = boot();
  try {
    const copied = [];
    w.navigator.clipboard = { writeText: (v) => { copied.push(v); return Promise.resolve(); } };
    const host = d.createElement('div');
    host.innerHTML = w.__T.formatTasks([{ id: 7, state: 'done', description: 'create release', started_at: '2026-10-31T10:00:00Z' }]);
    d.body.appendChild(host);
    const button = host.querySelector('.copy-btn-logs');
    expect(button.getAttribute('aria-label')).toBe('Copy shown tasks');
    expect(button.title).toBe('Copy shown tasks');
    await w.copyTableRowsAsText('.tasks-table', { currentTarget: button });
    await new Promise((r) => setTimeout(r, 120));
    expect(copied.length).toBe(1);
    expect(d.getElementById('blacksmith-live-region').textContent).toBe('Copied shown tasks');
  } finally { w.close(); }
});

t('the log viewer labels its server-supplied times as UTC', () => {
  const src = fs.readFileSync(path.join(UI, 'blacksmith.js'), 'utf8');
  expect(src.match(/class="log-col-timestamp">Timestamp \(UTC\)<\/th>/g).length).toBe(2);
  expect(src).not.toMatch(/class="log-col-timestamp">Timestamp<\/th>/);
});

t('the toolbar button style covers the instance toolbars under Services too', () => {
  const css = fs.readFileSync(path.join(UI, 'forge-theme.css'), 'utf8');
  expect(css).not.toMatch(/(^|\n)#blacksmith \.table-toolbar-actions/);
  expect(css).toMatch(/:is\(#blacksmith, #services\) \.table-toolbar-actions \.refresh-btn-logs\.success/);
  expect(css).toMatch(/:is\(#blacksmith, #services\) \.table-toolbar-actions :is\(\.refresh-btn-logs, \.copy-btn-logs\):is\(:hover, :focus-visible\)/);
});
