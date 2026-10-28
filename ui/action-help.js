/* Shared action explanations. Navigation and operations remain owned by their handlers. */
(function (root) {
  'use strict';
  const document = root.document;
  if (!document) return;
  const selector = 'button, a[href], [role="button"], [tabindex][title]';
  let active, tooltip, originalTitle, hoverTimer, closeTimer, observer, dismissed;
  let hoveringControl = false, hoveringTip = false;
  const genericCopy = /^copy (?:to clipboard|content|table data)$/i;
  function label(control) {
    const title = control === active ? originalTitle : control.getAttribute('title') || control.dataset.tooltip;
    const name = control.getAttribute('aria-label');
    return control.dataset.help || (genericCopy.test(title || '') ? name || title : title || name) || '';
  }
  function normalize(control) {
    if (!control.matches?.(selector) || control.hasAttribute('data-context-help') || control.closest('.action-tooltip')) return;
    const value = label(control);
    if (!value) return;
    if (control.dataset.actionHelp !== value) control.dataset.actionHelp = value;
    // Only icon controls get optional visible labels; never duplicate ordinary button text.
    const text = control.textContent.trim();
    const iconOnly = !/[a-z0-9]/i.test(text) && Boolean(control.querySelector('svg') || text);
    if (iconOnly) {
      if (!control.hasAttribute('data-icon-help')) control.setAttribute('data-icon-help', '');
      if (!control.getAttribute('aria-label')) control.setAttribute('aria-label', value);
    }
  }
  function scan(node) {
    if (node.nodeType !== 1 && node.nodeType !== 9) return;
    normalize(node);
    node.querySelectorAll?.(selector).forEach(normalize);
  }
  function describe() {
    if (!active || !tooltip) return;
    const ids = (active.getAttribute('aria-describedby') || '').split(/\s+/).filter(id => id && id !== tooltip.id);
    if (tooltip.textContent !== (active.getAttribute('aria-label') || active.textContent.trim())) ids.push(tooltip.id);
    if (ids.length) active.setAttribute('aria-describedby', ids.join(' '));
    else active.removeAttribute('aria-describedby');
  }
  function hide() {
    clearTimeout(hoverTimer); clearTimeout(closeTimer);
    const old = active;
    active = null;
    if (old && tooltip) {
      const ids = (old.getAttribute('aria-describedby') || '').split(/\s+/).filter(id => id && id !== tooltip.id);
      if (ids.length) old.setAttribute('aria-describedby', ids.join(' ')); else old.removeAttribute('aria-describedby');
    }
    if (old && originalTitle !== null) old.setAttribute('title', originalTitle);
    tooltip?.remove(); tooltip = null; originalTitle = null;
    hoveringControl = false; hoveringTip = false;
  }
  function position() {
    if (!active?.isConnected || !tooltip) return hide();
    const box = active.getBoundingClientRect();
    const tip = tooltip.getBoundingClientRect();
    const viewport = root.visualViewport;
    const left = viewport?.offsetLeft || 0, top = viewport?.offsetTop || 0;
    const width = viewport?.width || root.innerWidth, height = viewport?.height || root.innerHeight;
    tooltip.style.left = `${Math.max(left + 8, Math.min(box.left, left + width - tip.width - 8))}px`;
    const below = box.bottom + 6;
    tooltip.style.top = `${Math.max(top + 8, Math.min(below + tip.height <= top + height - 8 ? below : box.top - tip.height - 6, top + height - tip.height - 8))}px`;
  }
  function show(control) {
    if (control.hasAttribute('data-context-help') || dismissed === control || !control.isConnected || control.closest('[inert]')) return;
    if (active === control) return;
    const text = label(control);
    if (!text) return;
    hide(); active = control; originalTitle = control.getAttribute('title') ?? control.dataset.tooltip ?? null;
    // Avoid native and custom tooltips appearing together. Restore before actions
    // because existing handlers use title to preserve/replace their busy status.
    if (originalTitle !== null) control.removeAttribute('title');
    tooltip = document.createElement('div');
    tooltip.id = 'action-help-tooltip';
    tooltip.className = 'action-tooltip'; tooltip.setAttribute('role', 'tooltip');
    tooltip.textContent = text;
    // An identical accessible name is already announced; do not repeat it as a description.
    // Supplementary help is described while existing field descriptions remain intact.
    const host = control.closest('dialog[open], [role="dialog"], .modal-overlay, .terminal-modal') || document.body;
    host.append(tooltip); describe(); position();
  }
  function maybeHide() {
    clearTimeout(closeTimer);
    closeTimer = setTimeout(() => {
      if (!hoveringControl && !hoveringTip && document.activeElement !== active) hide();
    }, 100);
  }
  function showThemes() {
    hide();
    let dialog = document.getElementById('theme-choice-dialog');
    if (!dialog) {
      dialog = document.createElement('dialog');
      dialog.id = 'theme-choice-dialog'; dialog.className = 'theme-choice-dialog';
      dialog.setAttribute('aria-labelledby', 'theme-choice-title');
      dialog.innerHTML = '<header><h2 id="theme-choice-title">Appearance</h2><button type="button" data-close aria-label="Close appearance" title="Close appearance">×</button></header><p>Choose the theme that is easiest for you to read.</p><div class="theme-choices"></div>';
      for (const [value, name] of [['light','Anvil Light'],['dark','Anvil Dark'],['high-contrast-light','High Contrast Light'],['high-contrast-dark','High Contrast Dark'],['system','System — follow device appearance']]) {
        const button = document.createElement('button'); button.type = 'button';
        button.dataset.themeChoice = value; button.textContent = name;
        button.addEventListener('click', () => { root.blacksmithTheme?.setPreference(value); dialog.close(); root.blacksmithNav?.announce(`${name} selected`); });
        dialog.querySelector('.theme-choices').append(button);
      }
      dialog.querySelector('[data-close]').addEventListener('click', () => dialog.close());
      dialog.addEventListener('close', () => { hide(); document.getElementById('themeToggle')?.focus(); });
      dialog.addEventListener('click', event => { if (event.target === dialog) dialog.close(); });
      document.body.append(dialog);
    }
    let preference = 'system';
    try { preference = root.localStorage.getItem('theme') || 'system'; } catch (_) { /* Storage may be blocked. */ }
    dialog.querySelectorAll('[data-theme-choice]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.themeChoice === preference)));
    dialog.showModal(); root.blacksmithAnnouncers?.watch(dialog); dialog.querySelector('[aria-pressed="true"]')?.focus();
  }
  function addLabelToggle() {
    const content = document.querySelector('#keyboard-help-dialog .keyboard-help-content');
    if (!content || content.querySelector('[data-action-label-toggle]')) return;
    const label = document.createElement('label'); label.className = 'action-label-setting';
    const input = document.createElement('input'); input.type = 'checkbox'; input.dataset.actionLabelToggle = '';
    input.checked = document.body.hasAttribute('data-action-labels');
    input.addEventListener('change', () => document.body.toggleAttribute('data-action-labels', input.checked));
    label.append(input, document.createTextNode(' Show text labels beside icon actions'));
    content.prepend(label);
  }
  function init() {
    if (observer) return;
    document.documentElement.classList.add('blacksmith-action-help');
    scan(document); addLabelToggle();
    observer = new root.MutationObserver(records => {
      for (const record of records) {
        if (record.type === 'childList') record.addedNodes.forEach(scan);
        else {
          if (record.target === active && record.attributeName === 'title' && active.hasAttribute('title')) {
            originalTitle = active.getAttribute('title'); active.removeAttribute('title');
          }
          normalize(record.target);
        }
      }
      if (active && !active.isConnected) hide();
      if (active && tooltip) { const text = label(active); if (tooltip.textContent !== text) tooltip.textContent = text; describe(); position(); }
      addLabelToggle();
    });
    observer.observe(document.body, {subtree:true,childList:true,attributes:true,attributeFilter:['title','aria-label','data-help','data-tooltip']});
    document.addEventListener('focusin', event => { const control = event.target.closest(selector); if (control) show(control); });
    document.addEventListener('focusout', () => { dismissed = null; maybeHide(); });
    document.addEventListener('pointerover', event => {
      if (event.pointerType === 'touch') return;
      if (tooltip?.contains(event.target)) { hoveringTip = true; clearTimeout(closeTimer); return; }
      const control = event.target.closest(selector);
      if (!control || control.contains(event.relatedTarget)) return;
      if (control !== dismissed) { clearTimeout(hoverTimer); hoverTimer = setTimeout(() => { show(control); hoveringControl = active === control; }, 300); }
      if (active === control) { hoveringControl = true; clearTimeout(closeTimer); }
    });
    document.addEventListener('pointerout', event => {
      if (tooltip?.contains(event.target) && !tooltip.contains(event.relatedTarget)) { hoveringTip = false; maybeHide(); }
      const control = event.target.closest(selector);
      if (!control || control.contains(event.relatedTarget)) return;
      clearTimeout(hoverTimer); if (dismissed === control) dismissed = null;
      if (active === control) { hoveringControl = false; maybeHide(); }
    });
    // Capture at window level, before existing document dialog handlers.
    root.addEventListener('keydown', event => {
      if (event.key === 'Escape' && tooltip) { dismissed = active; hide(); event.preventDefault(); event.stopImmediatePropagation(); }
      else if (event.key === 'Enter' || event.key === ' ') hide();
    }, true);
    root.addEventListener('pointerdown', hide, true);
    root.addEventListener('click', hide, true);
    root.addEventListener('resize', hide);
    document.addEventListener('scroll', hide, true);
    document.addEventListener('visibilitychange', () => { if (document.hidden) hide(); });
  }
  root.blacksmithActionHelp = { openThemes: showThemes, hide, scan };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init, {once:true}); else init();
})(globalThis);
