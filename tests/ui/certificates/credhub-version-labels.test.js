const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');
const t = JSDOM ? test : test.skip;

const { boot, sleep } = H.fleet;
const DAY = 86400000;
const iso = (ms) => new Date(ms).toISOString();

async function openCert(id, tweak) {
  const env = await boot();
  const { w, d, server } = env;
  tweak(server.certs.find((c) => c.id === id));
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  w.blacksmithCerts.state.attention = false; await w.blacksmithCerts.refresh({ announce: true }); await sleep(80);
  d.querySelector(`#cert-tbody tr.cert-row[data-cert-id="${id}"]`).click(); await sleep(120);
  return { ...env, dr: d.getElementById('cert-fleet-drawer') };
}

const pending = (c, matches) => {
  c.key = '/ws-bosh/valkey/var2';
  c.versions = { deployed_version_id: 'v-old', latest_version_id: 'v-new', deployed_not_after: iso(Date.now() + DAY), latest_not_after: iso(Date.now() + 5 * DAY) };
  c.findings = [{ code: 'deploy_pending', severity: 'info', message: 'CredHub holds version v-new, but the deployment still uses version v-old.' }];
  c.observations = [
    { source: 'credhub', locator: '/ws-bosh/valkey/var2', version_id: 'v-new', sha256_fingerprint: 'abc123', matches, state: 'ok', not_after: iso(Date.now() + 5 * DAY), observed_at: iso(Date.now()) }
  ];
};
const labels = (dr) => Array.from(dr.querySelectorAll('.cert-drawer-credhub th')).map((th) => th.textContent);
const notices = (dr) => Array.from(dr.querySelectorAll('.cert-callout-text')).map((n) => n.textContent);

t('each CredHub detail label names its subject, and the heading is sentence case', async () => {
  const { w, dr } = await openCert('c2', (c) => pending(c, 'latest'));
  try {
    expect(Array.from(dr.querySelectorAll('h4')).map((h) => h.textContent)).toContain('CredHub details');
    const got = labels(dr);
    expect(got).toEqual(expect.arrayContaining(['Referenced by deployment', 'Referenced version expires', 'Latest in CredHub', 'Latest in CredHub expires', 'Listener serves']));
    ['Deployed version', 'Latest version', 'Matches'].forEach((old) => expect(got).not.toContain(old));
  } finally { w.close(); }
});

t('the notice says what the listener serves when it serves the latest certificate', async () => {
  const { w, dr } = await openCert('c2', (c) => pending(c, 'latest'));
  try {
    expect(notices(dr)).toContain('The listener serves the latest certificate, but the deployment still references an older version.');
    expect(notices(dr).join(' ')).not.toContain('still uses');
  } finally { w.close(); }
});

t('the notice keeps the version IDs when the listener does not serve the latest certificate', async () => {
  const { w, dr } = await openCert('c2', (c) => pending(c, 'deployed'));
  try {
    expect(notices(dr)).toContain('CredHub holds version v-new, but the deployment still references version v-old.');
  } finally { w.close(); }
});
