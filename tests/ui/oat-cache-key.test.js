const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

const UI = path.resolve(__dirname, '..', '..', 'ui');

// The broker caches a keyed asset for a year, so the vendored Oat files
// carry a key that names the Oat release. An Oat upgrade changes the
// release in VERSION.md and must change the key with it, or browsers keep
// the old copy. The asset-keys.json pin catches a content change as well.
test('the Oat files are loaded with a key naming the vendored release', () => {
  const html = fs.readFileSync(path.join(UI, 'index.html'), 'utf8');
  const head = html.slice(0, html.indexOf('</head>'));
  const version = fs.readFileSync(path.join(UI, 'vendor', 'oat', 'VERSION.md'), 'utf8').match(/^# Oat UI (\S+)/m)[1];
  for (const file of ['oat.min.css', 'oat.min.js']) {
    const refs = head.match(new RegExp('(?:href|src)="/vendor/oat/' + file.replace(/\./g, '\\.') + '[^"]*"', 'g')) || [];
    expect(refs.length).toBe(1);
    const key = refs[0].match(/\?v=([^"]+)"/);
    expect(key ? 'keyed' : file + ' has no ?v= key').toBe('keyed');
    expect(key[1].endsWith('-oat-' + version)).toBe(true);
  }
});
