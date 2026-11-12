const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');
const t = JSDOM ? test : test.skip;

const { boot, sleep } = H.fleet;
const css = fs.readFileSync(path.resolve(__dirname, '..', '..', '..', 'ui', 'blacksmith.css'), 'utf8');

const open = async (d, index) => {
  d.querySelectorAll('#cert-tbody tr.cert-row')[index].click();
  await sleep(80);
};

t('the drawer takes at least half the viewport, and all of it on a phone', () => {
  const rule = css.match(/\.cert-fleet-drawer \{([^}]*)\}/)[1];
  expect(rule).toContain('width: min(max(50vw, 520px), 100vw)');
  expect(css).toMatch(/@media \(max-width: 768px\) \{\s*\.cert-fleet-drawer \{\s*width: 100%;/);
});

t('the drawer closes from its button and Escape, and a second cert replaces the first', async () => {
  const { w, d } = await boot();
  try {
    await w.blacksmithNav.switchTab('certificates');
    await sleep(120);
    const drawer = d.getElementById('cert-fleet-drawer');
    const title = d.getElementById('cert-drawer-title');

    await open(d, 0);
    expect(drawer.hidden).toBe(false);
    const first = title.textContent;
    await open(d, 1);
    expect(drawer.hidden).toBe(false);
    expect(title.textContent).not.toBe(first);
    expect(d.querySelectorAll('#cert-fleet-drawer').length).toBe(1);

    drawer.querySelector('.cert-drawer-close').click();
    expect(drawer.hidden).toBe(true);
    expect(d.activeElement.dataset.certId).toBe(d.querySelectorAll('#cert-tbody tr.cert-row')[1].dataset.certId);

    await open(d, 0);
    d.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    expect(drawer.hidden).toBe(true);
    expect(d.activeElement.dataset.certId).toBe(d.querySelectorAll('#cert-tbody tr.cert-row')[0].dataset.certId);
  } finally { w.close(); }
});
