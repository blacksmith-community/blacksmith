/* Navigation only: existing screen handlers remain responsible for data and actions. */
(function (root) {
  'use strict';
  const instanceSections = [
    ['details', 'Overview'], ['events', 'Events'], ['vms', 'Virtual machines'],
    ['manifest', 'Manifest'], ['certificates', 'Certificates'],
    ['credentials', 'Credentials'], ['instance-logs', 'Logs'], ['testing', 'Connection tests']
  ];
  const aliases = { upgrade: 'upgrade releases stemcells jobs', configs: 'bosh configuration', config: 'settings configuration', vms: 'vms virtual machines ssh terminal', details: 'details overview context', testing: 'testing connection tests' };
  function destinations(screens, instances, catalog) {
    const entries = [];
    for (const screen of screens) {
      entries.push({ screen: screen.id, label: screen.label, context: 'Screens', type: 'Screen', breadcrumb: screen.label, keywords: aliases[screen.id] });
      for (const section of screen.sections || []) {
        entries.push({ screen: screen.id, section: section.id, label: section.label, context: screen.label, type: 'Section', breadcrumb: screen.label + ' > ' + section.label, keywords: aliases[section.id] });
      }
    }
    (catalog?.services || []).forEach((service, serviceIndex) => {
      (service?.plans || []).forEach((plan, planIndex) => {
        if (plan) entries.push({ screen: 'blacksmith', section: 'plans', planId: plan.id, serviceId: service.id, serviceIndex, planIndex,
          label: plan.name || plan.id || 'Plan', context: `Service Plans · ${service.name || service.id}`, type: 'Plan', breadcrumb: `Blacksmith Broker > Service plans > ${service.name || service.id}` });
      });
    });
    for (const [id, instance] of Object.entries(instances || {})) {
      if (!instance) continue;
      const context = [instance.instance_name, id, instance.service_id, instance.deleted ? 'Deleted' : ''].filter(Boolean).join(' · ');
      for (const [section, label] of instanceSections) {
        entries.push({ screen: 'services', instanceId: id, section, label, context, type: 'Instance', breadcrumb: `Service Instances > ${instance.instance_name || id} > ${label}`, detail: [id, instance.service_id, instance.deleted ? 'Deleted' : ''].filter(Boolean).join(' · '), keywords: aliases[section] });
      }
    }
    [['upgrade', 'Upgrade'], ['converge', 'Converge'], ['rotate', 'Rotate certificates']].forEach(([operation, label]) =>
      entries.push({ screen: 'upgrade', operation, label, type: 'Operation', context: 'Batch Operations', breadcrumb: 'Batch Operations > Options', keywords: operation }));
    [
      ['Redeploy', {mode:'redeploy'}],
      ['Patch in place', {mode:'patch'}],
      ['Patch in place · Leave unconverged', {mode:'patch',converge:'pending'}],
      ['Patch in place · Converge right away', {mode:'patch',converge:'immediate'}]
    ].forEach(([choice,rotation]) => entries.push({screen:'upgrade',operation:'rotate',rotation,
      label:`Rotate certificates · ${choice}`,type:'Operation',context:'Batch Operations',
      breadcrumb:'Batch Operations > Options',keywords:rotation.converge === 'immediate' ? 'converged' : ''}));
    [['light','Anvil Light'], ['dark','Anvil Dark'], ['high-contrast-light','High Contrast Light'], ['high-contrast-dark','High Contrast Dark'], ['system','System']].forEach(([theme,label]) => entries.push({ theme, label, type: 'Theme', context: 'Appearance theme mode', breadcrumb: 'Appearance > Theme', detail: theme === 'system' ? 'Follow your device’s appearance with Anvil' : '', keywords: 'theme appearance color mode' }));
    return entries;
  }
  function search(entries, query) {
    const normalized = query.trim().toLocaleLowerCase();
    const words = normalized.split(/\s+/);
    const score = entry => {
      const label = entry.label.toLocaleLowerCase();
      return label === normalized ? 0 : label.startsWith(normalized) ? 1 : 2;
    };
    return entries.filter(entry => {
      const haystack = `${entry.label} ${entry.context || ''} ${entry.breadcrumb || ''} ${entry.keywords || ''}`.toLocaleLowerCase();
      // Prefix matching keeps 'con' from also matching 'unconverged'.
      return entry.rotation ? words.every(word => haystack.split(/[^a-z0-9]+/).some(token => token.startsWith(word))) : words.every(word => haystack.includes(word));
    })
      .sort((a, b) => score(a) - score(b));
  }
  root.BlacksmithNavigation = { destinations, search };
  if (!root.document) return;
  const document = root.document;

  function init() {
    const dialog = document.getElementById('navigation-dialog');
    const trigger = document.getElementById('navigation-trigger');
    if (!dialog || !trigger) return;
    const input = dialog.querySelector('input');
    const list = dialog.querySelector('[role="listbox"]');
    const status = dialog.querySelector('[role="status"]');
    let entries = [], results = [], selected = 0, previousFocus;
    let navigationController;
    const mainButton = id => [...document.querySelectorAll('.tabs > .tab-button')].find(b => b.dataset.tab === id);
    const sectionButton = (screen, id) => [...document.getElementById(screen).querySelectorAll('.detail-tab')].find(b => b.dataset.tab === id);
    const shortcut = /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘ K' : 'Ctrl K';
    trigger.querySelector('kbd').textContent = shortcut;

    const buttonLabel = button => [...button.childNodes].filter(node => node.nodeType === 3).map(node => node.textContent).join('').trim();
    function inventory() {
      const screens = [...document.querySelectorAll('.tabs > .tab-button')].map(button => ({
        id: button.dataset.tab, label: buttonLabel(button),
        sections: button.dataset.tab === 'blacksmith' ? [...document.querySelectorAll('#blacksmith .detail-tab')].map(b => ({ id: b.dataset.tab, label: b.textContent.trim() })) : []
      }));
      const all = destinations(screens, root.serviceInstances, root.plansData);
      // Conditional sub-screens are indexed only while their parent view exists.
      document.querySelectorAll('.tab-panel.active .manifest-tab-btn, .tab-panel.active .instance-log-job-tab, .tab-panel.active .operation-tab, .tab-panel.active .broker-tab-btn, .tab-panel.active .endpoint-item').forEach(button => {
        if (!button.disabled && button.getClientRects().length) {
          all.push({ label: button.textContent.trim(), context: `Current screen · ${document.getElementById('console-title').textContent}`, button });
        }
      });
      return all;
    }
    function paintSelection() {
      [...list.children].forEach((option, index) => option.setAttribute('aria-selected', String(index === selected)));
      const active = list.children[selected];
      if (active) {
        input.setAttribute('aria-activedescendant', active.id);
        active.scrollIntoView({ block: 'nearest' });
      } else input.removeAttribute('aria-activedescendant');
    }
    function render() {
      const matches = search(entries, input.value);
      results = matches.slice(0, 80);
      selected = 0;
      list.replaceChildren();
      results.forEach((entry, index) => {
        const option = document.createElement('div');
        option.id = `navigation-option-${index}`;
        option.setAttribute('role', 'option');
        const label = document.createElement('strong');
        label.textContent = entry.label;
        const context = document.createElement('span');
        context.textContent = entry.context;
        const row = document.createElement('div');
        row.className = 'navigation-result-row';
        const type = document.createElement('span');
        type.className = 'navigation-result-type';
        type.textContent = entry.type || 'Section';
        context.className = 'navigation-result-location';
        context.textContent = entry.breadcrumb || entry.context;
        row.append(label, type, context);
        option.append(row);
        if (entry.detail) {
          const detail = document.createElement('span');
          detail.className = 'navigation-result-detail';
          detail.textContent = entry.detail;
          option.append(detail);
        }
        option.addEventListener('click', () => navigate(entry));
        list.append(option);
      });
      status.textContent = !matches.length ? 'No matches. Try a screen, instance, or theme.' :
        matches.length > 80 ? `${matches.length} matches. Showing the first 80; type to narrow results.` : `${matches.length} matches`;
      paintSelection();
    }
    function blocked() {
      return [...document.querySelectorAll('dialog[open], .modal, .modal-overlay, .dialog-modal-overlay.active, [role="dialog"], .terminal-modal')]
        .some(el => el !== dialog && el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden' && getComputedStyle(el).display !== 'none') ||
        document.activeElement?.closest('.xterm, #terminal-modal');
    }
    const helpDialog = document.getElementById('keyboard-help-dialog');
    const helpTrigger = document.getElementById('keyboard-help-trigger');
    let helpOpener;
    function showKeyboardHelp() {
      if (!helpDialog || blocked()) return;
      helpOpener = document.activeElement;
      const content = helpDialog.querySelector('.keyboard-help-content');
      content.replaceChildren();
      const section = (title, rows) => {
        const heading = document.createElement('h3'); heading.textContent = title;
        const table = document.createElement('table');
        const body = document.createElement('tbody');
        rows.forEach(([keys, description]) => {
          const row = document.createElement('tr');
          const key = document.createElement('th'); key.scope = 'row';
          const binding = document.createElement('kbd'); binding.textContent = keys; key.append(binding);
          const text = document.createElement('td'); text.textContent = description;
          row.append(key,text); body.append(row);
        });
        table.append(body); content.append(heading,table);
      };
      section('Global', [
        [shortcut, 'Quick Navigation'],
        [shortcut.replace('K', '/'), 'Keyboard shortcuts'],
        ['Tab / Shift Tab', 'Move between controls'],
        ['Enter / Space', 'Activate the focused control'],
        ['Esc', 'Close the current dialog']
      ]);
      const panel = document.querySelector('.tab-panel.active');
      const rows = [['Arrow keys / Home / End', 'Move between focused navigation tabs'],
        ['Arrow keys', 'Choose radio, mode, or select options']];
      const names = {'batch-new':'New Operation (keeps your draft)', 'batch-activity':'Activity',
        'batch-flow-next':'Next step; on Review, focus confirmation', 'batch-flow-back':'Previous step',
        'rotate-op-upgrade':'Upgrade options', 'rotate-op-rotate':'Rotate certificate options', 'rotate-op-converge':'Converge options'};
      panel?.querySelectorAll('[aria-keyshortcuts]').forEach(control => {
        if (!names[control.id] && (!control.getClientRects().length || control.disabled)) return;
        rows.push([control.getAttribute('aria-keyshortcuts').replaceAll('Alt','Alt/Option'),
          names[control.id] || control.getAttribute('aria-label') || control.getAttribute('placeholder') || control.textContent.trim()]);
      });
      section(document.getElementById('console-title').textContent || 'Current view', rows);
      const note = document.createElement('p');
      note.textContent = 'Batch shortcuts pause while typing or in a dialog. Final confirmation requires explicit activation.';
      if (panel?.id === 'upgrade') content.append(note);
      helpDialog.showModal();
      helpDialog.querySelector('[data-close]').focus();
    }
    helpTrigger?.addEventListener('click', showKeyboardHelp);
    helpDialog?.querySelector('[data-close]').addEventListener('click', () => helpDialog.close());
    helpDialog?.addEventListener('close', () => { if (helpOpener?.isConnected) helpOpener.focus(); });
    helpDialog?.addEventListener('click', event => { if (event.target === helpDialog) helpDialog.close(); });
    helpTrigger?.setAttribute('aria-keyshortcuts', /Mac|iPhone|iPad/.test(navigator.platform) ? 'Meta+/' : 'Control+/');
    trigger.setAttribute('aria-keyshortcuts', /Mac|iPhone|iPad/.test(navigator.platform) ? 'Meta+k' : 'Control+k');

    function open() {
      if (dialog.open || blocked()) return;
      previousFocus = document.activeElement;
      input.disabled = false;
      entries = inventory();
      input.value = '';
      dialog.showModal();
      input.setAttribute('aria-expanded', 'true');
      render();
      input.focus();
    }
    function close(restore = true) {
      navigationController?.abort();
      dialog.close();
      input.setAttribute('aria-expanded', 'false');
      if (restore && previousFocus?.isConnected) previousFocus.focus();
    }
    async function navigate(entry) {
      if (input.disabled) return;
      const controller = new AbortController();
      navigationController = controller;
      try {
        if (entry.operation) {
          if (!root.blacksmithBatch?.openOperation) throw new Error('Batch Operations is still loading. Please try again.');
          input.disabled = true;
          status.textContent = 'Opening operation options…';
          const opened = await root.blacksmithBatch.openOperation(entry.operation, controller.signal, entry.rotation);
          if (controller.signal.aborted || !dialog.open) return;
          if (!opened) throw new Error('Cannot open this operation right now. Please try again.');
          close(false);
          document.getElementById('batch-flow-title')?.focus({ preventScroll: true });
          updateContext();
          return;
        }
        if (entry.theme) {
          if (!root.blacksmithTheme) throw new Error('Theme controls are still loading. Please try again.');
          root.blacksmithTheme.setPreference(entry.theme);
          close();
          return;
        }
        let target;
        if (entry.button) {
          if (!entry.button.isConnected) throw new Error('This screen is no longer available. Close and reopen navigation.');
          target = entry.button;
        } else if (entry.instanceId) {
          if (!root.navigateToInstance) throw new Error('Instances are still loading. Please try again.');
          // Keep the modal open while the existing selection handler resolves.
          input.disabled = true;
          status.textContent = 'Opening instance…';
          const opened = await root.navigateToInstance(entry.instanceId, controller.signal);
          if (controller.signal.aborted || !dialog.open) return;
          if (!opened) throw new Error('This instance is no longer available. Close and reopen navigation.');
          mainButton('services').click();
          target = sectionButton('services', entry.section);
        } else {
          target = mainButton(entry.screen);
          if (entry.section) {
            target.click();
            target = sectionButton(entry.screen, entry.section);
          }
          if (entry.planId !== undefined) {
            target?.click();
            const serviceIndex = (root.plansData?.services || []).findIndex(service => service.id === entry.serviceId);
            const planIndex = root.plansData?.services?.[serviceIndex]?.plans?.findIndex(plan => plan.id === entry.planId);
            target = [...document.querySelectorAll('#blacksmith .plan-item')].find(item =>
              item.dataset.serviceIndex === String(serviceIndex) && item.dataset.planIndex === String(planIndex));
          }
        }
        if (!target || target.disabled) throw new Error('This screen is not available yet. Please try again.');
        close(false);
        target.click();
        (target.getClientRects().length ? target : target.closest('.entity-workspace')?.querySelector('.entity-section-select'))?.focus();
        updateContext();
      } catch (error) {
        status.textContent = error.message;
      } finally {
        if (navigationController === controller) {
          input.disabled = false;
          if (dialog.open) input.focus();
        }
      }
    }
    trigger.addEventListener('click', open);
    input.addEventListener('input', render);
    dialog.querySelector('[data-close]').addEventListener('click', () => close());
    dialog.addEventListener('cancel', event => { event.preventDefault(); close(); });
    dialog.addEventListener('click', event => { if (event.target === dialog) close(); });
    document.addEventListener('keydown', event => {
      if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey &&
          (event.key === '/' || event.code === 'Slash') && !event.repeat && !event.isComposing) {
        if (!blocked()) { event.preventDefault(); showKeyboardHelp(); }
        return;
      }
      if (dialog.open) {
        event.stopImmediatePropagation();
        if (event.key === 'Tab') {
          const controls = [...dialog.querySelectorAll('button, input')].filter(el => !el.disabled);
          const current = controls.indexOf(document.activeElement);
          event.preventDefault();
          controls[(current + (event.shiftKey ? -1 : 1) + controls.length) % controls.length].focus();
        } else if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
          event.preventDefault();
          input.focus();
        } else if (event.key === 'Escape') { event.preventDefault(); close(); }
        else if (!input.disabled && ['ArrowDown', 'ArrowUp'].includes(event.key) && document.activeElement === input) {
          event.preventDefault();
          selected = results.length ? (selected + (event.key === 'ArrowDown' ? 1 : -1) + results.length) % results.length : 0;
          paintSelection();
        } else if (event.key === 'Enter' && document.activeElement === input && results[selected]) {
          event.preventDefault(); navigate(results[selected]);
        }
        return;
      }
      if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === 'k' && !event.isComposing && !event.repeat && !blocked()) {
        event.preventDefault(); open();
      }
    }, true);

    function updateContext() {
      const panel = document.querySelector('.tab-panel.active');
      if (!panel) return;
      const main = (mainButton(panel.id) ? buttonLabel(mainButton(panel.id)) : '') || 'Blacksmith';
      const detailVisible = panel.id !== 'services' || panel.classList.contains('instance-open');
      const section = detailVisible ? panel.querySelector('.detail-tab.active')?.textContent.trim() : '';
      const instance = panel.id === 'services' && detailVisible ? (root.currentInstanceInfo?.name || root.currentInstanceInfo?.id) : '';
      document.getElementById('console-title').textContent = [main, instance, section].filter(Boolean).join(' / ');
      document.querySelectorAll('.detail-tabs').forEach(group => {
        group.setAttribute('role', 'tablist');
        group.setAttribute('aria-orientation', group.classList.contains('entity-navigation') ? 'vertical' : 'horizontal');
        group.setAttribute('aria-label', group.closest('#services') ? 'Instance sections' : 'Blacksmith sections');
        group.querySelectorAll('.detail-tab').forEach(tab => {
          tab.setAttribute('role', 'tab');
          const selected = tab.classList.contains('active');
          tab.setAttribute('aria-selected', String(selected));
          tab.tabIndex = selected ? 0 : -1;
          const entity = group.closest('#blacksmith, .service-detail');
          const content = entity?.querySelector('.detail-content');
          tab.id ||= `${group.closest('#services') ? 'instance' : 'broker'}-section-${tab.dataset.tab}`;
          if (content) {
            content.id ||= `${group.closest('#services') ? 'instance' : 'broker'}-section-content`;
            tab.setAttribute('aria-controls', content.id);
            if (selected) {
              content.setAttribute('role', 'tabpanel');
              content.setAttribute('aria-labelledby', tab.id);
              content.removeAttribute('aria-label');
              content.tabIndex = 0;
              const heading = entity.querySelector('.entity-current-title');
              if (heading && heading.textContent !== tab.textContent) heading.textContent = tab.textContent;
              const select = entity.querySelector('.entity-section-select');
              if (select) select.value = tab.dataset.tab;
              const hint = entity.querySelector('.entity-read-hint');
              if (hint) hint.hidden = tab.dataset.tab !== 'details';
            }
          }
        });
      });
      document.querySelectorAll('.service-item, .plan-item, .endpoint-item').forEach(item => {
        item.tabIndex = 0;
        item.setAttribute('role', 'button');
        item.setAttribute('aria-pressed', String(item.classList.contains('active') || item.classList.contains('selected')));
      });
    }
    document.addEventListener('click', event => {
      if (event.target.closest('.tab-button, .detail-tab, .service-item, .entity-back')) queueMicrotask(updateContext);
    });
    document.addEventListener('keydown', event => {
      const item = event.target.closest('.service-item, .plan-item, .endpoint-item');
      if (item && event.target === item && ['Enter', ' '].includes(event.key)) { event.preventDefault(); item.click(); }
      const tab = event.target.closest('.tab-button, .detail-tab');
      if (tab && ['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) {
        const siblings = [...(tab.closest('.detail-tabs') || tab.parentElement).querySelectorAll('.detail-tab, .tab-button')];
        let index = siblings.indexOf(tab);
        index = event.key === 'Home' ? 0 : event.key === 'End' ? siblings.length - 1 : (index + (['ArrowRight', 'ArrowDown'].includes(event.key) ? 1 : -1) + siblings.length) % siblings.length;
        event.preventDefault(); siblings[index].focus(); siblings[index].click();
      }
    });
    new MutationObserver(updateContext).observe(document.querySelector('.tab-content'), { childList: true, subtree: true });
    updateContext();
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})(globalThis);
