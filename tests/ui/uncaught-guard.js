// Preloaded by bunfig.toml, so the afterEach below applies to every test file.
// A harness window that raised an uncaught error (see watchUncaught in
// certificates/harness.js) fails the test that was running, whether or not the
// suite looks at its own errors list.
const { afterEach } = require('bun:test');

afterEach(() => {
  const windows = globalThis.__uiUncaught || [];
  globalThis.__uiUncaught = [];
  const found = windows.flat();
  if (found.length) throw new Error('Uncaught window errors during the test:\n' + found.join('\n'));
});
