const { test, expect, afterEach } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const envs = [];
afterEach(() => { while (envs.length) envs.pop().w.close(); });

// The page asks the broker for its own deployment name and uses only that
// name. When the broker reports none, the page asks for nothing and shows
// the failure, and it never guesses a deployment called blacksmith.
async function start(instance) {
  const status = (u, o, j) => j(200, { env: 'test', plans: {}, instances: {} });
  const catalog = (u, o, j) => j(200, { services: [] });
  const env = boot({
    noDialogs: true,
    routes: [
      ['/v2/catalog', catalog],
      ['/b/status', status],
      ['/b/instance', (u, o, j) => j(200, instance)],
      ['/b/config/ssh/ui-terminal-status', (u, o, j) => j(200, { enabled: true })],
      ['/b/deployments/', (u, o, j) => j(200, [])],
    ],
  });
  envs.push(env);
  await sleep(150);
  return env;
}

const deploymentCalls = (env) => env.calls.filter((c) => c.includes('/b/deployments/'));

t('the tabs ask for the deployment the broker reports', async () => {
  const env = await start({ deployment: 'lab-bs', id: 'b' });
  expect(env.w.blacksmithDeploymentName).toBe('lab-bs');
  await env.w.loadBlacksmithSection('events');
  await sleep(60);
  expect(deploymentCalls(env).some((c) => c.includes('/b/deployments/lab-bs/events'))).toBe(true);
  expect(deploymentCalls(env).some((c) => c.includes('/b/deployments/blacksmith'))).toBe(false);
});

t('a broker that reports no deployment name is asked for nothing', async () => {
  const env = await start({ deployment: '', id: 'b' });
  expect(env.w.blacksmithDeploymentName).toBe('');
  await env.w.loadBlacksmithSection('events');
  await sleep(60);
  expect(deploymentCalls(env)).toEqual([]);
  expect(env.d.querySelector('#blacksmith [data-load-error]')).not.toBeNull();
});
