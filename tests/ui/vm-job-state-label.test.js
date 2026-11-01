const { test, expect } = require('bun:test');
const { main: { boot } } = require('./certificates/harness.js');

const UUID = '154a360d-8f56-41e4-98cf-0a30856edd69';
const instance = {
  service_id: 'valkey', plan_id: 'standalone', plan: { id: 'standalone', name: 'standalone' },
  instance_name: 'certmgmt-valkey-solo', vm_status: 'running', vm_count: 1, vm_healthy: 1
};

test('the instance summary labels the running value as the VM job state', () => {
  const { w, d } = boot();
  try {
    w.__T.renderServiceDetail(UUID, instance, null);
    const host = d.createElement('div');
    host.innerHTML = w.serviceInstanceDetailsContent[UUID];
    const text = host.querySelector('.entity-metrics').textContent;
    expect(text).toContain('VM job state');
    expect(text).toContain('running');
    expect(text).not.toContain('VM status');
  } finally { w.close(); }
});

test('an instance card names its badge as the VM job state without changing the badge', () => {
  const { w, d } = boot();
  try {
    w.plansData = { services: [{ id: 'valkey', name: 'valkey' }] };
    const host = d.createElement('div');
    host.innerHTML = w.__T.renderServicesTemplate({ [UUID]: instance });
    const badge = host.querySelector('.vm-status-badge');
    expect(badge.getAttribute('title')).toBe('VM job state: running (1/1 healthy)');
    expect(badge.classList.contains('vm-status-running')).toBe(true);
    expect(badge.querySelector('.vm-status-text').textContent).toBe('running');
  } finally { w.close(); }
});
