# Console checks

Run the pure navigation checks with `bun test tests/ui/navigation.test.js`.
No JavaScript dependencies or build step are required.

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
