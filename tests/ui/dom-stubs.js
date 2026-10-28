// Stubs for the parts of the browser that jsdom lacks and that Oat and our
// dialogs rely on. Install them on a jsdom window before the page code runs:
//
//   const stubs = require('./dom-stubs.js').install(window);
//   stubs.isOpen(element)    true while a popover is showing
//   stubs.isModal(dialog)    true while a dialog is open as a modal
//   await stubs.settle()     waits for queued toggle and close events
//
// What is stubbed, and how it follows the specification:
//
//   Dialog     showModal, show, and close set and clear the open attribute.
//              showModal on an open dialog throws InvalidStateError. close
//              stores returnValue and queues a close event. Escape on a modal
//              fires a cancelable cancel event and closes the dialog unless it
//              is prevented.
//   Popover    showPopover, hidePopover, and togglePopover fire beforetoggle
//              at once (cancelable when showing) and queue a toggle event
//              that carries oldState and newState. A queued toggle keeps the
//              oldState of the first change, as the specification says.
//              Calling show on a showing popover, or hide on a hidden one,
//              does nothing. Showing an auto popover closes the other auto
//              popovers that do not contain it. A button with popovertarget
//              toggles, shows, or hides its popover on click.
//   Dismiss    Escape closes the topmost popover or modal, and a click
//              outside an auto popover closes it. Pass { dismiss: false } to
//              turn this off. Escape is handled in the capture phase, so it
//              ignores defaultPrevented and stopPropagation, unlike a browser.
//   ARIA       ariaExpanded, ariaSelected, ariaHidden, ariaLabel, and the
//              other string properties reflect to their aria- attributes.
//              Setting null removes the attribute.
//
// Not stubbed: focus moves into a dialog, :popover-open and :modal in
// selectors (use isOpen and isModal), the top layer, and Tab trapping.

const ARIA = ['Atomic', 'AutoComplete', 'Busy', 'Checked', 'ColCount', 'ColIndex', 'ColSpan', 'Current', 'Description', 'Disabled', 'Expanded', 'HasPopup', 'Hidden', 'KeyShortcuts', 'Label', 'Level', 'Live', 'Modal', 'MultiLine', 'MultiSelectable', 'Orientation', 'Placeholder', 'PosInSet', 'Pressed', 'ReadOnly', 'Relevant', 'Required', 'RoleDescription', 'RowCount', 'RowIndex', 'RowSpan', 'Selected', 'SetSize', 'Sort', 'ValueMax', 'ValueMin', 'ValueNow', 'ValueText'];

const kebab = (name) => 'aria-' + name.toLowerCase();

function install(w, { dismiss = true } = {}) {
  const { Element, HTMLElement, document } = w;
  const dialogProto = (w.HTMLDialogElement || HTMLElement).prototype;
  const modals = new Set();
  const popovers = new Map(); // element -> { order }
  const pending = new Map(); // element -> { oldState }
  const tasks = new Set();
  let order = 0;

  const queue = (fn) => {
    const handle = setTimeout(() => { tasks.delete(handle); fn(); }, 0);
    tasks.add(handle);
  };

  ARIA.forEach((name) => {
    const prop = 'aria' + name;
    if (prop in Element.prototype) return;
    Object.defineProperty(Element.prototype, prop, {
      configurable: true,
      get() { return this.getAttribute(kebab(name)); },
      set(value) {
        if (value === null || value === undefined) this.removeAttribute(kebab(name));
        else this.setAttribute(kebab(name), String(value));
      }
    });
  });

  if (!('popover' in HTMLElement.prototype)) {
    Object.defineProperty(HTMLElement.prototype, 'popover', {
      configurable: true,
      get() {
        const value = this.getAttribute('popover');
        if (value === null) return null;
        return value.toLowerCase() === 'manual' ? 'manual' : 'auto';
      },
      set(value) {
        if (value === null) this.removeAttribute('popover');
        else this.setAttribute('popover', String(value));
      }
    });
  }

  const fail = (name, message) => new w.DOMException(message, name);

  // ----- dialog -----

  const open = (dialog, modal) => {
    if (dialog.hasAttribute('open')) return;
    if (modal && !dialog.isConnected) throw fail('InvalidStateError', 'The dialog is not connected.');
    dialog.setAttribute('open', '');
    if (modal) modals.add(dialog);
    dialog.__stubOrder = ++order;
  };
  dialogProto.showModal = function showModal() {
    if (this.hasAttribute('open')) throw fail('InvalidStateError', 'The dialog is already open.');
    open(this, true);
  };
  dialogProto.show = function show() { open(this, false); };
  dialogProto.close = function close(returnValue) {
    if (!this.hasAttribute('open')) return;
    this.removeAttribute('open');
    modals.delete(this);
    if (returnValue !== undefined) this.returnValue = String(returnValue);
    queue(() => this.dispatchEvent(new w.Event('close')));
  };
  if (!('returnValue' in dialogProto)) dialogProto.returnValue = '';

  // ----- popover -----

  const isOpen = (el) => popovers.has(el);
  const toggleEvent = (type, oldState, newState, cancelable) => {
    const event = new w.Event(type, { bubbles: false, cancelable });
    event.oldState = oldState;
    event.newState = newState;
    return event;
  };
  const validate = (el) => {
    if (!el.hasAttribute('popover')) throw fail('NotSupportedError', 'The element has no popover attribute.');
    if (!el.isConnected) throw fail('InvalidStateError', 'The popover is not connected.');
  };
  const queueToggle = (el, oldState, newState) => {
    const earlier = pending.get(el);
    if (earlier) { oldState = earlier.oldState; clearTimeout(earlier.handle); tasks.delete(earlier.handle); }
    const handle = setTimeout(() => {
      tasks.delete(handle);
      pending.delete(el);
      el.dispatchEvent(toggleEvent('toggle', oldState, newState, false));
    }, 0);
    tasks.add(handle);
    pending.set(el, { oldState, handle });
  };
  const hide = (el, fireBefore = true) => {
    if (!isOpen(el)) return;
    if (fireBefore) el.dispatchEvent(toggleEvent('beforetoggle', 'open', 'closed', false));
    if (!isOpen(el)) return;
    popovers.delete(el);
    queueToggle(el, 'open', 'closed');
  };
  const show = (el) => {
    validate(el);
    if (isOpen(el)) return;
    const event = toggleEvent('beforetoggle', 'closed', 'open', true);
    el.dispatchEvent(event);
    if (event.defaultPrevented || isOpen(el)) return;
    if (el.popover === 'auto') {
      [...popovers.keys()].forEach((other) => {
        if (other.popover === 'auto' && !other.contains(el)) hide(other);
      });
    }
    popovers.set(el, { order: ++order });
    queueToggle(el, 'closed', 'open');
  };
  HTMLElement.prototype.showPopover = function showPopover() { show(this); };
  HTMLElement.prototype.hidePopover = function hidePopover() { validate(this); hide(this); };
  HTMLElement.prototype.togglePopover = function togglePopover(force) {
    validate(this);
    const want = force === undefined ? !isOpen(this) : !!force;
    if (want) show(this); else hide(this);
    return isOpen(this);
  };

  // ----- invoker buttons -----

  document.addEventListener('click', (event) => {
    if (event.defaultPrevented) return;
    const button = event.target.closest && event.target.closest('button[popovertarget]');
    if (!button || button.disabled) return;
    const target = document.getElementById(button.getAttribute('popovertarget'));
    if (!target || !target.hasAttribute('popover')) return;
    const action = (button.getAttribute('popovertargetaction') || 'toggle').toLowerCase();
    if (action === 'show') show(target);
    else if (action === 'hide') hide(target);
    else if (isOpen(target)) hide(target); else show(target);
  });

  // ----- dismissal -----

  if (dismiss) {
    document.addEventListener('click', (event) => {
      [...popovers.keys()].forEach((el) => {
        if (el.popover !== 'auto' || el.contains(event.target)) return;
        const invoker = event.target.closest && event.target.closest('button[popovertarget]');
        if (invoker && document.getElementById(invoker.getAttribute('popovertarget')) === el) return;
        hide(el);
      });
    }, true);
    document.addEventListener('keydown', (event) => {
      if (event.key !== 'Escape') return;
      const open = [
        ...[...popovers.entries()].filter(([el]) => el.popover === 'auto').map(([el, v]) => ({ el, order: v.order, popover: true })),
        ...[...modals].map((el) => ({ el, order: el.__stubOrder, popover: false }))
      ].sort((a, b) => b.order - a.order);
      if (!open.length) return;
      const top = open[0];
      if (top.popover) { hide(top.el); return; }
      const cancel = new w.Event('cancel', { cancelable: true });
      top.el.dispatchEvent(cancel);
      if (!cancel.defaultPrevented) top.el.close();
    }, true);
  }

  return {
    isOpen,
    isModal: (dialog) => modals.has(dialog),
    settle: () => new Promise((resolve) => setTimeout(resolve, 5))
  };
}

module.exports = { install };
