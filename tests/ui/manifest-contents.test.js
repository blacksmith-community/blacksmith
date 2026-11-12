const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { JSDOM } = require('./certificates/support.js');
const { watchUncaught } = require('./watch-uncaught.js');

const UI = path.resolve(__dirname, '..', '..', 'ui');
const manifest = JSON.parse(fs.readFileSync(path.join(UI, 'manifest.webmanifest'), 'utf8'));
const t = JSDOM ? test : test.skip;

// The splash screen and title bar use the manifest colours before any page
// script runs, so they must match the default light theme. The head's
// theme-color meta and the light background setTheme writes in
// blacksmith.js are the same colour.
test('the manifest colours match the default light theme', () => {
  const html = fs.readFileSync(path.join(UI, 'index.html'), 'utf8');
  const meta = html.match(/<meta name="theme-color" content="([^"]+)">/)[1];
  const js = fs.readFileSync(path.join(UI, 'blacksmith.js'), 'utf8');
  const light = js.match(/const background = highContrast \? \(dark \? '[^']+' : '[^']+'\) : \(dark \? '[^']+' : '([^']+)'\);/)[1];
  expect(meta).toBe(light);
  expect(manifest.background_color).toBe(light);
  expect(manifest.theme_color).toBe(light);
});

// Screenshots need real captures, so the manifest has none.
test('the manifest lists no screenshots', () => {
  expect(manifest.screenshots).toBeUndefined();
});

// A shortcut opens one of the four areas from the installed app's icon
// menu. Each URL must stay inside the app's scope, parse to its area in
// routes.js, and use an icon the broker serves without credentials.
t('the manifest has a shortcut for each of the four areas', () => {
  const dom = new JSDOM('<!DOCTYPE html><html><body></body></html>', { runScripts: 'outside-only', url: 'http://localhost/' });
  const w = watchUncaught(dom.window);
  w.eval(fs.readFileSync(path.join(UI, 'routes.js'), 'utf8'));
  const routes = w.BlacksmithRoutes;

  expect(Array.isArray(manifest.shortcuts)).toBe(true);
  const screens = manifest.shortcuts.map((s) => {
    expect(typeof s.name).toBe('string');
    expect(s.name.length).toBeGreaterThan(0);
    expect(typeof s.short_name).toBe('string');
    expect(s.short_name.length).toBeLessThanOrEqual(12);
    const url = new URL(s.url, 'http://localhost' + manifest.scope);
    expect(url.origin + url.pathname).toBe('http://localhost' + manifest.scope);
    const route = routes.parse(url.hash);
    expect(route).not.toBeNull();
    expect(s.url).toBe('/' + routes.format(route));
    for (const icon of s.icons || []) {
      expect(icon.src).toMatch(/^\/assets\/brand\/icon-[0-9]{1,4}\.png$/);
      expect(fs.existsSync(path.join(UI, icon.src))).toBe(true);
    }
    return route.screen;
  });
  expect(screens).toEqual(['blacksmith', 'services', 'certificates', 'upgrade']);
  w.close();
});
