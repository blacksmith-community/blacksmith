const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');

const t = JSDOM ? test : test.skip;
const { boot, sleep } = H.fleet;

const chipTexts = (d) => [...d.querySelectorAll('.cert-chip')].map((chip) => chip.querySelector('.cert-chip-text').textContent);

t('active filter chips use the filter labels and keep the URL values', async () => {
  const { w, d } = await boot();
  await w.blacksmithNav.switchTab('certificates');
  await sleep(100);
  const certs = w.blacksmithCerts;
  expect(certs.state.attention).toBe(true);
  expect(chipTexts(d)).toEqual(['Needs attention']);
  const chip = d.querySelector('.cert-chip');
  expect(chip.getAttribute('aria-label')).toBe('Remove filter Needs attention');
  expect(certs.toQuery(certs.state)).toContain('attention=true');

  const buttonFor = (label) => [...d.querySelectorAll('.cert-strip-btn')].find((b) => b.textContent.startsWith(label));
  buttonFor('Expiry warning').click();
  await sleep(80);
  const select = d.getElementById('cert-filter-service');
  select.value = 'valkey';
  select.dispatchEvent(new w.Event('change'));
  await sleep(80);
  expect(chipTexts(d)).toEqual(['Expiry warning', 'Service: valkey', 'Needs attention']);
  expect(d.querySelectorAll('.cert-chip')[1].getAttribute('aria-label')).toBe('Remove filter Service: valkey');
  const query = certs.toQuery(certs.state);
  expect(query).toContain('expiry=warning');
  expect(query).toContain('service=valkey');
  expect(query).toContain('attention=true');
  expect(d.querySelector('.cert-chips').textContent).not.toMatch(/\w=\w/);

  d.querySelectorAll('.cert-chip')[1].click();
  await sleep(80);
  expect(certs.state.service).toEqual([]);
  w.close();
});
