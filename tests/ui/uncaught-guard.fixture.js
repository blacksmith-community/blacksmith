// Run by uncaught-guard.test.js as its own bun process, never collected as a
// test file. It boots a harness window and lets a timer throw inside it.
const { test } = require('bun:test');
const H = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const { watchUncaught } = require('./watch-uncaught.js');

const t = JSDOM ? test : test.skip;
const { until } = H.main;

const throwLater = async (env) => {
  let ran = false;
  env.w.setTimeout(() => { ran = true; throw new Error('boom from a timer'); }, 0);
  await until(() => ran, 'the timer to run');
};

t('main harness window', async () => { const env = H.main.boot(); await throwLater(env); env.w.close(); });
t('instance harness window', async () => { const env = H.instance.boot(); await throwLater(env); env.w.close(); });
t('fleet harness window', async () => { const env = await H.fleet.boot(); await throwLater(env); env.w.close(); });
t('window built by the suite', async () => { const w = watchUncaught(new JSDOM('<body></body>', { runScripts: 'outside-only' }).window); await throwLater({ w }); w.close(); });
