const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

const { boot, sleep, EVIL } = H.main;
const chk = (name, passed, detail) => ({ name, passed, detail });
const good = { version_id: 'v-good-1234567890', created_at: '2026-10-21T14:10:00Z', serial: 'BB22' + EVIL, not_after: '2027-10-21T00:00:00Z', valid: true, checks: [chk('signed ' + EVIL, true, '')] };
const bad = { version_id: 'v-bad-0987654321', created_at: '2026-10-21T14:11:00Z', serial: 'CC33', not_after: '2027-10-21T00:00:00Z', valid: false, checks: [chk('chain', false, 'wrong CA')] };
// A task whose one instance waits to reconcile a regeneration. pending
// holds the decision's candidates and when they were checked, if ever.
const mkTask = (pending) => ({
  id: 'task-1', name: 'Spring', kind: 'certificates', status: 'paused', paused: true, certificates: { mode: 'redeploy', preview_id: 'pv' },
  total_count: 1, completed_count: 0, failed_count: 0, created_at: '2026-10-21T14:00:00Z',
  instances: [{
    instance_id: 'i-rec', instance_name: 'rec', deployment_name: 'd1', status: 'awaiting_decision',
    certificates: { phase: 'regenerate', variables: [{ name: '/s/leaf_crt', old_serial: 'AA11', regenerate_state: 'outcome_unknown' }],
      pending_decision: { kind: 'reconcile_regeneration', variable: '/s/leaf_crt', since: '2026-10-21T14:20:00Z', ...pending } }
  }]
});

t('candidate refresh', async () => {
  const env = boot(); const { w, d, errors, alerts, calls } = env; const T = w.__T;
  let refreshed = mkTask({ candidates: [good, bad], candidates_checked_at: '2026-10-21T15:05:00Z' });
  let resp = null;
  const posts = () => calls.filter((c) => c.startsWith('POST'));
  const candidatePosts = () => posts().filter((c) => /\/instances\/i-rec\/candidates( |$)/.test(c));
  w.__routes.push(['/b/bosh/stemcells', (u, o, j) => j(200, [])], ['/b/bosh/releases', (u, o, j) => j(200, [])], ['/b/bosh/deployment-releases', (u, o, j) => j(200, {})], ['/b/upgrade/settings', (u, o, j) => j(200, {})], ['/b/certificates/inventory', (u, o, j) => j(200, { certificates: [], capabilities: { rotation: { enabled: true } } })],
    [/\/instances\/[^/]+\/candidates$/, (u, o, j) => (resp ? j(resp.status, resp.body) : j(200, refreshed))],
    [/\/instances\/[^/]+\/reconcile$/, (u, o, j) => j(200, refreshed)],
    [/\/b\/upgrade\/tasks\/[^/]+$/, (u, o, j) => j(200, refreshed)],
    [/\/b\/upgrade\/tasks$/, (u, o, j) => j(200, [])]);
  w.serviceInstances = {}; await sleep(50); w.plansData = { services: [] };
  d.getElementById('upgrade').classList.add('active'); await T.initUpgradeTab(); await sleep(100);
  const panel = () => d.querySelector('#upgrade .upgrade-tasks');
  const reconcile = () => panel().querySelector('.cert-decision-panel');
  const btn = (action) => [...panel().querySelectorAll('[data-decision-action]')].find((b) => b.dataset.decisionKey === 'task-1|i-rec' && b.dataset.decisionAction === action);
  const live = () => d.getElementById('blacksmith-live-region').textContent;

  T._setSelectedTask('task-1');

  // ---- never checked: a decision loaded after a crash
  T.renderTaskDetail(mkTask({}));
  ok(/not checked CredHub/.test(reconcile().textContent), 'unchecked: says CredHub was not checked yet => ' + reconcile().textContent);
  ok(!/CredHub holds no version/.test(reconcile().textContent), 'unchecked: does not claim CredHub holds no version');
  ok(!!btn('candidates') && btn('candidates').textContent === 'Check CredHub again' && !btn('candidates').disabled, 'unchecked: offers Check CredHub again');
  ok(![...panel().querySelectorAll('[data-decision-action]')].some((b) => b.dataset.decisionAction.startsWith('adopt:')), 'unchecked: offers no Adopt button');
  ok(!!btn('abandon'), 'unchecked: Abandon is still offered');

  // ---- checked, none found
  T.renderTaskDetail(mkTask({ candidates_checked_at: '2026-10-21T15:00:00Z' }));
  ok(/CredHub holds no version that this request could have made/.test(reconcile().textContent), 'checked and empty: says CredHub holds no version');
  ok(reconcile().textContent.includes('2026-10-21 15:00 UTC'), 'checked and empty: shows when CredHub was checked');
  ok(!/not checked CredHub/.test(reconcile().textContent) && !!btn('candidates'), 'checked and empty: no not-checked text, refresh still offered');

  // ---- refresh finds a version to adopt
  T.renderTaskDetail(mkTask({}));
  const n0 = candidatePosts().length;
  btn('candidates').click(); btn('candidates').click(); await sleep(100);
  ok(candidatePosts().length === n0 + 1, 'refresh: one POST to the candidates route, even on a double click: ' + (candidatePosts().length - n0));
  ok(!d.querySelector('#dialog-modal-overlay.active'), 'refresh: asks for no confirmation');
  ok(!!btn('adopt:v-good-1234567890') && btn('adopt:v-good-1234567890').textContent === 'Adopt version v-good-1', 'refresh: the valid candidate gets an Adopt button');
  ok(!btn('adopt:v-bad-0987654321') && /failed a check/.test(reconcile().textContent), 'refresh: the failed candidate gets no Adopt button');
  ok(reconcile().textContent.includes('2026-10-21 15:05 UTC') && !/not checked CredHub/.test(reconcile().textContent), 'refresh: shows the new check time');
  ok(/found 2 versions/.test(live()), 'refresh: the live region says what was found => ' + live());
  ok(d.activeElement === btn('candidates'), 'refresh: focus returns to Check CredHub again');
  ok(panel().querySelectorAll('img,script,iframe').length === 0 && reconcile().textContent.includes(EVIL), 'refresh: hostile strings render as text');

  // ---- a refresh that fails shows why on the instance
  resp = { status: 503, body: { success: false, error: 'CredHub did not answer ' + EVIL, code: 'credhub_unavailable' } };
  T.renderTaskDetail(mkTask({}));
  btn('candidates').click(); await sleep(100);
  const box = panel().querySelector('[data-decision-error]');
  ok(!!box && box.getAttribute('role') === 'alert' && box.textContent.includes('CredHub did not answer'), 'failure: inline alert shows the message => ' + (box && box.textContent));
  ok(d.activeElement === box && live().includes('CredHub did not answer'), 'failure: focus on the alert, and it is announced');
  ok(panel().querySelectorAll('img,script,iframe').length === 0, 'failure: hostile message renders as text');
  resp = null;
  T.renderTaskDetail(mkTask({})); btn('candidates').click(); await sleep(100);
  ok(!panel().querySelector('[data-decision-error]') && !!btn('adopt:v-good-1234567890'), 'a later refresh clears the error');

  ok(alerts.length === 0, 'no alert/confirm/prompt: ' + alerts.join('|'));
  ok(!errors.some((e) => /Reference|TypeError|SyntaxError|LOAD/.test(e)), 'no script errors: ' + errors.join('|'));
  expect(failures).toEqual([]);
}, 120000);
