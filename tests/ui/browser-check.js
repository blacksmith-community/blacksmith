// Run with playwright-cli run-code --filename=tests/ui/browser-check.js
// Start `python3 -m http.server 4199 --directory ui` first. All API calls are fixtures.
async (page) => {
  const checks = [];
  const errors = [];
  const writes = [];
  let delayCredentials = false;
  const check = (condition, message) => { if (!condition) throw new Error(message); checks.push(message); };
  const catalog = { services: [{ id: 'valkey', name: 'valkey', description: 'Managed cache', plans: [{ id: 'small', name: 'small', description: 'Single node cache' }] }] };
  const instances = Object.fromEntries(Array.from({ length: 12 }, (_, i) => [`cache-${i + 1}`, {
    service_id: 'valkey', plan_id: 'small', instance_name: `Application cache ${i + 1}`,
    created_at: '2026-10-20T10:00:00Z', stemcell: 'ubuntu-jammy/1.80', deployment_name: `small-cache-${i + 1}`,
    vm_status: 'running', vm_count: 1, vm_healthy: 1
  }]));
  page.on('pageerror', error => errors.push(error.message));
  await page.unrouteAll();
  await page.route('**/v2/catalog', route => route.fulfill({ json: catalog }));
  await page.route('**/b/**', async route => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() !== 'GET') {
      writes.push(path);
      return route.fulfill({ status: 403, json: { error: 'Writes blocked in UI test' } });
    }
    let json = {};
    if (path === '/b/blacksmith/credentials' && delayCredentials) await new Promise(resolve => setTimeout(resolve, 200));
    if (path === '/b/status') json = { env: 'UI regression fixture', deployment: 'blacksmith-preview', instances, plans: { 'valkey/small': { limit: 100 } } };
    else if (path.includes('ui-terminal-status')) json = { enabled: false };
    else if (path.endsWith('/stemcells')) json = [{ operating_system: 'ubuntu-jammy', version: '1.90' }];
    else if (path.endsWith('/releases')) json = [{ name: 'valkey-forge', release_versions: [{ version: '1.3.0' }] }];
    else if (path.endsWith('/deployment-releases')) json = Object.fromEntries(Object.keys(instances).map(id => [`small-${id}`, ['valkey-forge/1.2.0']]));
    else if (path.endsWith('/tasks') || path.endsWith('/events') || path.endsWith('/vms') || path.endsWith('/endpoints') || path.endsWith('/registrations')) json = [];
    else if (path.endsWith('/details')) {
      if (path.includes('cache-2/')) await new Promise(resolve => setTimeout(resolve, 200));
      json = { deployment_name: path.split('/')[2] };
    } else if (path.endsWith('/manifest')) json = { parsed: { name: 'preview', releases: [], instance_groups: [] }, text: 'name: preview' };
    else if (path.endsWith('/logs')) json = { logs: '', total_lines: 0 };
    else if (path.endsWith('/settings')) json = { max_parallel_jobs: 2 };
    await route.fulfill({ json });
  });
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.goto('http://127.0.0.1:4199');
  await page.locator('#blacksmith .service-info-table').waitFor();
  const dialog = page.locator('#navigation-dialog');
  const input = page.locator('#navigation-search');
  const isOpen = () => dialog.evaluate(el => el.open);
  const palette = async query => {
    await page.keyboard.press('Control+k');
    await input.fill(query);
  };
  await page.locator('#navigation-trigger').focus();
  await page.keyboard.press('Meta+k');
  check(await isOpen(), 'Cmd+K opens navigation');
  check(await input.evaluate(el => el === document.activeElement), 'Search gets focus');
  await page.keyboard.press('Shift+Tab');
  await page.keyboard.press('Shift+Tab');
  check(await page.evaluate(() => !!document.activeElement.closest('#navigation-dialog')), 'Focus stays inside dialog');
  await input.focus();
  await input.fill('not-a-real-destination');
  check(await page.locator('#navigation-results [role=option]').count() === 0, 'No-results search clears options');
  check((await dialog.locator('[role=status]').textContent()).includes('No matching'), 'No-results feedback is announced');
  await input.fill('blacksmith');
  await page.keyboard.press('ArrowDown');
  check(await input.getAttribute('aria-activedescendant') === 'navigation-option-1', 'Arrow selection updates active descendant');
  await page.keyboard.press('Escape');
  check(!(await isOpen()), 'Escape closes navigation');
  check(await page.locator('#navigation-trigger').evaluate(el => el === document.activeElement), 'Dismissal restores trigger focus');
  await palette('cache-1 events');
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => !document.getElementById('navigation-dialog').open);
  check(await page.locator('#services .detail-tab.active').getAttribute('data-tab') === 'events', 'Instance destination opens requested section');
  check(await page.locator('#services .service-item.active').getAttribute('data-instance-id') === 'cache-1', 'Instance destination selects correct instance');
  await page.locator('#services .service-item[data-instance-id="cache-3"]').focus();
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => window.currentInstanceInfo.id === 'cache-3');
  check(await page.locator('#services .service-item.active').getAttribute('data-instance-id') === 'cache-3', 'Instance rows support keyboard activation');
  await palette('cache-2 events');
  await page.keyboard.press('Enter');
  await page.keyboard.press('Escape');
  // Wait for the deliberately delayed fixture response, then inspect cancellation.
  await page.waitForTimeout(300);
  check(await page.evaluate(() => window.currentInstanceInfo.id === 'cache-3'), 'Cancelling slow navigation preserves the previous instance');
  await palette('blacksmith plans');
  await page.keyboard.press('Enter');
  check(await page.locator('#blacksmith .detail-tab.active').getAttribute('data-tab') === 'plans', 'Blacksmith section navigation uses existing handler');
  await page.locator('#blacksmith .plan-item').first().click();
  check(await page.locator('#blacksmith .plan-details-table').count() === 1, 'Plan selection opens its details');
  delayCredentials = true;
  await page.locator('#blacksmith .detail-tab[data-tab="details"]').click();
  await page.locator('#blacksmith .detail-tab[data-tab="plans"]').click();
  await page.waitForTimeout(300);
  check(await page.locator('#blacksmith .plan-item').count() === 1, 'Slow previous screen cannot overwrite the selected screen');
  delayCredentials = false;
  await palette('valkey small');
  await page.keyboard.press('Enter');
  check(await page.locator('#blacksmith .plan-details-table').count() === 1, 'Palette opens catalog plan details');
  await page.locator('#blacksmith .detail-tab[data-tab="plans"]').focus();
  await page.keyboard.press('ArrowRight');
  check(await page.locator('#blacksmith .detail-tab.active').getAttribute('data-tab') === 'broker', 'Section tabs support arrow navigation');
  await page.locator('.broker-tab-btn').first().waitFor();
  await palette('marketplace');
  check(await page.locator('#navigation-results [role=option]').count() >= 1, 'Contextual broker screens appear in navigation');
  await page.keyboard.press('Escape');
  await page.evaluate(() => {
    window.serviceInstances['unsafe-label'] = { instance_name: '<img src=x onerror=alert(1)>', credentials: { password: 'NEVER-INDEX-THIS' } };
  });
  await palette('<img');
  check(await page.locator('#navigation-results img').count() === 0, 'Untrusted labels render as text, never markup');
  await input.fill('NEVER-INDEX-THIS');
  check(await page.locator('#navigation-results [role=option]').count() === 0, 'Browser index excludes credential values');
  await page.keyboard.press('Escape');
  await page.evaluate(() => { delete window.serviceInstances['unsafe-label']; });
  await palette('cache-12 events');
  await page.evaluate(() => { window.removedFixtureInstance = window.serviceInstances['cache-12']; delete window.serviceInstances['cache-12']; });
  await page.keyboard.press('Enter');
  check((await dialog.locator('[role=status]').textContent()).includes('no longer available'), 'Removed destination reports a recoverable error');
  await page.keyboard.press('Escape');
  await page.evaluate(() => { window.serviceInstances['cache-12'] = window.removedFixtureInstance; delete window.removedFixtureInstance; });
  await page.evaluate(() => { const terminal = document.createElement('div'); terminal.className = 'xterm'; terminal.innerHTML = '<textarea aria-label="Terminal test input"></textarea>'; document.body.append(terminal); terminal.firstChild.focus(); });
  await page.keyboard.press('Control+k');
  check(!(await isOpen()), 'Terminal keyboard input retains Ctrl+K');
  await page.evaluate(() => document.querySelector('.xterm').remove());
  await palette('blacksmith manifest');
  await page.keyboard.press('Enter');
  await page.locator('#blacksmith .manifest-tab-btn').first().waitFor();
  await palette('yaml');
  await page.keyboard.press('Enter');
  check(await page.locator('#blacksmith .manifest-tab-btn.active').textContent() === 'YAML', 'Palette reaches conditional manifest sub-screens');
  await palette('batch');
  await page.keyboard.press('Enter');
  await page.locator('#upgrade-service-filter').selectOption('valkey');
  await page.locator('#upgrade-release-filter option[value="1.3.0"]').waitFor({ state: 'attached' });
  await page.locator('#upgrade-release-filter').selectOption('1.3.0');
  await page.locator('#upgrade-select-all').click();
  check(await page.locator('#upgrade-start').isEnabled(), 'Release-only upgrade selection remains enabled');
  await page.locator('#upgrade-start').click();
  check(await page.locator('#dialog-modal-overlay.active').count() > 0, 'Upgrade still requires confirmation');
  await page.keyboard.press('Control+k');
  check(!(await isOpen()), 'Palette does not override confirmation dialog');
  await page.keyboard.press('Escape');
  check(writes.length === 0, 'Navigation and cancelled confirmation make no write requests');

  for (const theme of ['light', 'dark']) {
    await page.evaluate(theme => { document.documentElement.setAttribute('data-theme', theme); }, theme);
    await page.locator('.tab-button[data-tab="blacksmith"]').click();
    await page.locator('#blacksmith .detail-tab[data-tab="details"]').click();
    await page.locator('#blacksmith .info-value').first().waitFor();
    const colors = await page.locator('#blacksmith .info-value').first().evaluate(el => ({ color: getComputedStyle(el).color, background: getComputedStyle(el).backgroundColor }));
    check(colors.color !== colors.background, `${theme} details have distinct foreground/background`);
    await page.screenshot({ path: `output/playwright/console-${theme}.png` });
    await palette('certificates');
    await page.screenshot({ path: `output/playwright/palette-${theme}.png` });
    await page.keyboard.press('Escape');
  }
  await page.setViewportSize({ width: 390, height: 844 });
  for (const screen of ['blacksmith', 'services', 'upgrade']) {
    await page.locator(`.tab-button[data-tab="${screen}"]`).click();
    check(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${screen} does not overflow narrow viewport`);
    if (screen === 'services') check(await page.locator('#services .services-items-container').evaluate(el => Math.min(el.getBoundingClientRect().bottom, el.parentElement.getBoundingClientRect().bottom) - el.getBoundingClientRect().top >= 120), 'Mobile instance list retains usable scrolling space');
    await page.screenshot({ path: `output/playwright/mobile-${screen}.png` });
  }
  await page.locator('#upgrade-start').scrollIntoViewIfNeeded();
  check(await page.locator('#upgrade-start').isVisible(), 'Upgrade action remains reachable on narrow screens');
  await page.locator('#upgrade-release-filter').scrollIntoViewIfNeeded();
  check(await page.locator('#upgrade-release-filter').isVisible(), 'Release target remains reachable on narrow screens');
  await palette('cache-12');
  check(await page.locator('#navigation-results [role=option]').count() === 8, 'Full index searchable beyond initial result cap');
  await page.keyboard.press('Escape');
  await page.setViewportSize({ width: 1280, height: 800 });
  check(errors.length === 0, `No uncaught browser errors: ${errors.join('; ')}`);
  return { passed: checks.length, checks, writes };
}
