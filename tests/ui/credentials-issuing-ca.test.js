const { test, expect, afterEach } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');

// The PEM bodies are fixtures. They are not real certificates, because the UI never parses them.
const PEM_A = '-----BEGIN CERTIFICATE-----\nQUFBQUFBQUE=\n-----END CERTIFICATE-----\n';
const PEM_B = '-----BEGIN CERTIFICATE-----\nQkJCQkJCQkI=\n-----END CERTIFICATE-----\n';
const FACTS = [
  { common_name: 'mid-ca', not_after: '2027-03-04T05:06:07Z' },
  { common_name: 'root-ca', not_after: '2030-01-01T00:00:00Z' }
];

const envs = [];
afterEach(() => { while (envs.length) envs.pop().w.close(); });

// item is the body of GET /b/certificates/items/<id>. inventory null makes that request fail.
async function mount(instance, { item, inventory, certificates, items, hang } = {}) {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { w, d } = env;
  const probe = (extra) => ({ source: 'probe', state: 'ok', sha256_fingerprint: 'ab12', owner: { kind: 'instance', instance_id: instance }, ...extra });
  const listener = { id: 'leafid', key: 'sha256:ab12', observations: [probe()] };
  const other = { id: 'otherid', key: '/d/other', observations: [{ source: 'credhub', owner: { kind: 'instance', instance_id: instance } }] };
  w.__routes.push(
    ['/b/' + instance + '/credentials', (u, o, json) => json(200, { username: 'admin', host: 'cache.example' })],
    ['/b/certificates/inventory', (u, o, json) => {
      if (hang) return new Promise(() => {});
      return inventory === null ? json(500, { error: 'x' }) : json(200, { certificates: certificates ? certificates(probe) : [other, listener] });
    }],
    [/\/b\/certificates\/items\/(\w+)/, (u, o, json) => json(200, (items && items[u.split('/').pop()]) || item || {})]
  );
  const host = d.createElement('div');
  d.body.appendChild(host);
  host.innerHTML = await w.__T.fetchServiceDetail(instance, 'credentials');
  await sleep(60);
  return { env, host };
}
const known = { issuer_chain: [PEM_A, PEM_B], issuer_chain_facts: FACTS };
const row = (host) => host.querySelector('tr.issuing-ca-row');

test('the Issuing CA row shows the issuer subject and expiry as text', async () => {
  const { host } = await mount('c1', { item: known });
  const r = row(host);
  expect(r).not.toBeNull();
  expect(r.textContent).toContain('Issuing CA');
  expect(r.textContent).toContain('mid-ca');
  expect(r.textContent).toMatch(/2027/);
  expect(r.querySelectorAll('button').length).toBe(2);
});

test('the PEM never reaches an attribute, a title, or the page text', async () => {
  const { host } = await mount('c2', { item: known });
  const html = row(host).outerHTML;
  expect(html).not.toContain('BEGIN CERTIFICATE');
  expect(html).not.toContain('QUFBQUFBQUE');
  expect(host.textContent).not.toContain('BEGIN CERTIFICATE');
});

test('the buttons carry unique keep keys', async () => {
  const { host } = await mount('c3', { item: known });
  const keys = [...row(host).querySelectorAll('button')].map((b) => b.dataset.keepKey);
  expect(keys.every(Boolean)).toBe(true);
  expect(new Set(keys).size).toBe(2);
  const all = [...host.querySelectorAll('[data-keep-key]')].map((n) => n.dataset.keepKey);
  expect(new Set(all).size).toBe(all.length);
});

test('Copy PEM writes the whole chain to the clipboard and toasts without it', async () => {
  const { env, host } = await mount('c4', { item: known });
  const { w } = env;
  const written = [];
  w.navigator.clipboard = { writeText: (t) => { written.push(t); return Promise.resolve(); } };
  const toasts = [];
  w.Feedback = { success: (m) => toasts.push(m), info: (m) => toasts.push(m), error: (m) => toasts.push(m) };
  row(host).querySelector('[data-issuing-ca-action="copy"]').click();
  await sleep(20);
  expect(written).toEqual([PEM_A + PEM_B]);
  expect(toasts.join(' ')).not.toContain('BEGIN CERTIFICATE');
  expect(env.logs.join(' ')).not.toContain('BEGIN CERTIFICATE');
});

test('Download builds a Blob at click time and saves a .pem file', async () => {
  const { env, host } = await mount('c5', { item: known });
  const { w, d } = env;
  const blobs = [];
  let revoked = 0;
  w.URL.createObjectURL = (b) => { blobs.push(b); return 'blob:x/1'; };
  w.URL.revokeObjectURL = () => { revoked += 1; };
  const clicked = [];
  w.HTMLAnchorElement.prototype.click = function () { clicked.push({ download: this.download, href: this.getAttribute('href') }); };
  expect(blobs.length).toBe(0);
  row(host).querySelector('[data-issuing-ca-action="download"]').click();
  expect(blobs.length).toBe(1);
  const body = await new Promise((resolve) => { const r = new w.FileReader(); r.onload = () => resolve(r.result); r.readAsText(blobs[0]); });
  expect(body).toBe(PEM_A + PEM_B);
  expect(blobs[0].type).toBe('application/x-pem-file');
  expect(clicked).toEqual([{ download: 'issuing-ca.pem', href: 'blob:x/1' }]);
  expect(revoked).toBe(1);
  expect(d.querySelectorAll('a[download]').length).toBe(0);
});

test('a chain that is not known yet says so and offers no buttons', async () => {
  const { host } = await mount('c6', { item: { pem: PEM_A } });
  const r = row(host);
  expect(r).not.toBeNull();
  expect(r.textContent).toMatch(/not known yet/i);
  expect(r.textContent).toMatch(/when the broker can read the signing CA/i);
  expect(r.querySelectorAll('button').length).toBe(0);
});

test('a known empty chain says there is none and offers no buttons', async () => {
  const { host } = await mount('c7', { item: { issuer_chain: [], issuer_chain_facts: [] } });
  const r = row(host);
  expect(r.textContent).toMatch(/no issuing ca/i);
  expect(r.textContent).not.toMatch(/not known yet/i);
  expect(r.querySelectorAll('button').length).toBe(0);
});

test('without the inventory the credentials still render and no row appears', async () => {
  const { host } = await mount('c8', { inventory: null });
  expect(host.textContent).toContain('cache.example');
  expect(row(host)).toBeNull();
});

test('hostile subject text is escaped', async () => {
  const evil = '<img src=x onerror=alert(1)>';
  const { host } = await mount('c9', { item: { issuer_chain: [PEM_A], issuer_chain_facts: [{ common_name: evil, not_after: '2027-03-04T05:06:07Z' }] } });
  expect(row(host).querySelector('img')).toBeNull();
  expect(row(host).textContent).toContain(evil);
});

test('the Credentials table renders with a loading row before the lookups answer', async () => {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { w, d } = env;
  w.__routes.push(
    ['/b/c10/credentials', (u, o, json) => json(200, { username: 'admin' })],
    ['/b/certificates/inventory', () => new Promise(() => {})]
  );
  const host = d.createElement('div');
  d.body.appendChild(host);
  host.innerHTML = await w.__T.fetchServiceDetail('c10', 'credentials');
  expect(host.textContent).toContain('admin');
  expect(row(host).textContent).toMatch(/looking up/i);
  expect(row(host).querySelectorAll('button').length).toBe(0);
});

test('a lookup that never answers is dropped after the timeout and the row goes away', async () => {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { w, d } = env;
  w.AbortController = w.AbortController || AbortController;
  w.__routes.push(
    ['/b/c11/credentials', (u, o, json) => json(200, { username: 'admin' })],
    ['/b/certificates/inventory', (u, o) => new Promise((resolve, reject) => { if (o.signal) o.signal.addEventListener('abort', () => reject(new Error('aborted'))); })]
  );
  const host = d.createElement('div');
  d.body.appendChild(host);
  host.innerHTML = await w.__T.fetchServiceDetail('c11', 'credentials');
  await sleep(3000);
  expect(row(host)).toBeNull();
  expect(host.textContent).toContain('admin');
}, 10000);

test('the lookup never rescans the instance', async () => {
  const { env } = await mount('c12', { item: known });
  const bad = env.calls.filter((c) => /certificates\/instances\//.test(c) || /POST .*certificates\/scan/.test(c));
  expect(bad).toEqual([]);
  expect(env.calls.some((c) => c.includes('/b/certificates/inventory?instance=c12'))).toBe(true);
});

test('the buttons use the existing button classes', async () => {
  const { host } = await mount('c13', { item: known });
  row(host).querySelectorAll('button').forEach((b) => {
    expect(b.classList.contains('btn')).toBe(true);
    expect(b.classList.contains('btn-secondary')).toBe(true);
  });
});

test('Copy PEM failure shows a fixed error toast and leaks nothing', async () => {
  const { env, host } = await mount('c14', { item: known });
  const { w } = env;
  w.navigator.clipboard = { writeText: () => Promise.reject(new Error('denied ' + PEM_A)) };
  const toasts = [];
  w.Feedback = { success: (m) => toasts.push('ok:' + m), info: (m) => toasts.push(m), error: (m) => toasts.push('err:' + m) };
  row(host).querySelector('[data-issuing-ca-action="copy"]').click();
  await sleep(20);
  expect(toasts.length).toBe(1);
  expect(toasts[0].startsWith('err:')).toBe(true);
  expect(toasts.join(' ')).not.toContain('BEGIN CERTIFICATE');
  expect(env.logs.join(' ')).not.toContain('BEGIN CERTIFICATE');
});

for (const [label, next] of [['none', { issuer_chain: [], issuer_chain_facts: [] }], ['not known', { pem: PEM_A }]]) {
  test(`a later ${label} state clears the stored PEM so an old button copies nothing`, async () => {
    const id = 'c15' + label.replace(' ', '');
    const first = await mount(id, { item: known });
    const staleButton = row(first.host).querySelector('[data-issuing-ca-action="copy"]');
    // The same page loads the Credentials tab again, and now the chain is gone.
    const { w, d } = first.env;
    w.__routes.length = 0;
    w.__routes.push(
      ['/b/' + id + '/credentials', (u, o, json) => json(200, { username: 'admin' })],
      ['/b/certificates/inventory', (u, o, json) => json(200, { certificates: [{ id: 'leafid', key: 'k', observations: [{ source: 'probe', state: 'ok', sha256_fingerprint: 'ab12', owner: { instance_id: id } }] }] })],
      ['/b/certificates/items/leafid', (u, o, json) => json(200, next)]
    );
    const host2 = d.createElement('div');
    d.body.appendChild(host2);
    host2.innerHTML = await w.__T.fetchServiceDetail(id, 'credentials');
    await sleep(60);
    expect(row(host2).querySelectorAll('button').length).toBe(0);
    const written = [];
    w.navigator.clipboard = { writeText: (t) => { written.push(t); return Promise.resolve(); } };
    staleButton.click();
    await sleep(20);
    expect(written).toEqual([]);
  });
}

test('an incomplete chain says it stops short of the root', async () => {
  const { host } = await mount('c16', { item: { ...known, issuer_chain_incomplete: true } });
  expect(row(host).textContent).toMatch(/does not reach a root|stops short/i);
  expect(row(host).querySelectorAll('button').length).toBe(2);
});

test('the listener item is the one whose probe succeeded, not the first by hash order', async () => {
  const { host, env } = await mount('c17', {
    certificates: (probe) => [
      { id: 'aaa-failed', key: 'sha256:', observations: [probe({ state: 'failed', sha256_fingerprint: '', locator: '10.0.0.2:6379' })] },
      { id: 'leafid', key: 'sha256:ab12', observations: [probe({ locator: '10.0.0.1:6379' })] }
    ],
    items: { 'aaa-failed': { pem: PEM_A }, leafid: known }
  });
  expect(row(host).textContent).toContain('mid-ca');
  expect(env.calls.some((c) => c.includes('/items/aaa-failed'))).toBe(false);
});

test('between two good probe items the choice does not depend on inventory order', async () => {
  const good = (probe) => [
    { id: 'zzz', key: 'sha256:cd34', observations: [probe({ sha256_fingerprint: 'cd34', locator: '10.0.0.2:6379' })] },
    { id: 'leafid', key: 'sha256:ab12', observations: [probe({ locator: '10.0.0.1:6379' })] }
  ];
  const a = await mount('c18', { certificates: good, items: { zzz: { pem: PEM_A }, leafid: known } });
  const b = await mount('c19', { certificates: (p) => good(p).reverse(), items: { zzz: { pem: PEM_A }, leafid: known } });
  expect(row(a.host).textContent).toContain('mid-ca');
  expect(row(b.host).textContent).toContain('mid-ca');
});
