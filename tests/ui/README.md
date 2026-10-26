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

The batch flow checks also cover mode-tab arrow keys, required patch follow-up,
visible Select/Selected feedback, and removal of visual row ordinals.
Job metadata helper text and single-toggle label behavior are covered by
`batch-flow.test.js`.

After `batch-flow-browser-check.js`, run `batch-review-browser-check.js` to
verify the single-instance review at desktop and mobile sizes. It checks
that pagination is hidden for a single page, text uses the available width,
and the action bar stays compact while retaining 44px button targets.

Run `upgrade-options-browser-check.js` for the System/Releases table and
additional-release picker. It uses 153 uploaded releases and checks bounded
pages, search, draft isolation, Apply/Cancel/Escape, summary removal, and
responsive layouts without creating tasks. Inject axe-core afterward and run
`upgrade-options-accessibility-browser-check.js` for both screens in both
themes at desktop/mobile widths.

## Accessibility audit fixtures

`accessibility-populated-fixture.js` loads synthetic tasks, VMs, events,
configs, credentials and logs. Run it instead of the navigation regression
script before injecting axe-core. `accessibility-browser-check.js` covers
all broker and instance sections in all four themes at 1440px and 320px.
`accessibility-dialogs-browser-check.js` additionally checks Quick Navigation,
keyboard help, task details and config details, including task focus containment.
The batch and advanced-picker accessibility checks also cover all four themes.
Use only a local static server: the fixtures reject infrastructure writes.

See `docs/ui-accessibility-audit.md` for findings, remediation and limits.


For navigation/history regression coverage, run
`tests/ui/navigation-history-browser-check.js` after the populated fixture.
It exercises all broker and instance sections, plan Back/Forward, nested
certificate reloads, testing drafts across instances, and mobile plan return.
The new `routes`, `quicknav-cancellation`, `inspection-routing`,
`inspection-lifecycle`, `endpoint-navigation`, and `refresh-lifecycle` unit
suites cover canceled/late responses and prevent navigation from executing
operational actions. `action-help` and `manifest-accessibility` cover the
shared focus/hover help and nested tab semantics.


`quicknav-scale.test.js` covers 10,000 instances, identity ranking, cooperative
search cancellation, progressive keyboard results and friendly catalog names.
Run `quicknav-scale-browser-check.js` after the populated local API fixture to
check progressive results and all four themes at desktop, phone and short-window
sizes. These checks use the already loaded fleet inventory; result pagination
does not introduce a server-side search API.
