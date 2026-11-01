const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');
const { pinZone } = require('../fixed-zone.js');
pinZone('UTC');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const { boot, sleep } = H.main;
const mkTask = (variable) => ({
  id: 'task-1', name: 'Rotate', kind: 'certificates', status: 'running', paused: false,
  certificates: { mode: 'redeploy', preview_id: 'pv' },
  total_count: 1, completed_count: 0, failed_count: 0, created_at: '2026-10-24T09:00:00Z',
  instances: [{ instance_id: 'i1', instance_name: 'one', deployment_name: 'd1', status: 'running',
    certificates: { phase: 'verify', variables: [variable] } }],
});

t('expiry comparison shows the time only when both days match', async () => {
  const env = boot(); const { w, d, alerts } = env; const T = w.__T;
  await sleep(100);
  const panel = d.querySelector('#upgrade .upgrade-tasks');
  const expiry = (variable) => {
    T.renderTaskDetail(mkTask(variable));
    const el = panel.querySelector('.cert-task-comparison tbody tr:nth-child(2)');
    return el ? [...el.querySelectorAll('td')].map(cell => cell.textContent).join(' | ') : null;
  };

  ok(expiry({ name: '/s/a_crt', old_serial: 'AA', new_serial: 'BB',
    old_not_after: '2029-10-24T09:14:30Z', new_not_after: '2029-10-24T13:40:05Z' })
    === 'Expires | 2029-10-24 09:14:30 UTC | 2029-10-24 13:40:05 UTC', 'same day shows both times with seconds');
  ok(expiry({ name: '/s/a_crt', old_serial: 'AA', new_serial: 'BB',
    old_not_after: '2029-10-24T09:14:00Z', new_not_after: '2029-10-25T09:14:00Z' })
    === 'Expires | 2029-10-24 | 2029-10-25', 'different days keep the date-only output');
  ok(expiry({ name: '/s/a_crt', old_serial: 'AA', new_serial: '',
    old_not_after: '2029-10-24T09:14:00Z', new_not_after: '' })
    === 'Expires | 2029-10-24 | Pending', 'a missing new value keeps the date-only output');
  ok(alerts.length === 0, 'no window.alert/confirm/prompt used');
  expect(failures).toEqual([]);
});
