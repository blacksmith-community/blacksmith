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
async function mount(instance, { item, inventory } = {}) {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { w, d } = env;
  const listener = {
    id: 'leafid', key: '/d/leaf', observations: [{ source: 'probe', owner: { kind: 'instance', instance_id: instance } }]
  };
  const other = { id: 'otherid', key: '/d/other', observations: [{ source: 'credhub', owner: { kind: 'instance', instance_id: instance } }] };
  w.__routes.push(
    ['/b/' + instance + '/credentials', (u, o, json) => json(200, { username: 'admin', host: 'cache.example' })],
    ['/b/certificates/inventory', (u, o, json) => (inventory === null ? json(500, { error: 'x' }) : json(200, { certificates: [other, listener] }))],
    ['/b/certificates/items/leafid', (u, o, json) => json(200, item || {})]
  );
  const host = d.createElement('div');
  d.body.appendChild(host);
  host.innerHTML = await w.__T.fetchServiceDetail(instance, 'credentials');
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
