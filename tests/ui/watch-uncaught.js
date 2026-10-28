// Hooks a jsdom window into the uncaught-error record that
// tests/ui/uncaught-guard.js reads. A suite that builds its own window calls
//
//   const w = watchUncaught(new JSDOM(html, options).window);
//
// and the guard then fails the test that was running if the window raises an
// error event. Pass the errors list of a suite that reads its own copy.
const watchUncaught = (w, errors = []) => {
  const seen = [];
  (globalThis.__uiUncaught = globalThis.__uiUncaught || []).push(seen);
  w.addEventListener('error', (e) => { const m = 'UNCAUGHT ' + (e.message || e.error); seen.push(m); errors.push(m); });
  return w;
};

module.exports = { watchUncaught };
