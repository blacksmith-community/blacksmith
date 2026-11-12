const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');
const { pinZone } = require('../fixed-zone.js');
pinZone('UTC');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;
const { boot, openInstanceTab, sleep, day, now, iso } = H.instance;

const manifestCert = {
  id: 'c1', key: '/x/valkey_tls', variable: 'valkey_tls', kind: 'leaf', common_name: 'valkey.internal',
  effective_not_after: iso(now + 5 * day), days_left: 5, expiry: 'critical', health: 'ok', attention: false,
  coverage: { complete: true }, observations: [{ source: 'manifest', locator: 'instance_groups/valkey/properties/tls/cert', state: 'ok', not_after: iso(now + 5 * day) }]
};
const item = Object.assign({}, manifestCert, {
  pem: '-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----',
  facts: { common_name: 'valkey.internal', issuer: 'CN=Services CA', serial: '0a', sha256_fingerprint: 'AA:BB', dns_names: ['valkey.internal'] }
});
const body = () => ({
  instance: { id: 'i1', name: 'valkey-1', deployment: 'd' }, scan: { scanned_at: iso(now), last_success_at: iso(now), stale: false },
  certificates: [manifestCert], rotatable_variables: [], summary: { certificates: 1 }, capabilities: {}
});

t('a manifest certificate opens the shared certificate inspection modal', async () => {
  const { w, d, json } = boot({ oat: true });
  const inspected = [];
  w.inspectCertificate = (cert) => { inspected.push(cert); return true; };
  w.__routes = [
    [/certificates\/instances\//, () => json(200, body())],
    [/certificates\/items\/c1/, () => json(200, item)]
  ];
  await openInstanceTab(w, d, 'i1');
  await sleep(30);
  d.querySelector('[data-cert-action=fetch-manifest]').click();
  await sleep(30);
  const rows = d.getElementById('manifest-certificates-list-i1').querySelectorAll('.cert-list-item');
  ok(rows.length === 1, 'one manifest certificate is listed: ' + rows.length);
  const button = rows[0].querySelector('.cert-inspect-btn');
  ok(!!button, 'the manifest row has an Inspect button');
  button.click();
  await sleep(30);
  ok(inspected.length === 1, 'inspect ran once through the shared modal: ' + inspected.length);
  const legacy = inspected[0] || {};
  ok(legacy.subject === 'CN=valkey.internal' && /BEGIN CERTIFICATE/.test(legacy.pemEncoded || ''), 'the modal gets the certificate facts and PEM');
  ok(legacy.fingerprints && legacy.fingerprints.sha256 === 'AA:BB', 'the modal gets the fingerprint');
  expect(failures).toEqual([]);
}, 120000);
