const { test, expect } = require('bun:test');
const { main: { boot, sleep } } = require('./certificates/harness.js');

// Two log tables on one page each keep their own revealed-row count and
// focus, so the row cap key must differ between them.
test('instance log tables get different row cap keys', async () => {
  const { w, d } = boot({ noDialogs: true });
  await sleep(50);
  const lines = Array.from({ length: 150 }, (_, i) => `INFO line ${i}`).join('\n');
  const one = d.createElement('div');
  const two = d.createElement('div');
  d.body.append(one, two);
  one.innerHTML = w.__T.formatInstanceLogs({ 'node/a': { 'app.log': lines } });
  two.innerHTML = w.__T.formatInstanceLogs({ 'node/b': { 'app.log': lines } });
  w.__T.initializeAllTables();
  const all = [...d.querySelectorAll('.row-cap-more')].map((b) => b.dataset.keepKey);
  expect(all.length).toBe(2);
  expect(all[0]).toBeTruthy();
  expect(all[0]).not.toBe(all[1]);
  w.close();
});
