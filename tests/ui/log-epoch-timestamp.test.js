const { test, expect } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');

// An invented pid and epoch. 1767225600 is 2026-01-01 00:00:00 UTC.
const SIGNAL_LINE = '4242:signal-handler (1767225600) Received SIGTERM scheduling shutdown...';

const rowFor = (w, line) => {
  const host = w.document.createElement('table');
  host.innerHTML = `<tbody>${w.__T.renderLogRow(w.__T.parseLogLine(line), { timestamp: true, level: true, component: false, message: true })}</tbody>`;
  return host.querySelector('tr');
};

test('a Valkey signal-handler line shows the epoch in parentheses as a UTC time', async () => {
  const { w } = boot();
  try {
    await sleep(50);
    const entry = w.__T.parseLogLine(SIGNAL_LINE);
    expect(entry.date).toBe('2026-01-01');
    expect(entry.time).toBe('00:00:00.000');
    expect(entry.level).toBe('INFO');
    expect(entry.message).toContain('Received SIGTERM scheduling shutdown...');
    const row = rowFor(w, SIGNAL_LINE);
    expect(row.querySelector('.log-timestamp').textContent).toBe('2026-01-01 00:00:00.000');
    // The ordinary Redis line shows its time in the same form.
    const ordinary = rowFor(w, '4242:M 01 Jan 2026 00:00:01.250 * Ready to accept connections');
    expect(ordinary.querySelector('.log-timestamp').textContent).toBe('2026-01-01 00:00:01.250');
  } finally { w.close(); }
});

test('a line with no readable time keeps the dash and says why', async () => {
  const { w } = boot();
  try {
    await sleep(50);
    const cell = rowFor(w, 'a continuation line with no time').querySelector('.log-timestamp');
    expect(cell.getAttribute('title')).toBe('No timestamp in this log line');
    expect(cell.textContent).toBe(w.__T.EMPTY);
    const dated = rowFor(w, SIGNAL_LINE).querySelector('.log-timestamp');
    expect(dated.hasAttribute('title')).toBe(false);
    // Digits in parentheses that are not an epoch do not become a time.
    const small = w.__T.parseLogLine('4242:signal-handler (42) Received SIGTERM');
    expect(small.date).toBe('');
  } finally { w.close(); }
});
