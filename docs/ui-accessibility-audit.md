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
