// Shared helpers for the certificate UI suites. jsdom is not a dependency of
// this repository, so it is resolved at run time and the suites skip with one
// message when it cannot be found.
const NOTE = 'Skipping the certificate UI suites because jsdom could not be loaded. '
  + 'Provide it with `bun add --no-save jsdom`, or point JSDOM_PATH at an existing copy, '
  + 'for example `JSDOM_PATH=/path/to/node_modules/jsdom bun test tests/ui/`.';

function loadJSDOM() {
  const attempts = [() => require('jsdom')];
  if (process.env.JSDOM_PATH) attempts.push(() => require(process.env.JSDOM_PATH));
  for (const attempt of attempts) {
    try {
      const mod = attempt();
      if (mod && mod.JSDOM) return mod.JSDOM;
    } catch (_) { /* try the next source */ }
  }
  return null;
}

const JSDOM = loadJSDOM();
if (!JSDOM && !globalThis.__certUiSkipNoted) {
  globalThis.__certUiSkipNoted = true;
  console.warn(NOTE);
}

// Collects named checks the way the suites record them: ok(condition, message).
// A suite finishes with expect(failures).toEqual([]) so every failed message
// shows in the report.
function checks() {
  const failures = [];
  let passed = 0;
  const ok = (condition, message) => {
    if (condition) passed++;
    else failures.push(String(message));
  };
  return { ok, failures, count: () => passed };
}

module.exports = { JSDOM, checks, NOTE };
