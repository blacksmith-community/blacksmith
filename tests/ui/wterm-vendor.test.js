const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

// The terminal library is vendored as one classic-script bundle, its
// stylesheet, and the WASM core served as a separate file. The README pins
// every file by checksum, and the bundle must not carry a second, embedded
// copy of the core.
const dir = path.resolve(__dirname, '../../ui/vendor/wterm/0.5.10');
const file = (name) => path.join(dir, name);
const sha256 = (name) => crypto.createHash('sha256').update(fs.readFileSync(file(name))).digest('hex');

test('the vendored wterm directory holds the bundle, stylesheet, core, license, and README', () => {
  ['wterm.min.js', 'terminal.css', 'wterm.wasm', 'LICENSE', 'README.md'].forEach((name) => {
    expect(fs.existsSync(file(name))).toBe(true);
  });
});

test('the README pins each vendored file by its SHA-256', () => {
  const readme = fs.readFileSync(file('README.md'), 'utf8');
  ['wterm.min.js', 'terminal.css', 'wterm.wasm'].forEach((name) => {
    expect(readme).toContain(sha256(name));
  });
  expect(readme).toContain('https://registry.npmjs.org/@wterm/dom/-/dom-0.5.10.tgz');
  expect(readme).toContain('https://registry.npmjs.org/@wterm/core/-/core-0.5.10.tgz');
  expect(readme).toContain('scripts/vendor-wterm.sh');
});

test('the core is the published 0.5.10 WebAssembly binary', () => {
  const wasm = fs.readFileSync(file('wterm.wasm'));
  expect([...wasm.subarray(0, 4)]).toEqual([0x00, 0x61, 0x73, 0x6d]);
  expect(sha256('wterm.wasm')).toBe('df60f205a71419868802b0f42ec9306e8ed5dbaa1baf17b2765ab16405b76ddc');
});

test('the bundle exposes one global and leaves the embedded WASM out', () => {
  const bundle = fs.readFileSync(file('wterm.min.js'), 'utf8');
  expect(bundle).toContain('WTermLib');
  expect(bundle).not.toContain('AGFzbQ');
  expect(bundle).not.toContain('wasm-inline');
  expect(bundle).not.toMatch(/^\s*(import|export)\s/m);
  expect(bundle.length).toBeLessThan(200 * 1024);
});

test('the license is the Apache 2.0 text', () => {
  expect(fs.readFileSync(file('LICENSE'), 'utf8')).toContain('Apache License');
});
