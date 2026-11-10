const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

// The broker requires basic auth on every path, and a manifest request
// carries no credentials unless the link asks for them.
test('the manifest link sends credentials', () => {
  const html = fs.readFileSync(path.resolve(__dirname, '../../ui/index.html'), 'utf8');
  const link = html.match(/<link\s[^>]*rel="manifest"[^>]*>/);
  expect(link).not.toBeNull();
  expect(link[0]).toContain('href="/manifest.webmanifest"');
  expect(link[0]).toContain('crossorigin="use-credentials"');
});
