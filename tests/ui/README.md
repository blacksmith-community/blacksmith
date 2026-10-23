# Console checks

Run the pure navigation checks with `bun test tests/ui/navigation.test.js`.
No JavaScript dependencies or build step are required.

The certificate screens have their own suites under `tests/ui/certificates/`, and `bun test tests/ui/` runs them along with the navigation checks.
These suites load `ui/blacksmith.js` and `ui/index.html` into jsdom, replace `fetch` with scripted answers, and drive the real page code, so they need jsdom even though this repository does not depend on it.
We resolve jsdom with a normal import first and then from the `JSDOM_PATH` environment variable, so either of these works.

```sh
bun add --no-save jsdom
JSDOM_PATH=/path/to/node_modules/jsdom bun test tests/ui/
```

When neither source works, the certificate suites skip and print one message that says how to provide jsdom, and the navigation checks still run.
The first option installs jsdom without touching `package.json`, so we never commit it.
Every suite shares `tests/ui/certificates/harness.js`, which reads the page code through relative paths, and `tests/ui/certificates/support.js` finds jsdom and collects the named checks.
A suite reports every failed check by name at the end, so one failing test can list several broken behaviors.
Most suites also assert that no script error or `alert`, `confirm`, or `prompt` call happened, which keeps the rendering and dialog rules enforced.

For browser regression checks, start a static server from the repository:

```sh
python3 -m http.server 4199 --directory ui
```

In another terminal, from the same directory, use Playwright CLI:

```sh
playwright-cli -s=console-check open http://127.0.0.1:4199
playwright-cli -s=console-check run-code --filename=tests/ui/browser-check.js
```

The browser script intercepts catalog and broker endpoints with synthetic
data. It rejects and records every non-GET broker request. Do not substitute
a live deployment URL. A successful result reports 37 checks and zero writes.
Screenshots are written under `output/playwright/` relative to the browser
session's starting directory. Use a new session name if another session was
started from a different repository.

Checks include both shortcuts, focus containment/restoration, result selection,
empty results, matching beyond the rendered cap, instance/plan navigation,
cancelled and stale async responses, literal rendering of hostile labels,
exclusion of credentials, missing destinations, terminal and confirmation
conflicts, manifest and broker sub-screens, release-only upgrades, and narrow
layouts in both themes. No live upgrade, SSH session or CF mutation is run.
