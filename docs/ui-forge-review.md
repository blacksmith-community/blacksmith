# Forge console migration and review

The console loads the vendored Oat 1.0.0 CSS and JavaScript on every screen.
`forge-theme.css` supplies Oat theme tokens and application layouts; existing
operational components retain their component-specific CSS. Tailwind CSS,
its runtime, and utility-class dependencies have been removed. No build step
or runtime CDN is required.

## Operator experience

Broker and service instances share Overview, grouped operational sections,
and a native section picker at narrower widths. Overview distinguishes
reported state from unknown state, presents context before technical data,
and keeps identifiers copyable. Technical metadata is collapsed by default.
The section heading and navigation stay synchronized with asynchronous loads.

The instance list searches the whole fleet and mounts at most 50 rows.
Pagination retains existing handlers; quick navigation reveals destinations
outside the mounted page. On small screens, All instances returns to the list
and restores focus. Batch setup and Activity remain separate full-width views.

Iron charcoal, warm ivory, and copper distinguish the brand. Status colors
retain their independent meaning and text labels. Theme selection controls
Oat's color scheme explicitly. Primary reading pairs target 7:1, muted and
button text 4.5:1, and control/focus boundaries 3:1.

The generated anvil/spark artwork and exact generation prompt are in
[the brand README](../ui/assets/brand/README.md). The manifest provides
Android/desktop and maskable icons; Apple touch and multi-size favicon assets
are linked in the document. No service worker or offline state cache is added.

## Adversarial review and remediation

| Finding | Remediation and regression coverage |
| --- | --- |
| Full instance list remained mounted and lacked free-text search | Search and 50-row mounting, tested with 1,003 instances and off-page navigation |
| Instance labels could become HTML or break quoted attributes | Escape label/attribute values and remove nested inline click code; hostile-input test |
| ARIA groups broke tab ownership | Presentational group wrappers, labeled tablist, roving focus, linked tab panel; axe sweep |
| Mobile quick navigation focused a hidden tab | Focus the visible section picker and test keyboard navigation |
| Returning to the list retained detail context | Clear contextual heading and restore the selected list row |
| Visibility utility migration blocked the registration dialog | Remove visibility states explicitly on open; regression test |
| Modal background remained exposed to accessibility tools | Apply inert while open, restoring pre-existing state on close; regression test |
| Task and endpoint selectors lacked accessible labels | Add explicit labels; axe sweep |
| VM counts, dark action buttons, and muted filter labels missed contrast | Remove count opacity and use the tested Oat palette; axe sweep |
| Certificate table could not receive keyboard focus for horizontal scrolling | Named focusable scroll region; regression test and mobile audit |
| Oat and legacy review summaries drew two chevrons | Keep one stateful chevron in the review grid |
| Oat document spacing enlarged application cards/headings | Scope spacing adjustments to the console shell and inspect screenshots |

## Validation

- 49 UI tests, including certificate rotation, confirmations, selection,
  async state, hostile labels, theme ratios, and platform metadata.
- 64 browser fixture checks across all 11 broker and 8 instance sections,
  light/dark layouts, mobile keyboard navigation, and enlarged-text reflow.
- Populated batch checks with 1,003 instances, 50-row pages, preserved
  selection, expandable technical details, and mobile confirmation.
- axe-core WCAG A/AA sweep across 38 broker/instance view/theme combinations;
  eight populated fleet/batch view/theme/viewport combinations and one
  mobile confirmation check. All reported zero violations after remediation.
- Oat-only browser validation ran before Tailwind assets were deleted.

Tests use intercepted APIs. No live deployment, rotation, SSH session, or CF
mutation was performed. Browser installation on actual Android/iOS devices,
real operator data, and assistive-technology user testing remain lab checks.
Automated checks do not constitute full WCAG certification. Reproduction
commands are in [the UI test README](../tests/ui/README.md).
