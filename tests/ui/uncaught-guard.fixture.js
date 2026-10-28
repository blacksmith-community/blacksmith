// Run by uncaught-guard.test.js as its own bun process, never collected as a
// test file. It boots a harness window and lets a timer throw inside it.
const { test } = require('bun:test');
const H = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');

const t = JSDOM ? test : test.skip;
const { sleep } = H.main;

const throwLater = async (env) => {
  env.w.setTimeout(() => { throw new Error('boom from a timer'); }, 0);
  await sleep(30);
};

t('main harness window', async () => { const env = H.main.boot(); await throwLater(env); env.w.close(); });
t('instance harness window', async () => { const env = H.instance.boot(); await throwLater(env); env.w.close(); });
t('fleet harness window', async () => { const env = await H.fleet.boot(); await throwLater(env); env.w.close(); });
