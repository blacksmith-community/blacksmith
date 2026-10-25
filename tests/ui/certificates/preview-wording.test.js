const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');

const t = JSDOM ? test : test.skip;
const { boot } = H.main;

t('the preview names the CredHub path and the permanent converge wording', () => {
  const { w } = boot();
  try {
    const preview = { instances: [{
      instance_id: 'i1', instance_name: 'Cache 1', vms: 3,
      selected: [{ name: '/secret/cache/tls_crt', days_left: 10 }]
    }] };
    const body = w.__T.buildPreviewBody(preview, 'redeploy');
    const details = body.querySelector('.rotate-preview-path').closest('details');
    expect(details.querySelector('summary').textContent).toBe('Certificate CredHub path');
    const copy = details.querySelector('button');
    expect(copy.getAttribute('aria-label')).toBe('Copy certificate CredHub path');
    expect(copy.title).toBe('Copy certificate CredHub path');
    const converge = w.__T.buildPreviewBody(preview, 'converge');
    expect(converge.textContent).toContain('A redeploy applies the patched values as permanent.');
    expect(converge.textContent).not.toContain('for good');
  } finally { w.close(); }
});
