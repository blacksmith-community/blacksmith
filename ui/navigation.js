/* Navigation only: existing screen handlers remain responsible for data and actions. */
(function (root) {
  'use strict';
  const instanceSections = [
    ['details', 'Details'], ['events', 'Events'], ['vms', 'VMs'],
    ['manifest', 'Manifest'], ['certificates', 'Certificates'],
    ['credentials', 'Credentials'], ['instance-logs', 'Logs'], ['testing', 'Testing']
  ];
  const aliases = { upgrade: 'upgrade releases stemcells jobs', configs: 'bosh configuration', config: 'settings configuration', vms: 'virtual machines ssh terminal', details: 'overview' };
  function destinations(screens, instances, catalog) {
    const entries = [];
    for (const screen of screens) {
      entries.push({ screen: screen.id, label: screen.label, context: 'Screens', keywords: aliases[screen.id] });
      for (const section of screen.sections || []) {
        entries.push({ screen: screen.id, section: section.id, label: section.label, context: screen.label, keywords: aliases[section.id] });
      }
    }
    (catalog?.services || []).forEach((service, serviceIndex) => {
      (service?.plans || []).forEach((plan, planIndex) => {
        if (plan) entries.push({ screen: 'blacksmith', section: 'plans', planId: plan.id, serviceId: service.id, serviceIndex, planIndex,
          label: plan.name || plan.id || 'Plan', context: `Service Plans · ${service.name || service.id}` });
      });
    });
    for (const [id, instance] of Object.entries(instances || {})) {
      if (!instance) continue;
      const context = [instance.instance_name, id, instance.service_id, instance.deleted ? 'Deleted' : ''].filter(Boolean).join(' · ');
      for (const [section, label] of instanceSections) {
        entries.push({ screen: 'services', instanceId: id, section, label, context, keywords: aliases[section] });
      }
    }
    return entries;
  }
  function search(entries, query) {
    const normalized = query.trim().toLocaleLowerCase();
    const words = normalized.split(/\s+/);
    const score = entry => {
      const label = entry.label.toLocaleLowerCase();
      return label === normalized ? 0 : label.startsWith(normalized) ? 1 : 2;
    };
    return entries.filter(entry => words.every(word => `${entry.label} ${entry.context || ''} ${entry.keywords || ''}`.toLocaleLowerCase().includes(word)))
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

    function inventory() {
      const screens = [...document.querySelectorAll('.tabs > .tab-button')].map(button => ({
        id: button.dataset.tab, label: button.textContent.trim(),
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
        option.append(label, context);
        option.addEventListener('click', () => navigate(entry));
        list.append(option);
      });
      status.textContent = !matches.length ? 'No matching screens. Try a screen name or instance ID.' :
        matches.length > 80 ? `${matches.length} destinations. Showing the first 80; type to narrow results.` : `${matches.length} destinations`;
      paintSelection();
    }
    function blocked() {
      return [...document.querySelectorAll('dialog[open], .modal, .modal-overlay, .dialog-modal-overlay.active, [role="dialog"], .terminal-modal')]
        .some(el => el !== dialog && el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden' && getComputedStyle(el).display !== 'none') ||
        document.activeElement?.closest('.xterm, #terminal-modal');
    }
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
        target.focus();
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
      const main = mainButton(panel.id)?.textContent.trim() || 'Blacksmith';
      const section = panel.querySelector('.detail-tab.active')?.textContent.trim();
      const instance = panel.id === 'services' ? root.currentInstanceInfo?.id : '';
      document.getElementById('console-title').textContent = [main, instance, section].filter(Boolean).join(' / ');
      document.querySelectorAll('.detail-tabs').forEach(group => {
        group.setAttribute('role', 'tablist');
        group.setAttribute('aria-label', group.closest('#services') ? 'Instance sections' : 'Blacksmith sections');
        group.querySelectorAll('.detail-tab').forEach(tab => {
          tab.setAttribute('role', 'tab');
          tab.setAttribute('aria-selected', String(tab.classList.contains('active')));
        });
      });
      document.querySelectorAll('.service-item, .plan-item, .endpoint-item').forEach(item => {
        item.tabIndex = 0;
        item.setAttribute('role', 'button');
        item.setAttribute('aria-pressed', String(item.classList.contains('active')));
      });
    }
    document.addEventListener('click', event => {
      if (event.target.closest('.tab-button, .detail-tab, .service-item')) queueMicrotask(updateContext);
    });
    document.addEventListener('keydown', event => {
      const item = event.target.closest('.service-item, .plan-item, .endpoint-item');
      if (item && event.target === item && ['Enter', ' '].includes(event.key)) { event.preventDefault(); item.click(); }
      const tab = event.target.closest('.tab-button, .detail-tab');
      if (tab && ['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) {
        const siblings = [...tab.parentElement.querySelectorAll('button')];
        let index = siblings.indexOf(tab);
        index = event.key === 'Home' ? 0 : event.key === 'End' ? siblings.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + siblings.length) % siblings.length;
        event.preventDefault(); siblings[index].focus(); siblings[index].click();
      }
    });
    new MutationObserver(updateContext).observe(document.querySelector('.tab-content'), { childList: true, subtree: true });
    updateContext();
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})(globalThis);
