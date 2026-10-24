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
python3 -m http.server 4202 --directory ui
```

In another terminal, from the same directory, use Playwright CLI:

```sh
playwright-cli -s=console-check open http://127.0.0.1:4202
playwright-cli -s=console-check run-code --filename=tests/ui/browser-check.js
```

The browser script intercepts catalog and broker endpoints with synthetic
data. It mocks the upgrade preview and rejects all other non-GET broker requests. Do not substitute
a live deployment URL. A successful result reports 64 checks and zero writes.
Screenshots are written under `output/playwright/` relative to the browser
session's starting directory. Use a new session name if another session was
started from a different repository.

Checks include both shortcuts, focus containment/restoration, result selection,
empty results, matching beyond the rendered cap, instance/plan navigation,
cancelled and stale async responses, literal rendering of hostile labels,
exclusion of credentials, missing destinations, terminal and confirmation
conflicts, manifest and broker sub-screens, release-only upgrades, and narrow
layouts in both themes. No live upgrade, SSH session or CF mutation is run.


The forge checks cover all 11 broker and 8 instance sections, with 1,003
instances and at most 50 instance rows mounted. `forge-console.test.js` checks
Oat-only assets, hostile labels, visibility state, platform metadata, and
contrast ratios (7:1 primary reading text, 4.5:1 muted/action text, 3:1 controls).
Run `tests/ui/batch-browser-check.js` in the same CLI session for the populated
batch workflow, selection persistence, and mobile review.

For the accessibility sweep, run `browser-check.js` first to install its fixtures.
Install `axe-core` in a temporary directory, then inject its local script (replace
the absolute path below) before running the audit:

```sh
playwright-cli -s=console-check run-code 'async (page) => { await page.addScriptTag({path:"/absolute/path/node_modules/axe-core/axe.min.js"}); }'
playwright-cli -s=console-check run-code --filename=tests/ui/accessibility-browser-check.js
```

The audit checks all broker/instance sections in both themes using WCAG A/AA
rules. It fails with the affected selectors. Automated checks supplement visual,
keyboard, mobile, and lab checks; they do not certify full WCAG conformance.

After `batch-browser-check.js`, preload axe-core again (the page reload clears
it) and run `tests/ui/populated-accessibility-browser-check.js` to audit the
populated batch/certificate views in both themes and viewport sizes, plus
the mobile inline review.

Run `batch-flow-browser-check.js` for the focused Operation → Options →
Instances → Review flow. Its fixture records two synthetic task creations
(rotation and upgrade), verifies that no task starts before confirmation, and
checks retained drafts, typed confirmation, and late preview cancellation.
It uses 1,003 instances and writes screenshots of each major step.
After running it, inject axe-core again and run
`batch-flow-accessibility-browser-check.js` for all three operations and four
steps in light/dark themes at desktop/mobile sizes (48 combinations).
All of these browser checks use only local, intercepted endpoints.
