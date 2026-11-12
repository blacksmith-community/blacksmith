const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const UI = path.resolve(__dirname, '..', '..', 'ui');
const css = fs.readFileSync(path.join(UI, 'blacksmith.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
const body = (selector) => {
  const out = [];
  css.replace(/([^{}]+)\{([^{}]*)\}/g, (m, sel, rules) => {
    if (sel.split(',').map((s) => s.replace(/\s+/g, ' ').trim()).includes(selector)) out.push(rules);
    return m;
  });
  return out.join(';');
};

const vm = (extra = {}) => Object.assign({
  id: 'abc', job_name: 'node', index: 0, state: 'running', job_state: 'running', az: 'z1', vm_type: 'small',
  ips: ['10.0.0.4'], vm_cid: 'vm-1', agent_id: 'agent-1', bootstrap: true, active: true, dns: ['a.example'],
  vm_created_at: '2026-10-31T10:00:00Z', disk_cids: ['disk-1', 'disk-2'], resurrection_paused: false,
}, extra);

const mounted = (w, d, vms) => {
  const host = d.createElement('div');
  d.body.appendChild(host);
  host.innerHTML = w.__T.formatVMs(vms, 'inst-1');
  return host;
};
const firstThing = (cell) => {
  const box = cell.querySelector('.copy-first');
  expect(box).not.toBeNull();
  return box.firstElementChild;
};

t('the VMs table cells lead with their copy icons', () => {
  const { w, d } = boot();
  try {
    const host = mounted(w, d, [vm()]);
    ['vm-instance', 'vm-ips', 'vm-az'].forEach((cls) => {
      const first = firstThing(host.querySelector('tr.vm-row .' + cls));
      expect(first.tagName).toBe('BUTTON');
      expect(first.classList.contains('copy-btn-inline')).toBe(true);
    });
    expect(host.querySelector('tr.vm-row .vm-instance .copy-first button').getAttribute('aria-label')).toBe('Copy instance name');
    expect(host.querySelector('tr.vm-row .vm-ips .copy-first button').getAttribute('onclick')).toContain('10.0.0.4');
  } finally { w.close(); }
});

t('the VM detail values lead with their copy icons and keep their CIDs whole', () => {
  const { w, d } = boot();
  try {
    const host = mounted(w, d, [vm()]);
    ['vm-cid', 'vm-agent-id', 'vm-type'].forEach((cls) => {
      const dd = host.querySelector('.vm-detail-grid .' + cls);
      expect(firstThing(dd).tagName).toBe('BUTTON');
    });
    const disks = [...host.querySelectorAll('.vm-disk-cids .cid-line')];
    expect(disks.length).toBe(2);
    disks.forEach((line) => expect(firstThing(line).tagName).toBe('BUTTON'));
    expect(host.querySelector('.vm-cid .cid-value').getAttribute('title')).toBe('vm-1');
    expect(host.querySelector('.vm-cid button').getAttribute('onclick')).toContain('vm-1');
  } finally { w.close(); }
});

t('a detail field with no value keeps a slot, and its label is bold and inline', () => {
  const { w, d } = boot();
  try {
    const host = mounted(w, d, [vm({ vm_cid: '', agent_id: '' })]);
    const dd = host.querySelector('.vm-detail-grid .vm-cid');
    expect(dd.querySelector('button')).toBeNull();
    expect(dd.querySelector('.copy-slot')).not.toBeNull();
    expect(dd.querySelector('.copy-slot').nextElementSibling).not.toBeNull();
    expect(body('.vms-table .vm-detail-field')).toMatch(/display:\s*flex/);
    expect(body('.vms-table .vm-detail-field dt')).toMatch(/font-weight:\s*(700|600|bold)/);
    expect(body('.vms-table .vm-detail-field dt')).not.toMatch(/color:\s*var\(--text-secondary\)/);
    expect(body('.vms-table .vm-detail-grid')).toMatch(/gap:\s*0\.25rem 1\.5rem/);
    const forge = fs.readFileSync(path.join(UI, 'forge-theme.css'), 'utf8');
    expect(forge).toMatch(/#services \.detail-content :is\([^)]*\.vm-detail-field[^)]*\) \{ padding: 0; \}/);
  } finally { w.close(); }
});
