# Web UI accessibility review — 25 October 2026

Target: WCAG 2.2 A/AA (https://www.w3.org/TR/WCAG22/).

## Remediated findings

- Broker and instance sidebar groups broke the ARIA tab hierarchy. Group
  wrappers are now presentational; each tab retains its group description.
- Generic and enhanced sorting were mouse-only and did not announce sort
  direction. Enter/Space now sort, focus remains visible, and aria-sort follows
  state. Enhanced sorting removes the generic handlers to avoid double sorting.
- Managed and persistent dialogs exposed background controls to assistive
  technology. They now isolate the background and restore its prior inert state.
  Task, config and registration dialogs contain focus, close with Escape and
  restore the opener. Empty dialogs and refreshed focused content are handled.
- Dialog visibility transitions delayed initial focus. Visibility now changes
  synchronously while opacity can animate.
- Config versions were clickable divs. They are now native buttons with selected
  state. Operations tab choices and endpoint choices expose their selected state.
- Terminal session close controls were mouse-only spans. They are separate,
  named buttons. The terminal dialog is named; wterm output announcements are on,
  cursor blink is off, and Control+Shift+F6 returns focus to terminal controls.
- Wrapped header controls could extend outside their contrasting background.
  The header now grows with its controls instead of retaining a fixed height.
- Config-type badges failed text contrast. They use the theme reading palette.
  Theme changes no longer crossfade inherited text through low-contrast colors.

## Validation

Automated axe checks use WCAG 2.0/2.1/2.2 A/AA rules, all four Anvil/High Contrast
palettes, populated local fixtures, and desktop/narrow viewports. Coverage includes
all 19 broker/instance sections, all three batch operations and four steps,
advanced release selection, fleet certificates, activity evidence, Quick
Navigation, keyboard help, task/config dialogs, and Valkey/RabbitMQ Operations forms.
Keyboard regression tests cover sorting, modal focus/isolation/restoration,
registration Escape, config-version controls, and terminal session buttons.
Existing UI regressions cover selection persistence, destructive-action review,
copy controls, polling, and certificate workflows.

No live BOSH, Cloud Foundry, SSH, or service operations were performed. Browser
fixtures provide synthetic data and block infrastructure writes. Passing automated
checks is not a certification: real assistive-technology testing (VoiceOver/NVDA),
live terminal output, deployment-specific content, and user testing remain useful
release acceptance checks. The review does not claim every possible runtime state
or third-party terminal escape sequence has been tested.

## Release checklist

Before we tag a release, one of us runs a screen-reader pass over the web UI, because the automated checks above can't tell us how a page actually sounds. We use VoiceOver with Safari on macOS and NVDA with Firefox on Windows, and we run the pass in one light theme and one dark theme. The pass uses the same synthetic fixtures as the automated checks, so it never touches a live director, Cloud Foundry, or a service.

We start on the broker page and move through it by headings and landmarks, and every section should announce a heading that names it. Then we move through the broker and instance sidebars with the arrow keys, and each tab should read its name, its group, and whether it is selected. On a sortable table we sort a column from the keyboard and listen for the new sort direction, and on a table that doesn't sort we check that the column headers still read with each cell.

Next we open a task dialog and a config dialog, and we confirm that the screen reader stays inside the dialog and that Escape closes it and returns us to the control that opened it. We apply a broker setting and listen for the status line that says it was applied, and we enter a bad value to hear the error read with its field. We make a load fail and check that the failure message is announced and that Retry is reachable and named. For a destructive action, such as a recreate or a batch upgrade, we walk through the typed confirmation and make sure each step reads its purpose before anything runs.

The Recent changes tables on the Settings and Endpoints pages need their own check. Each table should announce its caption and its six column headers, and a change to a secret should read as "(changed)" in both value columns. No secret value should ever be read aloud, whether from a cell, a label, a tooltip, or a status message.

We record who ran the pass, the screen reader and browser versions, and the build we tested, and we file anything we find before the tag. A release doesn't ship while a control has no spoken name, while focus is lost after a dialog closes, or while a status is shown only by colour.
