const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

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
// The 39 digit form is what an older build printed for a serial that starts with 07.
const SHORT = '793a41c0b2d4e6f8091a2b3c4d5e6f708192a3b';
const LONG = '0793a41c0b2d4e6f8091a2b3c4d5e6f708192a3b';

t('serials are padded to whole bytes and compared after normalizing', async () => {
  const env = boot(); const { w, d, alerts } = env; const T = w.__T;
  await sleep(100);

  ok(SHORT.length === 39 && LONG.length === 40, 'fixture lengths');
  ok(T.displaySerial(SHORT) === LONG.toUpperCase(), 'a 39 digit serial gains its leading zero and shows in upper case');
  ok(T.displaySerial('abc') === '0ABC' && T.displaySerial('0ABC') === '0ABC' && T.displaySerial('7') === '07', 'odd lengths pad, even lengths stay');
  ok(T.displaySerial('00ab') === '00AB', 'a padded leading zero byte is kept');
  ok(T.displaySerial('') === '' && T.displaySerial(undefined) === '' && T.displaySerial(null) === '', 'an empty serial stays empty');
  ok(T.displaySerial('not hex!') === 'not hex!', 'a value that is not hex is shown as it came');

  ok(T.normalizeSerial(SHORT) === T.normalizeSerial(LONG), 'padded and unpadded forms normalize alike');
  ok(T.normalizeSerial('07:93:A4:1C') === T.normalizeSerial('0793a41c'), 'colons and case are ignored');
  ok(T.normalizeSerial('0000ab') === 'ab' && T.normalizeSerial('abc') === '0abc', 'leading zeros trim to whole bytes');
  ok(T.normalizeSerial('0') === '00' && T.normalizeSerial('00') === '00', 'zero is one byte');
  ok(T.normalizeSerial('xyz') === '' && T.normalizeSerial('') === '', 'a non hex value normalizes to nothing');

  ok(T.sameSerial(SHORT, LONG.toUpperCase()), 'old and new spellings of one serial match');
  ok(!T.sameSerial(LONG, '0793a41c0b2d4e6f8091a2b3c4d5e6f708192a3c'), 'different serials differ');
  ok(!T.sameSerial('', 'AA') && T.sameSerial('zz', 'zz') && !T.sameSerial('zz', 'yy'), 'empty and non hex values compare as plain text');

  const inst = (o, n) => ({ certificates: { variables: [{ name: '/s/a_crt', old_serial: o, new_serial: n }] } });
  ok(T.hasUndeployedVersion(inst(SHORT, LONG)) === false, 'a mixed pair of one serial is not a new version');
  ok(T.hasUndeployedVersion(inst(LONG.toUpperCase(), SHORT)) === false, 'case and padding do not make a new version');
  ok(T.hasUndeployedVersion(inst(LONG, '0793a41c0b2d4e6f8091a2b3c4d5e6f708192a3c')) === true, 'a changed serial is a new version');
  ok(T.hasUndeployedVersion(inst('AA', '')) === false && T.hasUndeployedVersion(inst('', 'BB')) === true, 'no new serial is not a new version');

  const panel = d.querySelector('#upgrade .upgrade-tasks');
  T.renderTaskDetail(mkTask({ name: '/s/a_crt', old_serial: SHORT, new_serial: 'abc', old_not_after: '2029-10-24T09:14:00Z', new_not_after: '2029-10-25T09:14:00Z' }));
  const line = panel.querySelector('.cert-task-variable-serial');
  ok(!!line && line.textContent === `Serial: was ${LONG.toUpperCase()}, now 0ABC`, 'the task serial line shows padded serials: ' + (line && line.textContent));
  ok(alerts.length === 0, 'no window.alert/confirm/prompt used');
  expect(failures).toEqual([]);
});
