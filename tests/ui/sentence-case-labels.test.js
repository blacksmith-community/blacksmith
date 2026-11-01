const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const UI = path.resolve(__dirname, '..', '..', 'ui');
const manifest = {
  text: 'name: test',
  parsed: {
    name: 'test',
    instance_groups: [{ name: 'web', jobs: [{ name: 'j', release: 'r' }] }],
    releases: [{ name: 'release', version: '1', url: 'https://example.test/r.tgz', sha1: 'abc' }],
    stemcells: [{ alias: 'default', os: 'ubuntu-noble', version: '1' }],
    variables: [{ name: 'v', type: 'password', options: { length: 8 } }]
  }
};

t('manifest tables and tabs use sentence case', () => {
  const { w, d } = boot();
  try {
    d.body.innerHTML = w.__T.formatManifestDetails(manifest, 'instance');
    const tabs = Array.from(d.querySelectorAll('[role=tab]')).map((tab) => tab.textContent.trim());
    expect(tabs).toContain('Instance groups');
    expect(tabs).not.toContain('Instance Groups');
    const headers = Array.from(d.querySelectorAll('.manifest-table th')).map((th) => th.textContent.trim());
    ['Property', 'Value', 'Name', 'Version', 'URL', 'SHA1', 'Type', 'Options'].forEach((word) => expect(headers).toContain(word));
    expect(headers.filter((text) => /^[A-Z][a-z]+ [A-Z][a-z]+$/.test(text))).toEqual([]);
  } finally { w.close(); }
});

t('manifest table headers are not drawn in capitals', () => {
  const css = fs.readFileSync(path.join(UI, 'blacksmith.css'), 'utf8');
  const rule = css.match(/\.manifest-table th \{([^}]*)\}/)[1];
  expect(rule).not.toContain('text-transform');
});

t('Broker settings is sentence case in the navigation and routes', () => {
  const { w, d } = boot();
  try {
    const host = d.createElement('div');
    host.innerHTML = w.__T.renderBlacksmithTemplate({});
    const labels = Array.from(host.querySelectorAll('.detail-tab')).map((tab) => tab.textContent.trim());
    expect(labels).toContain('Broker settings');
    expect(labels).not.toContain('Broker Settings');
  } finally { w.close(); }
  const routes = fs.readFileSync(path.join(UI, 'routes.js'), 'utf8');
  expect(routes).toContain("config:'Broker settings'");
  expect(routes).not.toContain('Broker Settings');
});

t('VM and certificate tables use sentence case headers', () => {
  const src = fs.readFileSync(path.join(UI, 'blacksmith.js'), 'utf8');
  const html = fs.readFileSync(path.join(UI, 'index.html'), 'utf8');
  [src, html].forEach((text) => {
    expect(text).not.toMatch(/<th[^>]*>(Created At|Job State|VM State|VM Type|Job Name)<\/th>/);
  });
  expect(src).toContain('<th>Created at</th>');
  expect(src).toContain('<th>VM state</th>');
});
