const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const { boot, sleep } = H.fleet;

// After a patch in place, the CredHub observation describes the latest
// version while the item's facts and PEM describe the deployed one, so the
// drawer must take its fingerprint from the facts.
const DEPLOYED = 'd'.repeat(64);
const LATEST = 'e'.repeat(64);

t('the drawer fingerprint belongs to the certificate it shows', async () => {
  const { w, d, server } = await boot();
  const c2 = server.certs.find((c) => c.id === 'c2');
  c2.observations[0] = Object.assign({}, c2.observations[0], { sha256_fingerprint: LATEST, matches: 'latest', version_id: 'ae5bcf1e' });
  c2.versions = { deployed_version_id: '24528b18', latest_version_id: 'ae5bcf1e' };
  server.itemFacts = { sha256_fingerprint: DEPLOYED };
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  w.blacksmithCerts.state.attention = false; await w.blacksmithCerts.refresh({ announce: true }); await sleep(80);

  const calls = []; w.inspectCertificate = (x) => { calls.push(x); return true; };
  d.querySelector('#cert-tbody tr.cert-row[data-cert-id="c2"]').click(); await sleep(100);
  const dr = d.getElementById('cert-fleet-drawer');
  ok(!dr.hidden, 'drawer opens');

  const detail = dr.querySelector('.cert-drawer-detail');
  const text = detail ? detail.textContent : '';
  ok(text.includes(DEPLOYED), 'the detail table shows the deployed fingerprint');
  ok(!text.includes(LATEST), 'the detail table does not show the latest version\'s fingerprint');

  Array.from(dr.querySelectorAll('button')).find((b) => b.textContent === 'Inspect').click();
  ok(calls.length === 1 && calls[0].fingerprints.sha256 === DEPLOYED, 'Inspect passes the deployed fingerprint: ' + (calls[0] && calls[0].fingerprints.sha256));

  expect(failures).toEqual([]);
});
