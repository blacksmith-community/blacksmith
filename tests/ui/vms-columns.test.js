const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');

const vm = (extra = {}) => Object.assign({
  id: 'abc', job_name: 'blacksmith', index: 0, state: 'running', job_state: 'running', az: 'z1', vm_type: 'small',
  ips: ['10.0.0.4'], vm_cid: 'vm-1', agent_id: 'agent-1', bootstrap: true, active: true, dns: ['a.example'],
  vm_created_at: '2026-10-31T10:00:00Z', disk_cids: ['disk-1'], resurrection_paused: false
}, extra);

const render = (w, d, vms, instanceId) => {
  const host = d.createElement('div');
  d.body.appendChild(host);
  host.innerHTML = w.__T.formatVMs(vms, instanceId);
  return host;
};

for (const [name, instanceId] of [['broker VMs', null], ['instance VMs', 'inst-1']]) {
  test(`${name} show six columns by default`, () => {
    const { w, d } = boot();
    try {
      const host = render(w, d, [vm()], instanceId);
      const heads = [...host.querySelectorAll('.vms-table thead th')].map((th) => th.textContent.trim());
      expect(heads).toEqual(['Instance', 'VM state', 'Job state', 'IPs', 'AZ', 'Actions']);
      const row = host.querySelector('tr.vm-row');
      expect(row.cells).toHaveLength(6);
      expect(row.querySelector('.ssh-btn')).not.toBeNull();
    } finally { w.close(); }
  });

  test(`${name} keep the other fields in a hidden detail row grid`, () => {
    const { w, d } = boot();
    try {
      const host = render(w, d, [vm()], instanceId);
      const detail = host.querySelector('tr.row-detail');
      expect(detail.hidden).toBe(true);
      expect(detail.querySelector('td').colSpan).toBe(6);
      const labels = [...detail.querySelectorAll('dt')].map((dt) => dt.textContent);
      expect(labels).toEqual(['VM CID', 'Agent ID', 'Bootstrap', 'VM type', 'Active', 'DNS', 'Created at', 'Disk CIDs', 'Resurrection']);
      expect(detail.querySelector('.vm-cid .cid-value').textContent).toBe('vm-1');
      expect(detail.querySelector('.vm-cid button.copy-btn-inline').getAttribute('onclick')).toContain('vm-1');
      expect(detail.querySelector('.vm-agent-id button.copy-btn-inline')).not.toBeNull();
      expect(detail.querySelector('.vm-type button.copy-btn-inline')).not.toBeNull();
      expect(detail.querySelector('.vm-disk-cids button.copy-btn-inline')).not.toBeNull();
    } finally { w.close(); }
  });
}

test('the row toggle opens and closes its detail row and names its state', () => {
  const { w, d } = boot();
  try {
    const host = render(w, d, [vm()], 'inst-1');
    const toggle = host.querySelector('.vm-expand');
    const detail = host.querySelector('tr.row-detail');
    expect(toggle.getAttribute('aria-controls')).toBe(detail.id);
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(toggle.getAttribute('aria-label')).toBe('Show details for blacksmith/0');
    toggle.click();
    expect(detail.hidden).toBe(false);
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(toggle.getAttribute('aria-label')).toBe('Hide details for blacksmith/0');
    toggle.click();
    expect(detail.hidden).toBe(true);
  } finally { w.close(); }
});

test('sorting keeps each detail row under its VM and keeps the Actions column', () => {
  const { w, d } = boot();
  try {
    const host = render(w, d, [vm({ id: 'b', index: 1, vm_cid: 'vm-b' }), vm({ id: 'a', index: 0, vm_cid: 'vm-a' })], 'inst-1');
    w.__T.initializeSorting('vms-table');
    host.querySelector('.vms-table thead th').click();
    const rows = [...host.querySelectorAll('.vms-table tbody tr')];
    expect(rows.map((r) => r.className)).toEqual(['vm-row', 'row-detail', 'vm-row', 'row-detail']);
    expect(rows[0].cells).toHaveLength(6);
    expect(rows[1].querySelector('.vm-cid').textContent).toContain('vm-');
    expect(host.querySelector('.vms-table thead th:last-child').getAttribute('aria-sort')).toBeNull();
  } finally { w.close(); }
});

test('search matches a detail value and hides the detail row with its VM', () => {
  const { w, d } = boot();
  try {
    const host = render(w, d, [vm({ vm_cid: 'cid-needle' }), vm({ id: 'x', index: 1, vm_cid: 'other' })], 'inst-1');
    w.__T.filterTableRows('vms-table', 'needle');
    const rows = [...host.querySelectorAll('.vms-table tbody tr')];
    expect(rows.map((r) => r.style.display)).toEqual(['', '', 'none', 'none']);
  } finally { w.close(); }
});

test('the table has no minimum width that would force a horizontal scroll', () => {
  const css = fs.readFileSync(path.join(__dirname, '..', '..', 'ui', 'blacksmith.css'), 'utf8');
  const forge = fs.readFileSync(path.join(__dirname, '..', '..', 'ui', 'forge-theme.css'), 'utf8');
  for (const source of [css, forge]) {
    for (const m of source.matchAll(/([^{}]*\.vms-table[^{}]*)\{([^}]*)\}/g)) {
      expect(m[2]).not.toMatch(/min-width:\s*\d{3,}/);
    }
  }
});
