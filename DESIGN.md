# Design

## Source of truth
- Status: Draft
- Last refreshed: 2026-05-21
- Primary product surfaces: `GET /` and `GET /ui` Helios Console, protected API calls behind the browser shell, operator case-detail inspection.
- Evidence reviewed:
  - `README.md` — product flow, Web UI workflow, configuration surface.
  - `docs/architecture/overview.md` — Helios as the incident workflow operator surface.
  - `internal/api/ui.go` — current browser console implementation.
  - `internal/api/ui_test.go` — public shell and protected API expectations.
  - User-provided Privy style reference — high-contrast digital architecture, light canvas, dark operational surfaces, single violet accent.
  - User-provided Romer-style dashboard reference — dark command dashboard, calm low-contrast panels, subdued borders, compact team operating cockpit.

## Brand
- Personality: precise, calm, architectural, command-center oriented, serious during incident response.
- Trust signals: visible case state, deterministic status labels, exact transaction hashes, explicit handoff and notification status, no decorative ambiguity.
- Avoid: marketing hero layouts, pure black/white hard contrast, pastel dashboards, rounded card stacks, and copy that explains obvious UI mechanics.

## Product goals
- Goals:
  - Let operators submit or inspect tx-backed analysis cases quickly.
  - Make queue, outcome, handoff, notification, artifact, GitHub publish, and Pre-Lumos sync state scannable.
  - Preserve enough metadata context for hack-detector submissions and manual cases.
- Non-goals:
  - Replace Grafana/Prometheus observability dashboards.
  - Become a generic BI dashboard.
  - Recreate Privy branding literally.
- Success signals:
  - Operators can identify whether a case is queued, running, done, failed, handed off, or retryable without opening raw JSON.
  - Manual case submission remains possible on small screens.
  - Case metadata and result payloads stay available for forensic detail.

## Personas and jobs
- Primary personas:
  - Incident operator monitoring incoming hack-detector signals.
  - Security engineer checking PoC/RCA progress and handoff state.
  - Developer validating Helios locally.
- User jobs:
  - Submit a chain + tx hash to start analysis.
  - Scan recent cases and choose the next case needing attention.
  - Inspect analysis result, GitHub publish links, Pre-Lumos output paths, events, metadata, handoff attempts, and notification attempts.
  - Retry handoff only when a completed case is retryable.
- Key contexts of use:
  - Local development at `127.0.0.1:18080/ui`.
  - Production behind TLS and Bearer-protected API calls.
  - Incident-response sessions where dense, low-noise status matters more than explanation.

## Information architecture
- Primary navigation: single-screen console; no multi-page navigation until case volume demands it.
- Core routes/screens:
  - `/` and `/ui`: public HTML shell.
  - Protected API calls from the shell: `POST /cases`, `GET /cases`, `GET /cases/{case_id}`, `POST /cases/{case_id}/retry-handoff`.
- Content hierarchy:
  - Top command bar: product identity, connection token, health action.
  - Left command panel: submit case and status feedback.
  - Right work area: case list first, selected case detail second.
  - Deep detail: analysis result and product side-effect summary before metadata/events/attempt logs.

## Design principles
- Principle 1: Operational density over marketing composition.
- Principle 2: Low-noise dark surfaces replace decoration; subtle borders, state chips, and layout rhythm create hierarchy.
- Principle 3: Actions stay close to the state they affect.
- Tradeoffs:
  - The UI favors compact scanability over empty space.
  - Raw JSON remains visible where precision matters, but summarized status appears first.

## Visual language
- Color:
  - Custom Background `#070708` for the app canvas.
  - Surface `#101112`, Panel `#111214`, and Sidebar `#0d0e0f` for calm command-dashboard surfaces.
  - Divider `#232426` and Divider Light `#1b1c1e` for low-contrast structure.
  - On Surface `#e5e2e3` for primary text and Muted Text `#9a9da3` for secondary text.
  - Primary Violet `#5e6bff`, Cyan `#50d8e9`, and Amber `#ffb689` only for active controls, positive/attention state, and priority state.
- Typography:
  - Use Inter/system sans for all UI text.
  - Use compact Manrope-like heading proportions with system sans fallback; do not load external fonts in the inline shell.
  - Implementation letter spacing is `0` to match repo frontend constraints; density comes from size, weight, and spacing.
- Spacing/layout rhythm:
  - Comfortable but dense: 8px, 10px, 12px, 16px, 20px, 24px, 32px.
  - Keep command controls grouped; keep tables compact.
- Shape/radius/elevation:
  - Cards/panels use 8px radius or less.
  - Buttons use compact 4px to 8px radius; avoid pill treatments unless the action needs strong separation.
  - Avoid heavy drop shadows. Depth comes from surface layering, low-contrast borders, and restrained inset highlights.
- Motion:
  - No decorative motion.
  - Preserve instant state updates and polling without animation dependencies.
- Imagery/iconography:
  - No illustration or decorative imagery.
  - Use simple text glyphs for navigation markers in the inline shell; add an icon library only if a frontend bundle exists.

## Components
- Existing components to reuse:
  - Inline `/ui` shell in `internal/api/ui.go`.
  - Existing status pills, case table, key/value detail, JSON blocks, retry handoff action.
- New/changed components:
  - Dark app shell with fixed-feel top command bar.
  - Left control rail for token and case submission.
  - Central incident queue with compact signal tiles and table.
  - Right intelligence/detail panel for selected case, analysis result, handoff, and notification attempts.
  - Dark inputs with violet focus treatment and low-contrast borders.
- Variants and states:
  - State chips: queued/running/done/handed-off/failed plus outcome values.
  - Status panel: neutral, success, and error.
  - Retry action: disabled unless `state=done` and `handoff_status=failed`.
- Token/component ownership:
  - Tokens live in the `/ui` CSS until a broader frontend bundle exists.
  - Any future component extraction should preserve these tokens first.

## Accessibility
- Target standard: WCAG AA for contrast and keyboard access.
- Keyboard/focus behavior: every button/input/select must expose visible violet focus outlines.
- Contrast/readability: dark surfaces use near-white text; muted text must remain secondary, not essential-only.
- Screen-reader semantics: preserve native form labels, buttons, tables, and headings.
- Reduced motion and sensory considerations: avoid decorative motion and flashing state changes.

## Responsive behavior
- Supported breakpoints/devices: desktop operations first, tablet and mobile fallback for local checks.
- Layout adaptations:
  - Desktop: two-column command/work layout.
  - Narrow screens: single-column flow with command panel before case list.
- Touch/hover differences:
  - Hover can darken table rows, but selection must not rely on hover alone.
  - Buttons keep large enough hit targets.

## Interaction states
- Loading: status panel uses concise progress text.
- Empty: case table and detail panel show a quiet empty state.
- Error: status panel uses dark red-tinted text treatment and raw error details when available.
- Success: status panel confirms submitted/loaded/retried case identifiers.
- Disabled: disabled buttons lower opacity and remove pointer cursor.
- Offline/slow network, if applicable: fetch errors surface in the status panel with returned JSON details.

## Content voice
- Tone: terse, operational, exact.
- Terminology:
  - Use "case", "analysis", "handoff", "notification", "outcome", "GitHub publish", "Pre-Lumos sync", "metadata", and "tx hash" consistently.
  - Distinguish Helios product UI from Grafana/Prometheus observability.
- Microcopy rules:
  - Prefer labels and state names over explanatory paragraphs.
  - Preserve raw identifiers exactly.
  - Avoid promotional claims.

## Implementation constraints
- Framework/styling system:
  - Current UI is a raw HTML/CSS/JS string in Go, served by `internal/api/ui.go`.
  - No frontend build pipeline is present.
- Design-token constraints:
  - Keep tokens as CSS custom properties in the shell.
  - Do not add dependencies or external font loading for this pass.
- Performance constraints:
  - Keep the shell self-contained and lightweight.
  - Avoid large client libraries.
- Compatibility constraints:
  - Public HTML shell remains available at `/` and `/ui`.
  - Protected API calls still require Bearer token.
- Test/screenshot expectations:
  - Run `go test ./internal/api` for shell and auth expectations.
  - For visual implementation passes, run a local Helios smoke and inspect `/ui` in browser where possible.

## Open questions
- [ ] Should hack-detector-submitted cases get a distinct source filter or inbox lane? Owner: product/ops. Impact: case list filtering and prioritization.
- [ ] Should the dashboard expose aggregate counts by state/outcome? Owner: product/ops. Impact: may require additional client aggregation or API support.
- [ ] Should production branding use a Helios logo/mark? Owner: product/design. Impact: top bar identity only.
