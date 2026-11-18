const { test, expect, afterEach } = require('bun:test');
const { main: { boot, idle, TEST_LIMIT } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? (n, f, l) => test(n, f, l === undefined ? TEST_LIMIT : l) : test.skip;

const envs = [];
afterEach(() => { while (envs.length) envs.pop().w.close(); });

// The Logs and Debug tabs of the broker's own deployment read the latest
// deployment task's log from the task output route the broker serves.
// No route answers /b/deployments/{name}/tasks/{id}/log, so the page must
// never ask for it.
async function start() {
  const event = { task_id: '77', action: 'update', user: 'admin', timestamp: 100, object_type: 'deployment' };
  const log = [{ time: 1700000000, stage: 'Updating instance', task: 'blacksmith/0', index: '0', state: 'finished', progress: 100 }];
  const env = boot({
    noDialogs: true,
    routes: [
      ['/v2/catalog', (u, o, j) => j(200, { services: [] })],
      ['/b/status', (u, o, j) => j(200, { env: 'test', plans: {}, instances: {} })],
      ['/b/instance', (u, o, j) => j(200, { deployment: 'lab-bs', id: 'b' })],
      ['/b/config/ssh/ui-terminal-status', (u, o, j) => j(200, { enabled: true })],
      ['/b/deployments/lab-bs/events', (u, o, j) => j(200, [event])],
      ['/b/tasks/77/output', (u, o, j) => j(200, log)],
    ],
  });
  envs.push(env);
  await idle(env);
  return env;
}

for (const tab of ['logs', 'debug']) {
  t(`the ${tab} tab reads the task output route`, async () => {
    const env = await start();
    await env.w.loadBlacksmithSection(tab);
    await idle(env);
    expect(env.calls.some((c) => c.includes('/b/tasks/77/output?type=event'))).toBe(true);
    expect(env.calls.some((c) => /\/b\/deployments\/[^/]+\/tasks\//.test(c))).toBe(false);
    const row = env.d.querySelector('#blacksmith .log-stage');
    expect(row && row.textContent).toBe('Updating instance');
  });
}
