const { test, expect, afterEach } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');

const U0 = '02994cdb-bead-4c52-8f0f-aaaaaaaaaaaa';
const U1 = '12994cdb-bead-4c52-8f0f-bbbbbbbbbbbb';
const file = (text) => ({ 'a.log': '2026-10-25 12:00:00.000 [INFO] ' + text });
const BOTH = { logs: { ['node/' + U0]: file('one'), ['node/' + U1]: file('two') } };
const ONE = { logs: { ['node/' + U0]: file('one') } };
const VMS = [{ job_name: 'node', index: 0, id: U0 }, { job_name: 'node', index: 1, id: U1 }];

const envs = [];
afterEach(() => { while (envs.length) envs.pop().w.close(); });

async function mount(instance, logs, vmsRoute) {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { w, d } = env;
  w.__routes.push([new RegExp('/b/' + instance + '/logs'), (u, o, json) => json(200, logs)], [new RegExp('/b/' + instance + '/vms'), vmsRoute]);
  const host = d.createElement('div');
  d.body.appendChild(host);
  host.innerHTML = await w.__T.fetchServiceDetail(instance, 'instance-logs');
  return host;
}
const labels = (host) => [...host.querySelectorAll('.log-node-select option')].map((o) => o.textContent);

test('the node picker shows the index with the short id once the VMs arrive', async () => {
  const host = await mount('i1', BOTH, (u, o, json) => json(200, VMS));
  await sleep(30);
  expect(labels(host)).toEqual(['node/0 (02994cdb)', 'node/1 (12994cdb)']);
  const options = [...host.querySelectorAll('.log-node-select option')];
  expect(options.map((o) => o.value)).toEqual(['node/' + U0, 'node/' + U1]);
});

test('the logs render at once with the raw node ids while the VMs are pending', async () => {
  let release;
  const host = await mount('i2', BOTH, () => new Promise((resolve) => { release = resolve; }));
  expect(labels(host)).toEqual(['node/' + U0, 'node/' + U1]);
  expect(host.textContent).toContain('one');
  release({ ok: true, status: 200, json: async () => VMS, text: async () => JSON.stringify(VMS) });
  await sleep(30);
  expect(labels(host)[0]).toBe('node/0 (02994cdb)');
});

test('a failed VMs fetch leaves the raw node ids and the logs intact', async () => {
  const host = await mount('i3', BOTH, (u, o, json) => json(500, { error: 'down' }));
  await sleep(30);
  expect(labels(host)).toEqual(['node/' + U0, 'node/' + U1]);
  expect(host.textContent).toContain('one');
});

test('a single node label shows the index and the muted short id', async () => {
  const host = await mount('i4', ONE, (u, o, json) => json(200, VMS));
  await sleep(30);
  const label = host.querySelector('.log-node-label');
  expect(label.textContent).toContain('node/0');
  expect(label.querySelector('.log-node-id').textContent).toBe('02994cdb');
});
