const { test, expect, afterEach } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const envs = [];
afterEach(() => { while (envs.length) envs.pop().w.close(); });

const MESSAGE = 'credentials are not available because nothing is stored for this instance';
const EVIL = '<img src=x onerror=alert(1)>';

// The broker's answer when it has no credentials to give.
const answer = (status, body) => (u, o, json) => json(status, body);

// Mounts the tab's HTML the way the page does and lets the panel settle.
async function mount(tab, credentialsRoute) {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { w, d } = env;
  w.__routes.push(['/b/inst-1/credentials', credentialsRoute]);
  const host = d.createElement('div');
  d.body.appendChild(host);
  host.innerHTML = await w.__T.fetchServiceDetail('inst-1', tab);
  await sleep(30);
  return { env, host };
}

for (const tab of ['credentials', 'testing']) {
  t(`the ${tab} tab shows the broker's message when credentials are not available`, async () => {
    const { env, host } = await mount(tab, answer(404, { success: false, error: MESSAGE, code: 'not_found' }));
    const detail = host.querySelector('[data-load-error] .load-error-detail');
    expect(detail).not.toBeNull();
    expect(detail.textContent).toBe(MESSAGE);
    expect(env.logs.join('\n')).not.toContain(MESSAGE);
    expect(env.alerts).toEqual([]);
  });

  t(`the ${tab} tab shows the status when the answer has no message`, async () => {
    const { host } = await mount(tab, (u, o, json) => Promise.resolve({ ok: false, status: 503, statusText: 'x', json: () => Promise.reject(new SyntaxError('no body')), text: () => Promise.resolve('') }));
    const detail = host.querySelector('[data-load-error] .load-error-detail');
    expect(detail.textContent).toContain('HTTP 503');
  });

  t(`the ${tab} tab shows a message as text and never as markup`, async () => {
    const { host } = await mount(tab, answer(503, { error: EVIL }));
    const detail = host.querySelector('[data-load-error] .load-error-detail');
    expect(detail.textContent).toBe(EVIL);
    expect(host.querySelector('img')).toBeNull();
    const panel = host.querySelector('[data-load-error]');
    for (const attr of panel.querySelectorAll('*')) {
      for (const { name, value } of attr.attributes) expect(value.includes(EVIL)).toBe(false);
    }
  });
}
