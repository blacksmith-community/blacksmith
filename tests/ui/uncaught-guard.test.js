const { test, expect } = require('bun:test');
const fs = require('fs');
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
  for (const name of ['main harness window', 'instance harness window', 'fleet harness window', 'window built by the suite']) {
    expect(text).toContain('(fail) ' + name);
  }
  expect(text).toContain('Uncaught window errors during the test');
  expect(text).toContain('UNCAUGHT boom from a timer');
}, 60000);

// A suite that builds its own window with new JSDOM must hook it with watchUncaught, or the guard
// cannot see its errors. The harness and the helper itself are the only files that may do it differently.
t('every suite that builds its own window hooks it into the guard', () => {
  const exempt = new Set(['uncaught-guard.fixture.js']);
  const files = fs.readdirSync(__dirname).filter((f) => f.endsWith('.js') && !exempt.has(f));
  const bare = files.filter((f) => {
    const src = fs.readFileSync(path.join(__dirname, f), 'utf8');
    return /new JSDOM\(/.test(src) && !src.includes('watchUncaught(');
  });
  expect(bare).toEqual([]);
});
