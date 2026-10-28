const { test, expect } = require('bun:test');
const path = require('path');
const { JSDOM } = require('./certificates/support.js');

const t = JSDOM ? test : test.skip;
const ROOT = path.resolve(__dirname, '..', '..');

// The fixture's tests throw inside a harness window and assert nothing, so
// each one can only fail through the preloaded afterEach in uncaught-guard.js.
const run = () => {
  const out = Bun.spawnSync([process.execPath, 'test', './tests/ui/uncaught-guard.fixture.js'], { cwd: ROOT, env: process.env });
  return { code: out.exitCode, text: out.stdout.toString() + out.stderr.toString() };
};

t('a test fails when its window raises an uncaught error', () => {
  const { code, text } = run();
  expect(code).not.toBe(0);
  for (const name of ['main harness window', 'instance harness window', 'fleet harness window']) {
    expect(text).toContain('(fail) ' + name);
  }
  expect(text).toContain('Uncaught window errors during the test');
  expect(text).toContain('UNCAUGHT boom from a timer');
}, 60000);
