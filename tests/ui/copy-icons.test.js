const { test, expect } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const POLYLINE = 'polyline';

// Mounts rendered markup in the document so the delegated click handler runs.
const mount = (d, html) => {
  const host = d.createElement('div');
  host.innerHTML = html;
  d.body.appendChild(host);
  return host;
};

const copyButtons = (host) => Array.from(host.querySelectorAll('button[data-entity-copy], button.copy-btn-inline'));

const expectIconButtons = (buttons) => {
  expect(buttons.length).toBeGreaterThan(0);
  buttons.forEach((b) => {
    expect(b.querySelector('svg')).not.toBeNull();
    expect(b.querySelector('svg').getAttribute('stroke')).toBe('currentColor');
    expect(b.textContent.trim()).toBe('');
    expect(b.getAttribute('aria-label') || '').toMatch(/^Copy /);
  });
};

t('the broker overview copy buttons are icon buttons that copy their value', async () => {
  const { w, d } = boot();
  try {
    const copied = [];
    w.navigator.clipboard = { writeText: (v) => { copied.push(v); return Promise.resolve(); } };
    const host = mount(d, w.__T.renderBlacksmithOverview({ deployment: 'ocfp-lab-blacksmith', env: 'ocfp-lab', instances: {}, plans: {} }));
    const buttons = copyButtons(host);
    expectIconButtons(buttons);
    const deployment = buttons.find((b) => b.getAttribute('aria-label') === 'Copy Deployment');
    const environment = buttons.find((b) => b.getAttribute('aria-label') === 'Copy Environment');
    expect(deployment).toBeDefined();
    expect(environment).toBeDefined();
    expect(deployment.classList.contains('ghost')).toBe(false);

    environment.click();
    await sleep(120);
    expect(copied).toEqual(['ocfp-lab']);
    expect(d.getElementById('blacksmith-live-region').textContent).toBe('Copied to clipboard');
    expect(environment.querySelector(POLYLINE)).not.toBeNull();
    expect(environment.getAttribute('aria-label')).toBe('Copy Environment');
  } finally { w.close(); }
});

t('the instance overview copy buttons are icon buttons that copy their value', async () => {
  const { w, d } = boot();
  try {
    const copied = [];
    w.navigator.clipboard = { writeText: (v) => { copied.push(v); return Promise.resolve(); } };
    mount(d, w.__T.renderServiceDetail('id-1', { service_id: 'valkey', plan_id: 'cluster' }, null));
    const overview = mount(d, w.serviceInstanceDetailsContent['id-1']);
    const buttons = copyButtons(overview);
    expectIconButtons(buttons);
    const first = buttons[0];
    const expected = first.dataset.entityCopy;
    first.click();
    await sleep(120);
    expect(copied).toEqual([expected]);
    expect(d.getElementById('blacksmith-live-region').textContent).toBe('Copied to clipboard');
  } finally { w.close(); }
});

t('inline copy buttons keep their icon and copy through copyValue', async () => {
  const { w, d } = boot();
  try {
    const copied = [];
    w.navigator.clipboard = { writeText: (v) => { copied.push(v); return Promise.resolve(); } };
    const host = mount(d, '<span class="copy-wrapper"><span>vm-1</span><button class="copy-btn-inline" aria-label="Copy VM" title="Copy VM"><svg stroke="currentColor"></svg></button></span>');
    const button = host.querySelector('button');
    await w.copyValue({ preventDefault() {}, stopPropagation() {}, currentTarget: button }, 'vm-1');
    expect(copied).toEqual(['vm-1']);
    expect(button.classList.contains('copied')).toBe(true);
    expect(button.querySelector(POLYLINE)).not.toBeNull();
  } finally { w.close(); }
});
