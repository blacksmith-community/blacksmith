const { test, expect } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');

const JOB = 'job/0';

// Mounts the instance logs section with the production renderer, the way a first load does.
const mountLogs = async (w, d, files) => {
  await sleep(200);
  const panel = d.getElementById('services');
  panel.classList.add('active');
  const item = d.createElement('div');
  item.className = 'service-instance-item active';
  item.dataset.id = 'inst-1';
  panel.appendChild(item);
  w.currentInstanceInfo = { id: 'inst-1' };
  let content = panel.querySelector('.detail-content');
  if (!content) {
    content = d.createElement('div');
    content.className = 'detail-content';
    panel.appendChild(content);
  }
  panel.querySelectorAll('.detail-tab').forEach((tab) => tab.classList.toggle('active', tab.dataset.tab === 'instance-logs'));
  expect(w.__T.retainView(content, 'instance-logs')).toBe(false);
  content.innerHTML = w.__T.formatInstanceLogs({ [JOB]: files }, false);
  return content;
};
const stubLogs = (w, body) => {
  w.fetch = () => Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve(JSON.stringify(body)) });
};
const options = (content) => [...content.querySelectorAll('.log-file-dropdown option')].map((o) => o.value);

test('Refresh keeps the file list and shows the new contents of the selected file', async () => {
  const { w, d } = boot();
  try {
    const content = await mountLogs(w, d, { 'app.log': 'first app line', 'other.log': 'first other line' });
    const select = content.querySelector('.log-file-dropdown');
    select.value = 'other.log';
    expect(options(content)).toEqual(['app.log', 'other.log']);
    stubLogs(w, { [JOB]: { 'app.log': 'second app line', 'other.log': 'second other line' } });
    content.querySelector('[data-instance-log="refresh"]').click();
    await sleep(60);
    expect(options(content)).toEqual(['app.log', 'other.log']);
    expect(content.querySelector('.log-file-dropdown').value).toBe('other.log');
    expect(content.querySelector('tbody').textContent).toContain('second other line');
    expect(content.textContent).not.toContain('No logs available');
    expect(w.instanceLogOriginalText[JOB]).toBe('second other line');
  } finally { w.close(); }
});

test('Refresh reads the same wrapped response the first load reads', async () => {
  const { w, d } = boot();
  try {
    const content = await mountLogs(w, d, { 'app.log': 'first app line' });
    stubLogs(w, { logs: { [JOB]: { 'app.log': 'wrapped app line', 'new.log': 'new file line' } }, truncated: false });
    content.querySelector('[data-instance-log="refresh"]').click();
    await sleep(60);
    expect(options(content)).toEqual(['app.log', 'new.log']);
    expect(content.querySelector('tbody').textContent).toContain('wrapped app line');
  } finally { w.close(); }
});

test('Refresh falls back to the first file when the selected one is gone', async () => {
  const { w, d } = boot();
  try {
    const content = await mountLogs(w, d, { 'app.log': 'first app line', 'other.log': 'first other line' });
    content.querySelector('.log-file-dropdown').value = 'other.log';
    stubLogs(w, { [JOB]: { 'app.log': 'only app line' } });
    content.querySelector('[data-instance-log="refresh"]').click();
    await sleep(60);
    expect(options(content)).toEqual(['app.log']);
    expect(content.querySelector('tbody').textContent).toContain('only app line');
  } finally { w.close(); }
});
