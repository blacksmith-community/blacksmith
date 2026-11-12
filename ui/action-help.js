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
    const viewport = root.visualViewport;
    const left = viewport?.offsetLeft || 0, top = viewport?.offsetTop || 0;
    const width = viewport?.width || root.innerWidth, height = viewport?.height || root.innerHeight;
    // A dialog clips what overflows it, so a tip inside one stays within the
    // dialog and the viewport on both axes.
    const hostBox = tooltip.parentElement && tooltip.parentElement !== document.body ? tooltip.parentElement.getBoundingClientRect() : null;
    const hosted = hostBox && hostBox.width > 0;
    const minX = Math.max(left, hosted ? hostBox.left : left), maxX = Math.min(left + width, hosted ? hostBox.right : left + width);
    const minY = Math.max(top, hosted ? hostBox.top : top), maxY = Math.min(top + height, hosted ? hostBox.bottom : top + height);
    // Cap the width to the room there is, then measure, so the text wraps
    // rather than running past the edge.
    tooltip.style.maxWidth = `${Math.max(80, Math.min(384, maxX - minX - 16))}px`;
    const tip = tooltip.getBoundingClientRect();
    const fit = (size) => {
      const wantLeft = Math.max(minX + 8, Math.min(box.left, maxX - size.width - 8));
      const below = box.bottom + 6, above = box.top - size.height - 6;
      const preferred = below + size.height <= maxY - 8 ? below : above >= minY + 8 ? above : below;
      const wantTop = Math.max(minY + 8, Math.min(preferred, maxY - size.height - 8));
      return [wantLeft, wantTop];
    };
    let [setLeft, setTop] = fit(tip);
    tooltip.style.left = `${setLeft}px`; tooltip.style.top = `${setTop}px`;
    // A dialog with a transform or containment becomes the containing block for
    // fixed children, so check where the tip really landed and correct for it,
    // using its settled size.
    if (tip.width > 0) {
      for (let pass = 0; pass < 3; pass++) {
        const placed = tooltip.getBoundingClientRect();
        const [wantLeft, wantTop] = fit(placed);
        const dx = wantLeft - placed.left, dy = wantTop - placed.top;
        if (Math.abs(dx) <= 0.5 && Math.abs(dy) <= 0.5) break;
        setLeft += dx; setTop += dy;
        tooltip.style.left = `${setLeft}px`; tooltip.style.top = `${setTop}px`;
      }
    }
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
    const host = control.closest('dialog[open], [role="dialog"], .terminal-modal') || document.body;
    // Measure from the left edge so the control's own position cannot squeeze the tip.
    tooltip.style.left = '0px'; tooltip.style.top = '0px';
    host.append(tooltip); describe(); position();
    // A dialog that is still opening moves its controls, so place the tip again
    // once the motion has settled.
    for (const delay of [120, 350]) setTimeout(() => { if (active === control) position(); }, delay);
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
      dialog.innerHTML = '<header><h2 id="theme-choice-title">Appearance</h2><button type="button" class="dialog-close" data-close aria-label="Close appearance" title="Close appearance">' + root.blacksmithIcon('x', { size: 18 }) + '</button></header><p>Choose the theme that is easiest for you to read.</p><div class="theme-choices"></div>';
      for (const [value, name] of [['light','Anvil Light'],['dark','Anvil Dark'],['high-contrast-light','High Contrast Light'],['high-contrast-dark','High Contrast Dark'],['system','System — follow device appearance']]) {
        const button = document.createElement('button'); button.type = 'button';
        button.dataset.themeChoice = value; button.textContent = name;
        button.addEventListener('click', () => { root.blacksmithTheme?.setPreference(value); dialog.close(); root.blacksmithNav?.announce(`${name} selected`); });
        dialog.querySelector('.theme-choices').append(button);
      }
      // The text-label setting sits under the theme choices. It is a body
      // attribute, off until someone turns it on.
      const labelSetting = document.createElement('label'); labelSetting.className = 'action-label-setting';
      const labelInput = document.createElement('input'); labelInput.type = 'checkbox'; labelInput.dataset.actionLabelToggle = '';
      labelInput.addEventListener('change', () => document.body.toggleAttribute('data-action-labels', labelInput.checked));
      labelSetting.append(labelInput, document.createTextNode(' Show text labels beside icon actions'));
      dialog.append(labelSetting);
      dialog.querySelector('[data-close]').addEventListener('click', () => dialog.close());
      dialog.addEventListener('close', () => { hide(); document.getElementById('themeToggle')?.focus(); });
      dialog.addEventListener('click', event => { if (event.target === dialog) dialog.close(); });
      document.body.append(dialog);
    }
    let preference = 'system';
    try { preference = root.localStorage.getItem('theme') || 'system'; } catch (_) { /* Storage may be blocked. */ }
    dialog.querySelectorAll('[data-theme-choice]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.themeChoice === preference)));
    dialog.querySelector('[data-action-label-toggle]').checked = document.body.hasAttribute('data-action-labels');
    dialog.showModal(); root.blacksmithAnnouncers?.watch(dialog); dialog.querySelector('[aria-pressed="true"]')?.focus();
  }
  function init() {
    if (observer) return;
    document.documentElement.classList.add('blacksmith-action-help');
    scan(document);
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
    });
    observer.observe(document.body, {subtree:true,childList:true,attributes:true,attributeFilter:['title','aria-label','data-help','data-tooltip']});
    // Focus that code sets as a dialog opens is not the operator asking for
    // help, so it shows nothing. Focus that matches :focus-visible, which is
    // keyboard focus, does show it, as long as it is not that opening focus.
    const DIALOG_OPEN_QUIET_MS = 250;
    const dialogProto = root.HTMLDialogElement && root.HTMLDialogElement.prototype;
    const stampOpen = proto => {
      const original = proto && Object.prototype.hasOwnProperty.call(proto, 'showModal') ? proto.showModal : null;
      if (typeof original !== 'function' || original.stampsOpen) return;
      const wrapped = function (...args) { this.helpOpenedAt = Date.now(); return original.apply(this, args); };
      wrapped.stampsOpen = true;
      proto.showModal = wrapped;
    };
    stampOpen(dialogProto); stampOpen(root.HTMLElement && root.HTMLElement.prototype);
    const visibleFocus = target => { try { return target.matches(':focus-visible'); } catch (error) { return true; } };
    const openingFocus = target => { const dialog = target.closest('dialog'); return Boolean(dialog && dialog.helpOpenedAt && Date.now() - dialog.helpOpenedAt < DIALOG_OPEN_QUIET_MS); };
    document.addEventListener('focusin', event => { const control = event.target.closest(selector); if (control && visibleFocus(event.target) && !openingFocus(event.target)) show(control); });
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
