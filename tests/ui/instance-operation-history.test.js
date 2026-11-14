const { test, expect, afterEach } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const envs = [];
afterEach(() => { while (envs.length) envs.pop().w.close(); });

const ID = '0a1b2c3d-1111-2222-3333-444455556666';
const DETAILS = { service_id: 'valkey', plan: { name: 'small' }, vm_count: 2, vm_status: 'running' };
const EVIL = '<img src=x onerror=alert(1)>';
const NOW = Math.floor(Date.now() / 1000);
const HISTORY = [
  { timestamp: NOW - 300, action: 'provision', description: 'Parsing service parameters', state: 'in_progress' },
  { timestamp: NOW - 200, action: 'provision', description: 'Deploying to BOSH', state: 'in_progress', task_id: 4242 },
  { timestamp: NOW - 100, action: 'provision', description: 'Deploy failed: ' + EVIL, state: 'failed' },
];

const summary = (history) => {
  const env = boot();
  envs.push(env);
  env.w.__T.renderServiceDetail(ID, DETAILS, { instance_id: ID, instance_name: 'cache', history });
  const host = env.d.createElement('div');
  host.innerHTML = env.w.serviceInstanceDetailsContent[ID];
  env.d.body.appendChild(host);
  return { env, host };
};

t('the Summary lists the operation history newest first', () => {
  const { host } = summary(HISTORY);
  const section = host.querySelector('section.operation-history');
  expect(section).not.toBeNull();
  expect(section.querySelector('h3').textContent).toBe('Operation history');
  const heads = [...section.querySelectorAll('thead th')].map((h) => h.textContent);
  expect(heads).toEqual(['Time', 'Operation', 'Step', 'State', 'Failure']);
  const steps = [...section.querySelectorAll('tbody tr')].map((r) => r.children[2].firstChild.textContent.trim());
  expect(steps).toEqual(['Deploy failed', 'Deploying to BOSH', 'Parsing service parameters']);
});

t('failure text is text, never markup, and sits in the Failure column', () => {
  const { host } = summary(HISTORY);
  expect(host.querySelector('section.operation-history img')).toBeNull();
  const failed = host.querySelector('section.operation-history tbody tr');
  expect(failed.children[4].textContent).toBe(EVIL);
  expect(failed.children[3].textContent).toBe('Failed');
});

t('an entry with a BOSH task ID links to the task view', async () => {
  const { env, host } = summary(HISTORY);
  const link = host.querySelector('section.operation-history a[data-history-task-id="4242"]');
  expect(link).not.toBeNull();
  expect(link.textContent).toContain('4242');
  expect(link.dataset.keepKey).toBeTruthy();
  const shown = [];
  env.w.showTaskDetails = (id) => shown.push(id);
  link.dispatchEvent(new env.w.MouseEvent('click', { bubbles: true, cancelable: true }));
  expect(shown).toEqual([4242]);
});

t('a Last change card reports the newest entry', () => {
  const { host } = summary(HISTORY);
  const card = host.querySelector('.entity-metrics [data-last-change]');
  expect(card).not.toBeNull();
  expect(card.textContent).toContain('Last change');
  expect(card.textContent).toContain('Provision');
  expect(card.textContent).toContain('Failed');
  expect(card.querySelector('time')).not.toBeNull();
});

t('with no history the section and the card say so', () => {
  const { host } = summary(undefined);
  expect(host.querySelector('section.operation-history').textContent).toContain('No operations recorded');
  expect(host.querySelector('[data-last-change]').textContent).toContain('Not reported');
});

const poll = (history, body) => {
  const env = boot();
  envs.push(env);
  const timers = [];
  const realSetTimeout = env.w.setTimeout.bind(env.w);
  let nextTimer = 0;
  env.w.setTimeout = (fn, ms, ...rest) => { if (ms !== 5000) return realSetTimeout(fn, ms, ...rest); timers.push({ fn, ms, id: -(++nextTimer) }); return -nextTimer; };
  env.w.clearTimeout = (id) => { const at = timers.findIndex((x) => x.id === id); if (at >= 0) timers.splice(at, 1); };
  env.w.currentInstanceInfo = { id: ID };
  env.w.__routes.push([`/b/${ID}/details`, (u, o, j) => { env.fetches = (env.fetches || 0) + 1; return j(200, { history: body() }); }]);
  env.w.__T.renderServiceDetail(ID, DETAILS, { instance_id: ID, history });
  const host = env.d.createElement('div');
  host.innerHTML = env.w.serviceInstanceDetailsContent[ID];
  const workspace = env.d.createElement('div');
  workspace.className = 'entity-workspace';
  workspace.dataset.section = 'details';
  workspace.appendChild(host);
  env.d.body.appendChild(workspace);
  return { env, host, timers, workspace };
};

t('while the latest entry is in progress the section refreshes every 5 seconds', async () => {
  let current = HISTORY.slice(0, 2);
  const { env, host, timers } = poll(current, () => current);
  env.w.__T.mountOperationHistory(host, ID);
  await sleep(20);
  expect(timers.map((x) => x.ms)).toEqual([5000]);
  current = [...HISTORY.slice(0, 2), { timestamp: NOW - 10, action: 'provision', description: 'Provision finished', state: 'succeeded' }];
  timers.shift().fn();
  await sleep(20);
  expect(host.querySelector('section.operation-history tbody tr').children[2].textContent).toBe('Provision finished');
  expect(timers.length).toBe(0);
});

t('polling stops when the operator leaves the instance', async () => {
  const { env, host, timers } = poll(HISTORY.slice(0, 2), () => HISTORY.slice(0, 2));
  env.w.__T.mountOperationHistory(host, ID);
  await sleep(20);
  expect(timers.length).toBe(1);
  host.remove();
  timers.shift().fn();
  await sleep(20);
  expect(timers.length).toBe(0);
});

t('a finished operation is not polled', async () => {
  const { env, host, timers } = poll(HISTORY, () => HISTORY);
  env.w.__T.mountOperationHistory(host, ID);
  await sleep(20);
  expect(timers.length).toBe(0);
});

const RUNNING = () => HISTORY.slice(0, 2);

t('leaving the Summary for another tab of the same instance stops the poll', async () => {
  const { env, host, timers } = poll(RUNNING(), RUNNING);
  env.w.__T.mountOperationHistory(host, ID);
  await sleep(20);
  expect(timers.length).toBe(1);
  const fetched = env.fetches;
  env.w.__T.markSection(host, 'vms');
  await sleep(0);
  expect(timers.length).toBe(0);
  timers.splice(0).forEach((x) => x.fn());
  await sleep(20);
  expect(env.fetches).toBe(fetched);
  expect(timers.length).toBe(0);
});

t('a tick that fires after the section changed neither fetches nor reschedules', async () => {
  const { env, host, timers, workspace } = poll(RUNNING(), RUNNING);
  env.w.__T.mountOperationHistory(host, ID);
  await sleep(20);
  const fetched = env.fetches;
  const tick = timers.shift();
  workspace.dataset.section = 'events';
  tick.fn();
  await sleep(20);
  expect(env.fetches).toBe(fetched);
  expect(timers.length).toBe(0);
});

t('a route change to another screen stops the poll', async () => {
  const UUID = ID;
  const routes = [['/v2/catalog', (u, o, j) => j(200, { services: [] })],
    ['/b/status', (u, o, j) => j(200, { env: 'lab', deployment: 'blacksmith', instances: { [UUID]: { service_id: 'valkey', plan_id: 'small', plan: { id: 'small', name: 'small' }, instance_name: 'cache' } }, plans: {} })],
    [`/b/${UUID}/details`, (u, o, j) => j(200, { instance_name: 'cache', history: RUNNING() })]];
  const env = boot({ noDialogs: true, routes });
  envs.push(env);
  const { w } = env;
  await sleep(80);
  const real = w.setTimeout.bind(w);
  const timers = [];
  let nextTimer = 0;
  w.setTimeout = (fn, ms, ...rest) => { if (ms !== 5000) return real(fn, ms, ...rest); timers.push({ fn, ms, id: -(++nextTimer) }); return -nextTimer; };
  w.clearTimeout = (id) => { const at = timers.findIndex((x) => x.id === id); if (at >= 0) timers.splice(at, 1); };
  expect(await w.navigateToInstance(UUID)).toBe(true);
  await sleep(40);
  expect(timers.length).toBe(1);
  w.cancelInstanceSection();
  timers.splice(0).forEach((x) => x.fn());
  await sleep(40);
  expect(timers.length).toBe(0);
});

t('an entry that stays in progress is not polled for ever', async () => {
  const { env, host, timers } = poll(RUNNING(), RUNNING);
  env.w.__T.mountOperationHistory(host, ID);
  let ticks = 0;
  for (; ticks < 500; ticks++) {
    await sleep(2);
    if (!timers.length) break;
    timers.shift().fn();
  }
  expect(ticks).toBeLessThan(200);
  expect(host.querySelector('.operation-history .entity-status-note').textContent).toContain('no longer refreshing');
});

t('an entry left in progress long ago is not polled at all', async () => {
  const old = [{ timestamp: NOW - 86400, action: 'provision', description: 'Deploying', state: 'in_progress' }];
  const { env, host, timers } = poll(old, () => old);
  env.w.__T.mountOperationHistory(host, ID);
  await sleep(20);
  expect(timers.length).toBe(0);
  expect(host.querySelector('.operation-history').textContent).toContain('no longer refreshing');
});

t('two mounts leave one pending timer', async () => {
  const { env, host, timers } = poll(RUNNING(), RUNNING);
  env.w.__T.mountOperationHistory(host, ID);
  env.w.__T.mountOperationHistory(host, ID);
  await sleep(30);
  expect(timers.length).toBe(1);
});

t('a refresh already in flight cannot reschedule after a newer mount', async () => {
  const { env, host, timers } = poll(RUNNING(), RUNNING);
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  env.w.__routes.unshift([`/b/${ID}/details`, (u, o, j) => gate.then(() => j(200, { history: RUNNING() }))]);
  env.w.__T.mountOperationHistory(host, ID);
  env.w.__routes.shift();
  env.w.__T.mountOperationHistory(host, ID);
  await sleep(20);
  release();
  await sleep(30);
  expect(timers.length).toBe(1);
});

t('an unchanged history is not redrawn, so focus stays on the task link', async () => {
  const { env, host, timers } = poll(RUNNING(), RUNNING);
  env.w.__T.mountOperationHistory(host, ID);
  await sleep(20);
  const link = host.querySelector('a[data-history-task-id]');
  link.focus();
  const section = host.querySelector('[data-operation-history]');
  timers.shift().fn();
  await sleep(20);
  expect(host.querySelector('[data-operation-history]') === section).toBe(true);
  expect(env.d.activeElement === link).toBe(true);
});

t('a changed history keeps focus on the same task link', async () => {
  let current = RUNNING();
  const { env, host, timers } = poll(current, () => current);
  env.w.__T.mountOperationHistory(host, ID);
  await sleep(20);
  host.querySelector('a[data-history-task-id]').focus();
  const key = env.d.activeElement.dataset.keepKey;
  current = [...current, { timestamp: NOW - 5, action: 'provision', description: 'Waiting for the deployment', state: 'in_progress', task_id: 4242 }];
  timers.shift().fn();
  await sleep(20);
  const link = host.querySelector('a[data-history-task-id]');
  expect(link.dataset.keepKey).toBe(key);
  expect(env.d.activeElement === link).toBe(true);
});

t('each task links once, with a key built from the instance and the task', () => {
  const { host } = summary([
    { timestamp: NOW - 30, action: 'provision', description: 'Waiting', state: 'in_progress', task_id: 7 },
    { timestamp: NOW - 20, action: 'provision', description: 'Deploying', state: 'in_progress', task_id: 7 },
    { timestamp: NOW - 10, action: 'provision', description: 'Checking', state: 'in_progress', task_id: 8 },
  ]);
  const links = [...host.querySelectorAll('a[data-history-task-id]')];
  expect(links.map((a) => a.dataset.keepKey)).toEqual([`history-task:${ID}:8`, `history-task:${ID}:7`]);
  expect(host.textContent.match(/BOSH task 7/g).length).toBe(2);
});

t('a retained shell restarts the poll', async () => {
  const UUID = ID;
  const routes = [['/v2/catalog', (u, o, j) => j(200, { services: [] })],
    ['/b/status', (u, o, j) => j(200, { env: 'lab', deployment: 'blacksmith', instances: {
      [UUID]: { service_id: 'valkey', plan_id: 'small', plan: { id: 'small', name: 'small' }, instance_name: 'cache' },
      other: { service_id: 'valkey', plan_id: 'small', plan: { id: 'small', name: 'small' }, instance_name: 'other' } }, plans: {} })],
    [`/b/${UUID}/details`, (u, o, j) => j(200, { instance_name: 'cache', history: RUNNING() })],
    ['/b/other/details', (u, o, j) => j(200, { instance_name: 'other', history: [] })]];
  const env = boot({ noDialogs: true, routes });
  envs.push(env);
  const { w } = env;
  await sleep(80);
  const real = w.setTimeout.bind(w);
  const timers = [];
  let nextTimer = 0;
  w.setTimeout = (fn, ms, ...rest) => { if (ms !== 5000) return real(fn, ms, ...rest); timers.push({ fn, ms, id: -(++nextTimer) }); return -nextTimer; };
  w.clearTimeout = (id) => { const at = timers.findIndex((x) => x.id === id); if (at >= 0) timers.splice(at, 1); };
  await w.navigateToInstance(UUID);
  await sleep(40);
  await w.navigateToInstance('other');
  w.cancelInstanceSection();
  timers.splice(0).forEach((x) => x.fn());
  await sleep(40);
  expect(timers.length).toBe(0);
  expect(await w.navigateToInstance(UUID)).toBe(true);
  await sleep(40);
  expect(timers.length).toBe(1);
});

t('returning to the Summary through a retained view restarts the poll', async () => {
  const routes = [['/v2/catalog', (u, o, j) => j(200, { services: [] })],
    ['/b/status', (u, o, j) => j(200, { env: 'lab', deployment: 'blacksmith', instances: { [ID]: { service_id: 'valkey', plan_id: 'small', plan: { id: 'small', name: 'small' }, instance_name: 'cache' } }, plans: {} })],
    [`/b/${ID}/details`, (u, o, j) => j(200, { instance_name: 'cache', history: RUNNING() })],
    [`/b/${ID}/vms`, (u, o, j) => j(200, [])]];
  const env = boot({ noDialogs: true, routes });
  envs.push(env);
  const { w } = env;
  await sleep(80);
  const real = w.setTimeout.bind(w);
  const timers = [];
  let nextTimer = 0;
  w.setTimeout = (fn, ms, ...rest) => { if (ms !== 5000) return real(fn, ms, ...rest); timers.push({ fn, ms, id: -(++nextTimer) }); return -nextTimer; };
  w.clearTimeout = (id) => { const at = timers.findIndex((x) => x.id === id); if (at >= 0) timers.splice(at, 1); };
  await w.navigateToInstance(ID);
  await w.loadInstanceSection(ID, 'details');
  await sleep(40);
  expect(timers.length).toBe(1);
  await w.loadInstanceSection(ID, 'vms');
  await sleep(40);
  expect(timers.length).toBe(0);
  // The Summary comes back from the retained view, not from a fresh render.
  await w.loadInstanceSection(ID, 'details');
  await sleep(40);
  expect(timers.length).toBe(1);
});
