# Blacksmith console UX and quick navigation

## Authority and baseline

This plan is the authoritative task record in the implementation worktree. The source repository is `src/services/blacksmith` (the
requested `src/systems/blacksmith` does not exist here). Final base per user: `origin/main`, 4964a208 (main and dev now coincide).
This includes the ported release-upgrade UI and validation fixes. Preserve
release and stemcell targets, managed-release discovery, and request contracts.

## Progress

- [x] Inspect requested branch, main, and final dev baseline.
- [x] Research navigation and accessibility patterns.
- [x] Inventory UI and create design.
- [x] Adversarial design review and remediation below.
- [x] Create source-repository worktree from main.
- [x] Write failing behavior tests, implement, refactor.
- [x] Browser verification in both themes and narrow layouts.
- [x] Adversarial implementation review and fixes.
- [x] Record validation, atomic commits, and handoff.

## Findings and screen inventory

The application is a vanilla JavaScript console, with roughly 18,000 lines
of shared JavaScript and 11,000 of CSS. Rendering replaces detail DOM; handlers
own network requests and operations. Theme state uses data-theme while generated
Tailwind dark variants use a different mechanism. Browser inspection with
synthetic data exposed nearly white detail text on white surfaces, a 30px
header, excessive empty framing, small section controls, and cramped batch
fields. Instance selection is a clickable div without keyboard activation.

Main areas: Blacksmith, Service Instances, Batch Operations.
Blacksmith: Details, Plans (plan details), Broker (registrations and CF browsing),
Logs, VMs (terminal), Events, Tasks (task log dialog), BOSH Configs (versions and
diff dialogs), Certificates (trusted/config/endpoint views and certificate
dialogs), Manifest (structured sections and YAML), Blacksmith Configuration.
Instances: Details, Events, VMs, Manifest, Certificates, Credentials, Logs
(per job), Testing (service-specific operations). Instance context must remain
explicit because names and detail-tab identifiers repeat.
Batch: service/plan/minimum filters, target stemcell/releases, concurrency setting,
select all/none/range, upgrade confirmation, job list/detail/pause/resume/cancel.

## Design

### Visual system and layout

Use a restrained blue/slate operations console: light gray canvas, white
surfaces, dark navy text, blue selected states; dark mode uses slate surfaces
and light text. Retain semantic success/warning/error labels. Use system fonts,
14px baseline, 1.45 line height, consistent 8/12/16/24px spacing, subtle borders
and shadows, and visible keyboard focus. A 56px header holds the brand,
environment context, existing theme toggle, and a visible “Go to…” shortcut.
Keep the three familiar main tabs; make sub-tabs wrap rather than disappear
offscreen. Add an explicit page title and short purpose text above content.
Preserve full-width operational tables, code, logs, credentials, and controls.

Fix theme styles through existing semantic variables and narrowly targeted
surface/text rules; do not regenerate Tailwind or replace the CSS framework.
Scope new layout rules to console containers, not generic dialogs/terminals.
Retain terminal positioning dependencies when increasing header height.

Instance list: retain filters and count, improve row spacing/selected border,
make rows focusable and Enter/Space operable, label refresh visibly, and retain
all identifiers. Narrow screens stack list and details with bounded scrolling.
Batch: label the setup and job-monitoring areas clearly, add short ordered
guidance, allow controls to wrap, and keep the primary action reachable.
Use document/panel scrolling on small screens so minimum widths cannot hide
fields. Do not change selection semantics, target validation, confirmations,
polling, request bodies, or concurrency behavior.

### Command palette

Native modal dialog with an editable combobox and listbox results. Open via
Cmd+K/Ctrl+K or header button. Focus search on open. Query matches normalized,
case-insensitive words across label, context, aliases, instance name/ID.
Rank exact/prefix matches first with stable ordering. Empty query shows screen
destinations; results include contextual labels to disambiguate repeated tabs.
Use Arrow Up/Down to select, Enter to navigate, Escape or Close to dismiss.
Tab remains within dialog; restore prior focus on cancel. Announce result
count/no matches; selected option is exposed via aria-activedescendant.
No commands that mutate infrastructure, reveal credentials automatically,
create terminals, or execute testing actions.

Index all main areas and Blacksmith sections from the existing controls; index
all loaded service instances by name and ID, including their detail sections.
Index currently available nested navigation controls (manifest, certificate,
job-log tabs) with their context. Only include mounted/available conditional
screens; do not fabricate unavailable endpoints, unloaded CF orgs or spaces,
or credential values. Rebuild on open to avoid stale instance lists. No query
or history persistence. Render untrusted labels using textContent.

Activate existing handlers, never reimplement their API calls. For async
instance selection, navigate to the requested section only after that instance's
detail tabs are ready; expose a small awaited navigation bridge from existing
handlers. Retain ordinary filters and selection state. Focus destination
control after navigation. If destination disappears, show a recoverable
message instead of navigating to another instance. Modifier shortcut ignores
composition/repeat and active dialogs/terminal input to preserve workflows.

## Research basis

- https://www.w3.org/WAI/ARIA/apg/patterns/dialog-modal/ — modal containment,
  Escape and return focus.
- https://www.w3.org/WAI/ARIA/apg/patterns/combobox/ — listbox active descendant,
  arrow navigation and selection.
- https://docs.github.com/en/get-started/accessibility/github-command-palette
  — global shortcut, search and contextual navigation as a discoverable pattern.

## Adversarial design review (before implementation)

1. **Critical: cross-instance or stale async navigation.** Rejected timer-based
   click sequencing. Await the existing instance selection bridge, verify the
   destination and guard competing selection responses with a sequence token.
2. **High: destructive actions disguised as destinations.** Palette only indexes
   explicit navigation controls. Never infer entries from arbitrary buttons.
3. **High: modal/terminal shortcut collision.** Do not open over an active
   legacy dialog or terminal input. Native dialog provides background inertness.
   Capture palette keystrokes so legacy document handlers cannot consume Escape.
4. **High: lost hidden screens.** Include all Blacksmith sections and every
   loaded instance's eight sections, not only visible active-panel controls.
   Include available nested tabs. Document conditional screens as contextual.
5. **High: theme mismatch and broad CSS regressions.** Test computed foreground
   and backgrounds with screenshots in both themes; avoid global !important
   overrides and changes to terminal/code semantics.
6. **Medium: mobile clipping.** Test 390px width, short viewport and zoom-like
   narrow layouts, including batch fields and modal result scrolling.
7. **Medium: large inventories and sensitive data.** Cap rendered results with
   an explicit count; searchable full index remains available. Index names/IDs
   and screen labels only; no credentials, logs, localStorage, or telemetry.
8. **Medium: unsaved forms.** Retain normal navigation semantics. Do not allow
   the shortcut to bypass confirmation dialogs. No new automatic saves.

## Verification and completion gates

Use Bun tests for matching/ranking/inventory contracts and browser regression
checks for actual dialog interactions and existing navigation. Prove red before
implementation. Browser fixtures must intercept all API traffic and reject
unplanned writes; never submit operations against a real deployment.
Check both shortcuts, repeated opening, focus return, arrows/Enter/Escape,
empty results, hostile labels, dynamic instances, nested tabs, terminal/modal
exclusion, keyboard instance selection, narrow overflow, both themes, and
batch selection/confirmation behavior. Run relevant existing Go UI contract
tests and broader repository tests as feasible; record pre-existing failures
separately. Review final diff for removed handlers or changed API contracts.

## Future considerations

Keep release-target UX compatible with future director/API changes.
Server-backed search, recent destinations, route/deep-link redesign, broad
legacy accessibility cleanup and backend behavioral changes are separate work.


## Implementation and adversarial review results

Worktree: `src/services/blacksmith-console-ux`; branch:
`improve-console-navigation`, based on `origin/main` at 4964a208.
No backend, request-body, batch selection, polling, confirmation, or terminal
protocol changes. New static assets are served by the existing WebRoot file
server. No package dependency or UI compilation step was introduced.

Completed visual work: theme-aware surfaces and readable detail tables,
clearer header and page context, wrapping section navigation, visible focus,
keyboard-operable instance/plan rows, labelled refresh, numbered batch setup,
action guidance, and stacked narrow-screen layouts. Palette covers main areas,
Blacksmith sections, all loaded instances and their eight sections, catalog
plans, and mounted manifest/log/testing/broker/endpoint sub-screens.

Adversarial findings and remediation:

| Finding | Evidence | Remediation |
| --- | --- | --- |
| Native dialog focus could wrap out | Failing browser focus test | Explicit Tab/Shift+Tab containment |
| Escape during slow selection still changed instance | Failing delayed-response test | Abort signal and deferred selection commit |
| Slow Details response overwrote Plans | Failing out-of-order response test | Request generation guards for both detail loaders |
| Plan display IDs did not match catalog IDs | Failing ordinary plan click test | Catalog service/plan index mapping; palette resolves current IDs |
| Fresh manifest buttons could miss rapid selection | Failing palette YAML test | Immediate delegated manifest tab handling |
| Legacy confirmation had a custom overlay class | Overlay audit | Explicit active-overlay shortcut exclusion |
| Mobile sticky filters obscured instance rows | Screenshot inspection | Static mobile filters and separately bounded list scrolling |
| Stale/hostile/large inventories | Browser and unit adversarial checks | Rebuild on open, literal labels, missing-target feedback, 80-row render cap |

All reproduced failures above were remediated and the relevant checks rerun.
No unresolved blocking findings remain from this review. Review was performed
in this session, not by an independent reviewer.

Validation:

- `bun test tests/ui/navigation.test.js`: 6 passed, 16 assertions.
- Playwright `tests/ui/browser-check.js`: 37 checks passed, zero uncaught
  browser errors, zero broker write requests.
- `go test ./...`: 2,002 passed across 58 packages. Go source is unchanged.
- `git diff --check`: clean.
- Inspected all eleven Blacksmith sections with fixture responses, plus
  instance details, batch controls and light/dark palette screenshots.
- Deliberate failing tests preceded the palette implementation and each
  reproduced async/plan/manifest regression fix.

Limits: browser checks use synthetic local responses. Live BOSH upgrades,
Cloud Foundry connections, SSH and certificate operations were not executed.
Existing operational handlers and their confirmations remain in place.
Screen-reader announcements were verified structurally; no manual assistive
technology session or live deployment acceptance test was performed.

## Commit review

Proposed atomic order: regression tests; plan selection fix; immediate manifest
navigation; quick navigation and async safety; visual styling; design/review
record and check instructions. Tests intentionally precede their implementation.
Do not push, merge or deploy as part of this task. Offer `/commit sync` after
commits, and retain `/commit plan` as the review entry point.

Completion: implementation and both adversarial reviews are finished. Local
commits are separated by the groups above; the worktree is ready for review.
Nothing has been pushed, merged, or deployed.
