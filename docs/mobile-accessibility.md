# Mobile, responsive, theme, and accessibility behavior

Director for Paseo uses one React Native semantic model on desktop, browser,
iOS, and Android. Paseo owns the route, header, host picker, workspace and
agent navigation, error boundary, query client, modal focus trap, Escape/back/
gesture dismissal, and focus return to the invoking control. Director owns
only the contributed surface and panel bodies. It does not reproduce those
host controls or use a cross-host fallback.

## Compact behavior

- Paseo `layout.compact` or a React Native window narrower than 1,200 points
  selects an Epic-grouped List. This preserves compact semantics in browser
  windows where the Paseo sidebar leaves a narrow plugin body. Resizing into
  that range also returns to List-first grouping. Switching to Board exposes
  accessible lane tabs and renders one engine-derived lane at a time. Neither
  presentation changes Task state or engine order.
- Paseo `Modal` renders filters and Task detail as a native compact bottom
  sheet and as a centered dialog in a wide layout. Director keeps modal state
  controlled so host dismissal restores the invoking surface.
- Home Project groups, Task detail actions, configuration controls, execution
  facts, Inspector states, and shared Worker controls stack and wrap. Compact
  Task rows keep Task and Status semantics while allowing text to grow instead
  of applying line clamps. Compact Board lanes have no fixed height.
- All actions are ordinary React Native presses. A 44-by-44 minimum target,
  touch hit slop, pressed feedback, keyboard focusability, and a token-colored
  focus outline come from the shared accessibility control. No action depends
  on hover, a pointer-specific menu, or an icon without a name.

## Assistive technology and preferences

Visible loading, empty, offline, stale, error, and mutation messages remain
live regions on platforms which expose them. Director also queues explicit
React Native accessibility announcements for iOS and Android and falls back to
the older announcement method supplied by React Native Web. Repeated identical
messages are not announced twice.

Task cards and rows expose Task key, title, derived state, priority, Workspace,
Epic, and the first actionable explanation. Tabs and filters expose selected,
expanded, busy, and disabled states. Actions name their exact effect and use a
hint where navigation, engine submission, or retry scope would otherwise be
unclear. Acceptance criteria and Activity events include their state, sequence,
time, and message in one accessible label.

Doctor checks and Repair effects expose their status, capability, detail, and
non-destructive/no-install guarantees as complete accessible labels. Doctor and
Repair controls use the same focus, target, pressed, busy, and disabled
semantics as every shared action. Read-only Doctor, Repair Preview/Apply,
refusal, and engine-readback outcomes are explicitly announced; reduced-motion
loading uses the same static indicator as the other surfaces.

Text retains React Native font scaling. Rows wrap, compact content has no line
clamp, detail grids collapse to one column, tab bars wrap, and status regions
grow vertically. Director adds no custom animation. When reduced motion is
enabled, rotating loading indicators become static token-colored icons. On
Android high-text-contrast or iOS darker-system-colors settings, control and
panel boundaries and the keyboard focus outline become heavier. Semantic text
and labels always accompany status color, so color is never the only carrier.

Every color comes from the active Paseo `PluginTheme`. The implementation has
no literal UI palette and recreates styles after the theme, compact layout, or
accessibility preference changes.

## Deterministic coverage

Focused tests cover the shared press target, pressed and focus states,
preference updates and cleanup, de-duplicated announcements, compact List-first
selection, one-lane Board rendering, native `Modal` use, large-text reflow
constraints, theme-token-only styling, no hover or custom animation, Task
modal/Inspector states, and preserved Paseo contribution boundaries.

## Manual accessibility checklist

Perform this checklist in an isolated exact Paseo `0.7.2` daemon with the
candidate-bound deterministic fixture. Capture desktop dark/light at
`1440x1000`, a responsive browser width, and compact `390x844` at device scale
1. Inspect every PNG at original resolution and require zero unexpected
browser console or page errors.

- [ ] Confirm Home, wide Board, wide List, Inspector, and Task Details,
  Execution, and Activity use the active dark and light theme without black
  default text, literal palette leakage, or duplicated Paseo chrome.
- [ ] Confirm compact first render is List, compact Board shows exactly one
  lane, and Filters and Task details use Paseo bottom sheets.
- [ ] With keyboard only, traverse in visual order, activate every control with
  Enter or Space, verify the token focus outline, keep focus trapped in each
  open dialog, dismiss with Escape, and verify focus returns to its opener.
- [ ] With touch emulation and no hover-capable pointer, open filters, select a
  lane, open a Task, switch detail tabs, and exercise every available or
  disabled action. Confirm targets remain at least 44 points and pressed state
  is visible.
- [ ] With a screen reader, confirm headings, Task summaries, tab selection,
  expanded filters, unavailable navigation, acceptance status, Activity order,
  and loading/empty/offline/stale/error announcements. Confirm repeated stable
  renders do not repeat announcements.
- [ ] At 200% browser text zoom or the largest native text setting, confirm
  controls and tab labels wrap, Task/List metadata remains readable, detail
  grids become one column, and all content is reachable without two-axis
  scrolling or clipped compact lanes.
- [ ] Emulate reduced motion and confirm loading uses static icons and Director
  introduces no transition or animation. Emulate forced/high contrast and
  confirm boundaries, text, selected state, warnings, errors, and focus remain
  distinguishable without relying on color alone.
- [ ] Verify loading, empty, offline, stale, and error states on Home and
  Board/List, plus Activity loading, empty, offline, stale, and error states in
  Task detail. Confirm cached data stays explicitly stale and no selected host
  falls through to another host.
- [ ] Verify Doctor healthy/degraded/blocking/loading/offline/stale/error and
  Repair Preview/confirmation/refused/success states in wide and compact
  layouts. Confirm Doctor remains read-only and every Repair action retains its
  exact engine-issued confirmation, busy, disabled, and announcement semantics.
- [ ] Verify Project Operations healthy/degraded/partial-sync/loading/offline/
  stale/error, Audit, bounded Logs, and support Preview/refusal/success states.
  Traverse every tab and manual control with keyboard and touch, confirm
  reduced-motion loading is static, and confirm support output remains local,
  mode `0600`, redacted, and never uploaded automatically.
- [ ] Verify Task close/back/gesture dismissal, Explorer Inspector, Open agent
  available/unavailable behavior, the composer pill, Return to Director Board,
  and Command Center items all resolve the exact native context.

Evidence is provisional until the planned final-base composition and exact-
Candidate recapture. Publication, review, CI authority, integration, cleanup,
and Task closure remain coordinator-owned workflow effects rather than product
UI preferences.
