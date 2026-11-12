const { test, expect, afterEach } = require('bun:test');
const fs = require('fs');
const path = require('path');
const H = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const { watchUncaught } = require('./watch-uncaught.js');

const t = JSDOM ? test : test.skip;
const UI = path.resolve(__dirname, '..', '..', 'ui');
const html = fs.readFileSync(path.join(UI, 'index.html'), 'utf8');
const head = html.slice(0, html.indexOf('</head>'));
const inline = [...head.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]);

const windows = [];
afterEach(() => { windows.splice(0).forEach((w) => w.close()); });

// The state a theme leaves on the page: the root's attributes and colour
// scheme, and the colour the browser paints its own chrome with.
const state = (w) => {
  const root = w.document.documentElement;
  return {
    theme: root.getAttribute('data-theme'),
    contrast: root.getAttribute('data-contrast'),
    scheme: root.style.colorScheme,
    meta: w.document.querySelector('meta[name="theme-color"]').getAttribute('content'),
  };
};

// Runs the head's inline script against the page's own head, with the
// saved theme and the system preference given, the way a browser runs it
// before the body is parsed.
const runInline = ({ saved, dark, storage = 'ok', media = 'ok' }) => {
  const doc = head.replace(/<script[\s\S]*?<\/script>/g, '').replace(/<link[^>]*>/g, '') + '</head><body></body></html>';
  const dom = new JSDOM(doc, { runScripts: 'outside-only', url: 'http://localhost/' });
  const w = watchUncaught(dom.window);
  windows.push(w);
  if (storage === 'throws') {
    w.Storage.prototype.getItem = function () { throw new w.Error('SecurityError: storage blocked'); };
  } else if (saved != null) {
    w.localStorage.setItem('theme', saved);
  }
  if (media === 'missing') delete w.matchMedia;
  else if (media === 'throws') w.matchMedia = () => { throw new w.Error('matchMedia failed'); };
  else w.matchMedia = (q) => ({ matches: q === '(prefers-color-scheme: dark)' && dark, addListener() {}, addEventListener() {} });
  const errors = [];
  w.console.error = (...a) => errors.push(a.join(' '));
  w.console.log = w.console.warn = w.console.debug = w.console.info = (...a) => errors.push(a.join(' '));
  w.eval(inline[0]);
  expect(errors).toEqual([]);
  return state(w);
};

// What setTheme in blacksmith.js leaves for the same theme.
const expected = (theme) => {
  const env = H.main.boot({ noDialogs: true });
  windows.push(env.w);
  env.w.__T.setTheme(theme);
  return state(env.w);
};

test('the head carries the iOS status bar style and one inline theme script', () => {
  expect(head).toMatch(/<meta name="apple-mobile-web-app-status-bar-style" content="default">/);
  expect(inline.length).toBe(1);
  // It must follow the theme-color meta it updates.
  expect(head.indexOf('<meta name="theme-color"')).toBeLessThan(head.indexOf('<script>'));
  expect(inline[0]).not.toMatch(/console\.|alert\(|confirm\(|prompt\(/);
  expect(inline[0]).toMatch(/try\s*\{[^}]*localStorage\.getItem\('theme'\)[^}]*\}\s*catch/);
});

// Each saved theme, and the system preference when none is saved, gives
// the same page state the full script gives later, so the two never fight
// and a dark-theme user sees no light frame.
const cases = [
  { name: 'nothing saved on a light system', saved: null, dark: false, theme: 'light' },
  { name: 'nothing saved on a dark system', saved: null, dark: true, theme: 'dark' },
  { name: 'saved light on a dark system', saved: 'light', dark: true, theme: 'light' },
  { name: 'saved dark on a light system', saved: 'dark', dark: false, theme: 'dark' },
  { name: 'saved high contrast light', saved: 'high-contrast-light', dark: true, theme: 'high-contrast-light' },
  { name: 'saved high contrast dark', saved: 'high-contrast-dark', dark: false, theme: 'high-contrast-dark' },
  { name: 'an unknown saved theme', saved: 'sepia', dark: true, theme: 'sepia' },
];
for (const c of cases) {
  t('the inline theme matches setTheme with ' + c.name, () => {
    expect(runInline(c)).toEqual(expected(c.theme));
  });
}

t('blocked storage falls back to the system preference without throwing', () => {
  expect(runInline({ storage: 'throws', dark: true })).toEqual(expected('dark'));
  expect(runInline({ storage: 'throws', dark: false })).toEqual(expected('light'));
});

t('a missing or failing matchMedia leaves the light theme', () => {
  expect(runInline({ saved: null, media: 'missing' })).toEqual(expected('light'));
  expect(runInline({ saved: null, media: 'throws' })).toEqual(expected('light'));
  expect(runInline({ saved: 'dark', media: 'throws' })).toEqual(expected('dark'));
});
