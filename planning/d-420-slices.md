---
shaping: true
---

# D-420 — Slices

Ground truth for slice breakdown. Shape **B** from `d-420-unify-app-shaping.md`.
Branch `refactor/d-420-viewing-layer-split` is stacked on #122 (`d6681ed`).

## V1 — Viewing-layer refactor in `cassini-viewer` (B3/B8)

**Goal:** split `App.svelte`'s two panes into reusable `MeetingList` + `MeetingView`
components composed by `App`, so `MeetingView` can later mount standalone (the
shareable single-meeting embed) and be imported by `cassini-app`. **Pure refactor —
no NC/build/route changes; standalone + share + web-archive behaviour identical.**

### Component boundary

**`App.svelte` keeps** (the orchestrator): `dataProvider`, catalog load, which-meeting-is-
selected (`selectedMeetingId`, `activeMeeting`, `catalogMeetings`), the **`meeting` hash
param** + its history (push/pop/seed), theme, responsive (`isDesktop`) + reduced-motion,
top-level layout/transitions, bundled-vs-catalog mode decision on mount.

**`MeetingList.svelte`** (presentational, left pane): props `meetings`, `selectedMeetingId`,
`ncMode`, `themeMode`, `errorMessage`; bindable `filter`. Emits `select(meeting)` and
`toggleTheme`. Owns only the list header/filter/cards/footer-error markup + the
filter/format helpers it uses.

**`MeetingView.svelte`** (smart, right pane — the reuse target): props `dataProvider`,
`meeting: MeetingCatalogEntry | null`, `bundled: boolean`, `ncMode`, `isDesktop`,
`prefersReducedMotion`. **Owns** artifact load for its `meeting` (or bundled), **playback**
(audio el + RAF clock + seek + follow-scroll + space key), **transcript switching** + the
**`tx` and `t` hash params** (per-meeting state — cleanly App-independent), and all
masthead/summary/transcript/metadata/footer markup. Emits `back` (mobile → list),
`enriched(meeting)` (real speaker/segment/duration counts → App updates the list card),
`loadError(message)`.

Hash-param split matches the entities: **App owns `meeting`; MeetingView owns `tx`+`t`.**
Both keep using `pushState`/`replaceState` fragment-only (unchanged mechanism).

### Files
- New `src/components/MeetingList.svelte`, `src/components/MeetingView.svelte`.
- `src/App.svelte` shrinks to orchestrator + composition of the two.
- Move view-only helpers (buildDisplaySegments, active-segment/token/word, timing/metadata
  formatters, continuation logic) into `MeetingView`; list helpers into `MeetingList`.
- `package.json`: add an `exports` map exposing `MeetingView`/`MeetingList` + the seam
  (`DataProvider`, `StaticCatalogProvider`) for later `cassini-app` consumption.

### Verification gates (must all stay green)
- `npm test` (module + embedded tests; count parity minus the known pre-existing reds).
- `npm run build`, `npm run build:embedded` (`assert-embedded-single-bundle: OK`).
- Manual render: standalone `npm run dev` — list→select→play→switch-transcript→back,
  deep-link `#meeting=…&tx=…&t=…`, browser back/forward. Bundled mode still view-only.

### Status — V1 COMPLETE (behaviour-neutral, verified)
- [x] MeetingList extracted (`src/components/MeetingList.svelte`), App composes it
- [x] MeetingView extracted (`src/components/MeetingView.svelte`) — owns load + playback + transcript switch + tx/t hash; App shrank to orchestrator (owns catalog/selection/meeting-param/theme/responsive)
- [x] Gates green: `svelte-check` 40/40 baseline (0 in new files) · tests 165/3 (baseline) · `build` ✓ · `build:embedded` ✓ single-bundle OK
- [x] Runtime verified (headless Chrome vs demo 6-meeting site): list renders 6 cards; deep-link `#meeting=…` loads meeting (209 segments + summary + player + masthead); desktop "Select a meeting" empty state; mobile/desktop layouts. **Behaviour-neutral confirmed** by diffing DOM against a fresh base-#122 build (identical output, incl. the pre-existing stale `formatArtifactMode` badge quirk).
- [~] `exports` map — **deferred to V2** (no consumer until `cassini-app` exists; adding it now would be an untested/unexercised surface). The interface shape is defined in V1 by the component props/events.

**One deliberate change (not a regression):** the audio-element `{#key}` now keys on `audioSrc` (was `selectedMeetingId`, which the extracted view no longer owns) — same remount-on-meeting-change semantics; transcript switches still don't remount (`applySwitchedTranscript` leaves `audioSrc` untouched).

## V2 — Workspace + `cassini-app` shell skeleton (B1/B2)  _(JS core done + verified; deploy wiring pending)_

Branch `refactor/d-420-app-shell-workspace`, stacked on V1. **npm workspace for now**
(pnpm swaps in via D-499; Chris OK'd npm scaffold as the interim foundation).

**Done + verified:**
- Root `package.json` (npm workspaces: `cassini-viewer`, `cassini-app`); `.gitignore` un-ignores it; stray `pnpm-lock.yaml` dropped.
- `cassini-viewer` = the **viewing layer**: `exports` (`App`, `MeetingView`, `MeetingList`, `dataProvider`, `app.css`) + `@source "./"` in `app.css` so its Tailwind classes are detected when a different-cwd build (cassini-app) bundles it.
- `cassini-control-panel` → **`cassini-app`** (`git mv`); its old operator `App.svelte` → `Operator.svelte` (dormant, for V3); depends on `cassini-viewer`.
- `cassini-app/src/App.svelte` = **shell** rendering `cassini-viewer/App.svelte` (browse surface) via the DataProvider seam. `embedded.ts` = viewer-derived NC bootstrap (served as the single `/ui/viewer.js`); `main.ts` + embedded pull `cassini-viewer/app.css`.
- **Gates green:** `cassini-app` `build` + `build:embedded` (single-bundle assert OK); `cassini-viewer` unchanged (165/3, `build:all` green). **Runtime:** merged `cassini-app` renders **pixel-identical** to the standalone viewer (list + deep-link load 209 segments + player, fully styled) — headless Chrome vs the 6-meeting demo.

**Remaining (deploy wiring — Chris: "not close to deploying"):**
- `exapp.go`: collapse `topMenuEntries` to one "Cassini" (`AdminRequired:0`); serve `/ui/viewer.js` from `cassini-app`'s dist; drop the control-panel entry.
- `appinfo/info.xml`: drop `/ui/control-panel.*` asset routes (keep operator JSON API `ADMIN`).
- `Dockerfile.exapp`/`.cuda`: `cassini-app` build stage (needs the workspace — copy both packages); `CASSINI_*_DIST`.
- `Dockerfile.control-panel`, `compose*.yml`, CI workflows: rename/reconcile.

**Debt to clean (noted):** the viewer-derived `embedded.ts` duplicates the viewer's bootstrap — extract a shared, parameterizable bootstrap into the viewing layer when V3 needs the operator-base capture too; `cassini-app` has no unit tests currently (`--passWithNoTests`) since the old control-panel `embedded.test.ts` tested now-removed operator-embedded code — tests return with V3. Also: with the npm workspace root, `cd cassini-viewer && npm test` wraps the exit code noisily (cosmetic; pnpm/D-499 resolves).

## V3 — Operator surface + hash-`surface` nav + 403 probe (B4/B5/B6/B7)  _(not started)_
## V4 — Formalise shareable single-meeting embed (R2/R3)  _(not started)_
