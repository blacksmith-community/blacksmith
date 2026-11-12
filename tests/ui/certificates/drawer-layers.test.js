const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const H = require('./harness.js');
const { JSDOM } = require('./support.js');
const t = JSDOM ? test : test.skip;

const { boot, sleep } = H.fleet;
const css = fs.readFileSync(path.join(__dirname, '../../../ui/blacksmith.css'), 'utf8');
const helpCss = fs.readFileSync(path.join(__dirname, '../../../ui/action-help.css'), 'utf8');

function rule(text, selector) {
  const at = text.indexOf(`\n${selector} {`);
  if (at < 0) return '';
  return text.slice(at, text.indexOf('}', at));
}
const token = (name) => {
  const m = css.match(new RegExp(`--${name}:\\s*(\\d+)`));
  return m ? Number(m[1]) : NaN;
};

async function openCert(id) {
  const env = await boot();
  const { w, d } = env;
  await w.blacksmithNav.switchTab('certificates'); await sleep(120);
  w.blacksmithCerts.state.attention = false; await w.blacksmithCerts.refresh({ announce: true }); await sleep(80);
  const row = d.querySelector(`#cert-tbody tr.cert-row[data-cert-id="${id}"]`);
  row.focus();
  row.click(); await sleep(120);
  return { ...env, row, dr: d.getElementById('cert-fleet-drawer') };
}

test('the layers stack as dock, drawer, toast, tooltip', () => {
  const [dock, drawer, toast, tip] = ['z-terminal-dock', 'z-drawer', 'z-toast', 'z-tooltip'].map(token);
  expect(dock).toBeLessThan(drawer);
  expect(drawer).toBeLessThan(toast);
  expect(toast).toBeLessThan(tip);
});

test('the terminal strip, drawer, toast, and tooltip use the tokens', () => {
  expect(rule(css, '.minimized-terminal-bar')).toContain('z-index: var(--z-terminal-dock)');
  expect(rule(css, '.cert-fleet-drawer')).toContain('z-index: var(--z-drawer)');
  expect(rule(css, '.notification')).toContain('z-index: var(--z-toast)');
  expect(helpCss).toContain('z-index: var(--z-tooltip');
});

t('the drawer close control is an icon button with a name and a focus key', async () => {
  const { w, dr } = await openCert('c2');
  try {
    const close = dr.querySelector('.cert-drawer-close');
    expect(close.textContent.trim()).toBe('');
    expect(close.querySelector('svg')).toBeTruthy();
    expect(close.getAttribute('aria-label')).toBe('Close certificate details');
    expect(close.dataset.keepKey).toBe('cert-drawer-close');
  } finally { w.close(); }
});

t('the close button, and Escape, close the drawer and return focus to the opener', async () => {
  for (const how of ['click', 'escape']) {
    const { w, d, row, dr } = await openCert('c2');
    try {
      expect(dr.hidden).toBe(false);
      if (how === 'click') dr.querySelector('.cert-drawer-close').click();
      else d.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
      await sleep(40);
      expect(dr.hidden).toBe(true);
      expect(d.activeElement).toBe(row);
    } finally { w.close(); }
  }
});

t('every drawer value cell starts with its copy slot, and empty rows keep the slot', async () => {
  const { w, dr } = await openCert('c2');
  try {
    const rows = Array.from(dr.querySelectorAll('.cert-drawer-body table tr')).filter((tr) => tr.querySelector('.cert-field-value'));
    expect(rows.length).toBeGreaterThan(2);
    let withCopy = 0;
    for (const tr of rows) {
      const wrap = tr.querySelector('.cert-field-value');
      const first = wrap.firstElementChild;
      const slot = wrap.querySelector('.cert-copy-slot');
      if (wrap.querySelector('.cert-copy-btn')) {
        withCopy += 1;
        expect(first.classList.contains('cert-copy-slot')).toBe(true);
        expect(first.querySelector('.cert-copy-btn')).toBeTruthy();
        expect(first.nextElementSibling.classList.contains('cert-field-text')).toBe(true);
      }
      expect(slot).toBe(first);
    }
    expect(withCopy).toBeGreaterThan(0);
    expect(css).toMatch(/\.cert-copy-slot\s*{[^}]*width:\s*28px/);
  } finally { w.close(); }
});
