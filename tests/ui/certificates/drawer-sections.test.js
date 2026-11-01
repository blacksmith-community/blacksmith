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
  const cert = server.certs.find((c) => c.id === id);
  if (tweak) tweak(cert);
  const copied = [];
  w.navigator.clipboard = { writeText: (text) => { copied.push(text); return Promise.resolve(); } };
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  w.blacksmithCerts.state.attention = false; await w.blacksmithCerts.refresh({ announce: true }); await sleep(80);
  d.querySelector(`#cert-tbody tr.cert-row[data-cert-id="${id}"]`).click(); await sleep(120);
  const dr = d.getElementById('cert-fleet-drawer');
  return { ...env, dr, copied };
}
const heading = (dr, text) => Array.from(dr.querySelectorAll('h4')).find((h) => h.textContent === text);
const position = (dr, node) => Array.from(dr.querySelector('.cert-drawer-body').children).findIndex((c) => c === node || c.contains(node));

t('the drawer reads buttons, warnings and notices, CredHub details, Certificate details, then Probe information', async () => {
  const { w, dr } = await openCert('c2', (c) => {
    c.findings = [
      { code: 'a', severity: 'info', message: 'This is a notice.' },
      { code: 'b', severity: 'warning', message: 'This is a warning.' }
    ];
  });
  try {
    const actions = dr.querySelector('.cert-drawer-actions');
    const callouts = dr.querySelector('.cert-drawer-callouts');
    const order = [actions, callouts, heading(dr, 'CredHub details'), heading(dr, 'Certificate details'), heading(dr, 'Probe information')];
    order.forEach((node) => expect(node).toBeTruthy());
    const spots = order.map((node) => position(dr, node));
    expect(spots).toEqual([...spots].sort((a, b) => a - b));
    expect(new Set(spots).size).toBe(5);
  } finally { w.close(); }
});

t('each warning and each notice is its own callout, with warnings first', async () => {
  const { w, dr } = await openCert('c2', (c) => {
    c.findings = [
      { code: 'a', severity: 'info', message: 'First notice.' },
      { code: 'b', severity: 'warning', message: 'First warning.' },
      { code: 'c', severity: 'error', message: 'The error.' },
      { code: 'e', severity: 'warning', message: 'Second warning.' },
      { code: 'd', severity: 'info', message: 'Second notice.' }
    ];
    c.rotation = { allowed: true, modes: ['redeploy'], reasons: [] };
  });
  try {
    const boxes = Array.from(dr.querySelectorAll('.cert-drawer-callouts .cert-callout'));
    expect(boxes.map((b) => b.classList.contains('cert-callout--warn') ? 'warn' : 'info')).toEqual(['warn', 'warn', 'warn', 'info', 'info']);
    expect(boxes.map((b) => b.querySelector('.cert-callout-label').textContent)).toEqual(['Error', 'Warning', 'Warning', 'Notice', 'Notice']);
    expect(boxes.map((b) => b.getAttribute('data-variant'))).toEqual(['warning', 'warning', 'warning', null, null]);
    expect(boxes.map((b) => b.querySelector('.cert-callout-text').textContent.split(' ').slice(0, 2).join(' '))).toEqual(['The error.', 'First warning.', 'Second warning.', 'First notice.', 'Second notice.']);
    boxes.forEach((b) => expect(b.querySelector('.cert-callout-label').textContent).toMatch(/^(Error|Warning|Notice)$/));
  } finally { w.close(); }
});

t('no callout section renders when there is nothing to say', async () => {
  const { w, dr } = await openCert('c2', (c) => {
    c.findings = [];
    c.rotation = { allowed: true, modes: ['redeploy'], reasons: [] };
  });
  try {
    expect(dr.querySelector('.cert-drawer-callouts')).toBeNull();
  } finally { w.close(); }
});

t('CredHub details is a label and value table with copy actions and no secret field', async () => {
  const { w, dr, copied, d } = await openCert('c2', (c) => {
    c.key = '/ws-bosh/valkey/var2';
    c.versions = { deployed_version_id: 'v-deployed-1', latest_version_id: 'v-latest-2', deployed_not_after: iso(Date.now() + DAY), latest_not_after: iso(Date.now() + 5 * DAY), transitional: true };
    c.observations = [
      { source: 'credhub', owner: { kind: 'instance' }, locator: '/ws-bosh/valkey/var2', version_id: 'v-latest-2', sha256_fingerprint: 'abc123', matches: 'latest', state: 'ok', not_after: iso(Date.now() + 5 * DAY), observed_at: iso(Date.now()) }
    ];
  });
  try {
    const table = dr.querySelector('.cert-drawer-credhub table.cert-details-table');
    expect(table).toBeTruthy();
    const labels = Array.from(table.querySelectorAll('th')).map((th) => th.textContent);
    expect(labels).toEqual(expect.arrayContaining(['Credential name', 'Referenced by deployment', 'Latest in CredHub', 'Version ID', 'SHA-256 fingerprint', 'Expires', 'Last read']));
    labels.forEach((label) => expect(label).not.toMatch(/private|key material|password|secret|value|pem/i));
    expect(table.textContent).not.toMatch(/BEGIN (CERTIFICATE|PRIVATE)/);
    const row = Array.from(table.querySelectorAll('tr')).find((tr) => tr.querySelector('th').textContent === 'Credential name');
    row.querySelector('.cert-copy-btn').click(); await sleep(10);
    expect(copied).toEqual(['/ws-bosh/valkey/var2']);
    expect(d.getElementById('cert-fleet-drawer').querySelectorAll('.cert-drawer-credhub .cert-copy-btn').length).toBeGreaterThan(3);
  } finally { w.close(); }
});

t('the probe table has one row per target and copies exactly the locator', async () => {
  const { w, dr, copied } = await openCert('c2', (c) => {
    c.observations = [
      { source: 'credhub', owner: { kind: 'instance' }, locator: '/x/y', state: 'ok', observed_at: iso(Date.now()) },
      { source: 'probe', owner: { kind: 'instance' }, locator: 'valkey/0 10.0.0.5:6379', state: 'ok', matches: 'deployed', not_after: iso(Date.now() + 9 * DAY), observed_at: iso(Date.now()) },
      { source: 'probe', owner: { kind: 'instance' }, locator: 'valkey/1 port 6379', state: 'failed', error: 'timed out', observed_at: iso(Date.now()) }
    ];
  });
  try {
    const section = dr.querySelector('.cert-drawer-probes');
    const rows = Array.from(section.querySelectorAll('tbody tr'));
    expect(rows.length).toBe(2);
    expect(Array.from(section.querySelectorAll('thead th')).map((th) => th.textContent)).toEqual(['Endpoint', 'Result', 'Matched', 'Expiry seen', 'Ran']);
    expect(rows[0].querySelector('.cert-field-text').textContent).toBe('valkey/0 10.0.0.5:6379');
    expect(rows[1].textContent).toContain('timed out');
    rows[1].querySelector('.cert-copy-btn').click(); await sleep(10);
    expect(copied).toEqual(['valkey/1 port 6379']);
    expect(dr.querySelector('.cert-obs-list')).toBeNull();
  } finally { w.close(); }
});

t('a single plain line replaces the probe table when no probes ran', async () => {
  const { w, dr } = await openCert('c2', (c) => {
    c.observations = [{ source: 'credhub', owner: { kind: 'instance' }, locator: '/x/y', state: 'ok', observed_at: iso(Date.now()) }];
  });
  try {
    const section = dr.querySelector('.cert-drawer-probes');
    expect(section.querySelector('table')).toBeNull();
    expect(section.querySelector('p').textContent).toBe('No listener probes have run for this certificate.');
  } finally { w.close(); }
});

t('Other sources lists manifest and vault observations after Probe information', async () => {
  const { w, dr, copied } = await openCert('c2', (c) => {
    c.findings = [{ code: 'a', severity: 'warning', message: 'A warning.' }];
    c.observations = [
      { source: 'credhub', owner: { kind: 'instance' }, locator: '/x/y', state: 'ok', observed_at: iso(Date.now()) },
      { source: 'probe', owner: { kind: 'instance' }, locator: 'valkey/0 10.0.0.5:6379', state: 'ok', matches: 'deployed', observed_at: iso(Date.now()) },
      { source: 'manifest', owner: { kind: 'instance' }, locator: 'instance_groups/0/properties/tls', sha256_fingerprint: 'fp-manifest', matches: 'deployed', state: 'ok', observed_at: iso(Date.now()) },
      { source: 'vault', owner: { kind: 'instance' }, locator: 'secret/x/cert', state: 'ok', observed_at: iso(Date.now()) }
    ];
  });
  try {
    const section = dr.querySelector('.cert-drawer-others');
    expect(heading(dr, 'Other sources')).toBeTruthy();
    const rows = Array.from(section.querySelectorAll('tbody tr'));
    expect(rows.map((r) => r.querySelector('td').textContent)).toEqual(['manifest', 'vault']);
    expect(Array.from(section.querySelectorAll('thead th')).map((th) => th.textContent)).not.toContain('Expires');
    const order = [dr.querySelector('.cert-drawer-actions'), dr.querySelector('.cert-drawer-callouts'), heading(dr, 'CredHub details'), heading(dr, 'Certificate details'), heading(dr, 'Probe information'), heading(dr, 'Other sources')];
    const spots = order.map((n) => position(dr, n));
    expect(new Set(spots).size).toBe(6);
    expect(spots).toEqual([...spots].sort((a, b) => a - b));
    rows[0].querySelectorAll('.cert-copy-btn')[1].click(); await sleep(10);
    expect(copied).toEqual(['fp-manifest']);
  } finally { w.close(); }
});

t('Other sources is absent with only CredHub and probe observations', async () => {
  const { w, dr } = await openCert('c2', (c) => {
    c.observations = [
      { source: 'credhub', owner: { kind: 'instance' }, locator: '/x/y', state: 'ok', observed_at: iso(Date.now()) },
      { source: 'probe', owner: { kind: 'instance' }, locator: 'valkey/0 10.0.0.5:6379', state: 'ok', observed_at: iso(Date.now()) }
    ];
  });
  try {
    expect(dr.querySelector('.cert-drawer-others')).toBeNull();
  } finally { w.close(); }
});

t('a failed vault read shows its error and a stale one says so', async () => {
  const { w, dr } = await openCert('c2', (c) => {
    c.observations = [
      { source: 'vault', owner: { kind: 'instance' }, locator: 'secret/x/cert', state: 'failed', error: 'it holds no certificate', observed_at: iso(Date.now()) },
      { source: 'manifest', owner: { kind: 'instance' }, locator: 'a/b', state: 'stale', error: 'director unreachable', observed_at: iso(Date.now()) }
    ];
  });
  try {
    const section = dr.querySelector('.cert-drawer-others');
    expect(Array.from(section.querySelectorAll('thead th')).map((th) => th.textContent)).toContain('Status');
    const rows = Array.from(section.querySelectorAll('tbody tr')).map((r) => r.textContent);
    expect(rows[0]).toContain('Failed, it holds no certificate');
    expect(rows[1]).toContain('Stale, director unreachable');
  } finally { w.close(); }
});

t('an unconverged divergence says BOSH still records the old version, once', async () => {
  const since = iso(Date.now() - DAY);
  const bare = await openCert('c2', (c) => {
    c.findings = [];
    c.rotation = { allowed: true, modes: ['redeploy'], reasons: [] };
    c.divergence = { state: 'unconverged', task_id: '7', since };
  });
  try {
    const texts = Array.from(bare.dr.querySelectorAll('.cert-callout-text')).map((n) => n.textContent);
    expect(texts.length).toBe(1);
    expect(texts[0]).toContain('BOSH still records the old one');
    expect(texts[0]).not.toContain('older copy');
  } finally { bare.w.close(); }
  const both = await openCert('c2', (c) => {
    c.findings = [{ code: 'unconverged', severity: 'warning', message: 'Task 7 patched this certificate on the VMs, but BOSH does not yet record the patched version as deployed.' }];
    c.rotation = { allowed: true, modes: ['redeploy'], reasons: [] };
    c.divergence = { state: 'unconverged', task_id: '7', since };
  });
  try {
    expect(both.dr.querySelectorAll('.cert-callout').length).toBe(1);
  } finally { both.w.close(); }
});

t('the probe table shows the served fingerprint with a copy action', async () => {
  const { w, dr, copied } = await openCert('c2', (c) => {
    c.observations = [{ source: 'probe', owner: { kind: 'instance' }, locator: 'valkey/0 10.0.0.5:6379', sha256_fingerprint: 'served-fp', state: 'ok', matches: 'latest', observed_at: iso(Date.now()) }];
  });
  try {
    const section = dr.querySelector('.cert-drawer-probes');
    expect(Array.from(section.querySelectorAll('thead th')).map((th) => th.textContent)).toContain('SHA-256 fingerprint');
    section.querySelectorAll('tbody .cert-copy-btn')[1].click(); await sleep(10);
    expect(copied).toEqual(['served-fp']);
  } finally { w.close(); }
});

t('Kind shows even when CredHub holds nothing for the certificate', async () => {
  const { w, dr } = await openCert('c2', (c) => {
    c.key = 'sha256:abc';
    c.versions = undefined;
    c.observations = [{ source: 'manifest', owner: { kind: 'instance' }, locator: 'a/b', state: 'ok', observed_at: iso(Date.now()) }];
  });
  try {
    const labels = Array.from(dr.querySelectorAll('.cert-drawer-credhub th')).map((th) => th.textContent);
    expect(labels).toEqual(expect.arrayContaining(['Kind', 'Read from']));
  } finally { w.close(); }
});
