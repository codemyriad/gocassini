---
shaping: true
---

# Initiative: MVP

This document turns `planning/initiatives/mvp/venture.md` into the working execution shape for the first MVP.

## Working position

The venture brief defines the business frame: **Cassini is a Nextcloud-first meeting recorder and artifact system for privacy-conscious teams**.

For execution, the repo already suggests the right backbone:

- `./bin/cassini` is the product entry point
- `cassini record` / `build` / `publish` / `serve` already define the core artifact flow
- the portable meeting file and static meeting library are the product boundary we should build around

So the MVP should **not** start by building a large new product shell. It should start by making the existing meeting-to-artifact flow deployable, triggerable, and pilot-friendly.

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | Produce an end-to-end Nextcloud Talk meeting-to-artifact flow that works on a real call and is credible in pilots and partner demos. | Core goal |
| R1 | The MVP must be self-hostable by privacy-conscious teams using owned storage, with a documented deployment path. | Must-have |
| R2 | The MVP must stay Nextcloud Talk-only and avoid expanding into a general meeting platform, calendar product, or hosted control plane. | Must-have |
| R3 | An operator must be able to trigger a recording without SSHing into the box or manually stitching recorder, build, and publish steps together. | Must-have |
| R4 | The published meeting library and single-meeting viewer must be credible as the MVP delivery surface across desktop and mobile. | Must-have |
| R4.1 | Users must be able to open a stable URL and browse at least a small library of past meetings, with transcript search inside a meeting. | Must-have |
| R4.2 | The viewer must adopt a maintainable stock design-system foundation with coherent light/dark themes while preserving current interaction behavior. | Must-have |
| R4.3 | The viewer must separate Meeting List and Meeting View contexts on narrow screens, while keeping URL-driven deep linking, back/forward behavior, and a clean desktop empty state. | Must-have |
| R5 | The delivered artifact must include the recording, transcript, captions/readable transcript, and one minimal summary output. | Must-have |
| R6 | MVP implementation must reuse the current `cassini` CLI and artifact contracts instead of rebuilding capture, transcription, and publishing from scratch. | Must-have |
| R7 | Failures must be inspectable and rerunnable without manual artifact repair, using persisted job/artifact state and operator-facing logs/status. | Must-have |
| R8 | Setup documentation must clearly state deployment steps, hardware/model expectations, and which concerns remain the operator's responsibility (for example auth and retention). | Must-have |

---

## CURRENT: Existing repo baseline

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | Product CLI already exists as `./bin/cassini` with `record`, `build`, `publish`, `serve`, `inspect`, and `doctor`. | |
| **CURRENT2** | Recording is already grounded in a real Nextcloud Talk capture path and local harness. | |
| **CURRENT3** | Publish and serve paths already exist for a static meeting library (`catalog.json` + per-meeting artifacts). | |
| **CURRENT4** | Readable transcript generation already exists when the configured cleanup backend is available. | |
| **CURRENT5** | The viewer/library browser surface is verified and working: meeting list, single-meeting page, audio playback, transcript click-to-seek, and transcript search all function against published output. | |
| **CURRENT6** | There is no MVP deployment bundle yet for self-hosting the product flow as a service. | |
| **CURRENT7** | There is no lightweight trigger surface yet for "invite the bot" / start-a-recording workflows. | |
| **CURRENT8** | There is no operator-facing job runner/status surface wrapping the existing CLI pipeline. | |
| **CURRENT9** | There is no explicit minimal meeting-summary artifact contract or summary delivery path defined as MVP behavior. The viewer has no summary panel or summary UX yet. | |
| **CURRENT10** | Documentation is developer-oriented; pilot/self-host docs are not yet the product onboarding path. | |
| **CURRENT11** | Viewer styling is still bespoke CSS inside `App.svelte`, with no shared design-system layer and no light/dark theme model. | |
| **CURRENT12** | The viewer still uses a single always-visible sidebar/content layout, with no Meeting List / Meeting View split, no mobile-aware place swap, and no browser-history-driven in-app navigation. | |

---

## A: CLI-first operator workflow

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Package the existing CLI and dependencies into a self-hostable deployment bundle. | |
| **A2** | Operator starts each recording manually by running `cassini record` with the meeting URL over SSH or local shell access. | |
| **A3** | Operator runs or scripts `cassini publish` to refresh the static meeting library. | |
| **A4** | Document the flow as a pilot-only manual operating procedure. | |
| **A5** | Extend the existing static viewer in place for summary, design-system, and responsive navigation improvements without changing the CLI-first control model. | |

## B: Webhook worker around the existing Cassini pipeline

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | Package the current product flow into a self-hostable deployment bundle with shared storage for recordings, published site output, caches, and logs. | |
| **B2** | Add a small HTTP trigger service that accepts meeting URL plus minimal metadata, creates a job, and starts the background Cassini pipeline. | |
| **B3** | Run `cassini doctor`, `cassini record`, and publish/export steps from the worker so the existing CLI remains the execution backbone. | |
| **B4** | Keep the existing published static meeting library/viewer as the delivery base, preserving current behaviors while extending it in place for summary, design-system, and navigation improvements. | |
| **B5** | Add one minimal summary output to the post-processing path and include it in the exported meeting artifact. | |
| **B6** | Persist job status, logs, and rerun inputs so failed meetings can be inspected and retried without manual artifact surgery. | |
| **B7** | Ship pilot-oriented docs covering deployment, hardware expectations, auth/reverse-proxy assumptions, and storage ownership. | |
| **B8** | Refactor viewer styling onto stock DaisyUI/Tailwind with coherent light/dark theming and no bespoke CSS layer. | |
| **B9** | Split the viewer into Meeting List / Meeting View places with URL-driven navigation, desktop/mobile responsive behavior, and a back-to-list flow on narrow screens. | |

## C: Nextcloud app first

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **C1** | Build a dedicated Nextcloud app/UI for inviting Cassini directly from Talk. | |
| **C2** | Put meeting trigger, status, and storage wiring behind that new app boundary from day one. | |
| **C3** | Expand early into Nextcloud-specific permissions, notifications, and packaging/store concerns. | |
| **C4** | Make the app the main MVP surface instead of the existing Cassini CLI and artifact flow. | |
| **C5** | Preserve the current static viewer as the artifact-consumption surface while layering viewer-shell improvements separately from the Nextcloud app work. | |

---

## Fit Check

| Req | Requirement | Status | A | B | C |
|-----|-------------|--------|---|---|---|
| R0 | Produce an end-to-end Nextcloud Talk meeting-to-artifact flow that works on a real call and is credible in pilots and partner demos. | Core goal | ✅ | ✅ | ✅ |
| R1 | The MVP must be self-hostable by privacy-conscious teams using owned storage, with a documented deployment path. | Must-have | ✅ | ✅ | ✅ |
| R2 | The MVP must stay Nextcloud Talk-only and avoid expanding into a general meeting platform, calendar product, or hosted control plane. | Must-have | ✅ | ✅ | ❌ |
| R3 | An operator must be able to trigger a recording without SSHing into the box or manually stitching recorder, build, and publish steps together. | Must-have | ❌ | ✅ | ✅ |
| R4 | The published meeting library and single-meeting viewer must be credible as the MVP delivery surface across desktop and mobile. | Must-have | ✅ | ✅ | ✅ |
| R4.1 | Users must be able to open a stable URL and browse at least a small library of past meetings, with transcript search inside a meeting. | Must-have | ✅ | ✅ | ✅ |
| R4.2 | The viewer must adopt a maintainable stock design-system foundation with coherent light/dark themes while preserving current interaction behavior. | Must-have | ✅ | ✅ | ✅ |
| R4.3 | The viewer must separate Meeting List and Meeting View contexts on narrow screens, while keeping URL-driven deep linking, back/forward behavior, and a clean desktop empty state. | Must-have | ✅ | ✅ | ✅ |
| R5 | The delivered artifact must include the recording, transcript, captions/readable transcript, and one minimal summary output. | Must-have | ✅ | ✅ | ✅ |
| R6 | MVP implementation must reuse the current `cassini` CLI and artifact contracts instead of rebuilding capture, transcription, and publishing from scratch. | Must-have | ✅ | ✅ | ❌ |
| R7 | Failures must be inspectable and rerunnable without manual artifact repair, using persisted job/artifact state and operator-facing logs/status. | Must-have | ❌ | ✅ | ❌ |
| R8 | Setup documentation must clearly state deployment steps, hardware/model expectations, and which concerns remain the operator's responsibility (for example auth and retention). | Must-have | ✅ | ✅ | ✅ |

**Notes:**
- A fails R3: it still depends on shell/SSH access and per-meeting operator handling.
- A fails R7: resumable artifacts exist, but there is no job wrapper that preserves request context, status, and rerun flow as an operator surface.
- C fails R2: it expands the MVP into a larger Nextcloud product/program rather than proving the narrow artifact flow first.
- C fails R6: it shifts the center of gravity away from the existing Cassini CLI/artifact contracts.
- C fails R7: it adds a new application boundary before the simpler worker/rerun model is proven.

---

## Selected shape

**Selected shape: B — Webhook worker around the existing Cassini pipeline**

Why B fits best:

- it preserves the current repo's strongest asset: the existing `cassini` artifact flow
- it adds the missing MVP surface: a lightweight way to start recordings remotely
- it keeps the user-facing delivery simple: static meeting pages and a small meeting library
- it avoids prematurely turning MVP into a full Nextcloud app, orchestration system, or SaaS control plane

---

## B: Webhook worker around the existing Cassini pipeline

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | **Self-host bundle** | |
| B1.1 | Package services in one deployment bundle: trigger service, worker, static site server, and shared volumes for recordings/published output/logs/model caches. | |
| B1.2 | Define the owned-storage layout so artifacts persist outside container lifecycles and remain inspectable by operators. | |
| **B2** | **Trigger surface** | |
| B2.1 | Expose a small HTTP endpoint that accepts the Talk URL plus optional title/tags and returns a job identifier immediately. | |
| B2.2 | Validate input, create a job record/directory, and enqueue background execution instead of holding the request open for the full meeting. | |
| **B3** | **Worker on top of existing Cassini commands** | |
| B3.1 | Run `cassini doctor` as preflight before expensive work starts. | |
| B3.2 | Run `cassini record --call <url> --out <recordings>/<meeting>.opus` as the primary capture/build step. | |
| B3.3 | Refresh the publish output from the processed meetings root so the static library stays current after successful jobs. | |
| **B4** | **Artifact delivery baseline** | |
| B4.1 | Serve the exported static meeting library from a stable URL backed by `catalog.json` and per-meeting artifact directories. **Already working.** | |
| B4.2 | Keep scope to single-meeting transcript search plus browseable meeting list; no cross-meeting semantic search in MVP. **Already working.** | |
| **B5** | **Minimal summary output** | |
| B5.1 | Define one simple summary artifact shape for MVP, authored as markdown with stable headings/sections so design and generation target the same contract. | |
| B5.2 | Build the summary display UX in the viewer against seeded/demo summary artifacts so design can proceed without waiting for LLM integration. | |
| B5.3 | Generate that same summary artifact from the finished transcript with an API-first LLM backend. | |
| B5.4 | Include summary data in the meeting artifact/viewer path with a clear fallback when summary generation is disabled or fails. | |
| **B6** | **Failure and rerun model** | |
| B6.1 | Persist per-job request metadata, command logs, artifact paths, and terminal status for inspection. | |
| B6.2 | Support rerunning failed post-processing/publish work from preserved artifacts instead of requiring a fresh recording when not necessary. | |
| **B7** | **Pilot docs and guardrails** | |
| B7.1 | Write a quickstart for deployment, reverse-proxy/auth expectations, and hardware/runtime requirements. | |
| B7.2 | State explicit MVP exclusions: chat/RAG, multi-platform capture, calendar automation, billing, and packaged Nextcloud app/store work. | |
| **B8** | **Viewer design-system foundation** | |
| B8.1 | Install Tailwind + DaisyUI and migrate `App.svelte` styling from bespoke CSS to stock Daisy utilities/primitives in place, without changing handlers or component structure. | |
| B8.2 | Enable stock DaisyUI `light` + `dark` themes via `<html data-theme>`, default to `prefers-color-scheme`, and persist explicit user choice in `localStorage['cassini-theme']`. | |
| B8.3 | Retire the old radial-gradient / serif palette and remove bespoke `<style>` blocks so the viewer ships on the shared design-system foundation. | |
| **B9** | **Viewer place split and URL navigation** | |
| B9.1 | Split the browser surface into dedicated Meeting List and Meeting View regions: side-by-side on desktop, one-at-a-time on mobile. | |
| B9.2 | Keep warning banner and theme toggle in Meeting List; move transcript, metadata, summary, and player into Meeting View with a sticky mobile back-to-list affordance. | |
| B9.3 | Make the URL the navigation source of truth for meeting selection: `pushState` on user selection, `replaceState` on hydration, `popstate` reconciliation, and `#t=<ms>` handling. | |
| B9.4 | Anchor the player to the Meeting View bottom edge and show a minimal desktop empty state when no meeting is selected. | |

---

## Detail B: Concrete affordances

This breadboard details the selected shape as one end-to-end system: trigger a meeting job, run the existing Cassini pipeline, refresh the published meeting library, and consume the result from the browser.

### Places

| # | Place | Description |
|---|-------|-------------|
| P1 | Cassini Control Backend | Trigger API, worker, storage, summary generation, and static file serving. |
| P2 | Meeting List (browser) | Dedicated library/list place. Hosts the meeting catalog, warning banner, and theme toggle. |
| P3 | Meeting View (browser) | Dedicated single-meeting place with summary, transcript, metadata, and player. Side-by-side with P2 on desktop; shown one-at-a-time on mobile. |

### Workflow Guide

| Step | Action | Where to look |
|------|--------|---------------|
| **1** | Operator triggers a recording job | N1 → N2 → N3 → N4 |
| **2** | Worker runs capture, summary, and publish refresh | N4 → N5 → N6 → N7 → N9 |
| **3** | User opens the meeting list | N13 → N10 → U1 |
| **4** | User selects a meeting and enters Meeting View | U2 → N19 → N14 |
| **5** | User searches transcript or clicks a segment to seek audio | U5 → N15 → U6, and U7 → N16 → U4 |
| **6** | Mobile user returns from Meeting View to Meeting List | U10 → N19 → U1 |

### UI Affordances

| # | Place | Component | Affordance | Control | Wires Out | Returns To | Status |
|---|-------|-----------|------------|---------|-----------|------------|--------|
| U1 | P2 | viewer | meeting list | render | → U2 | — | ✅ Exists |
| U2 | P2 | viewer | meeting row | click | → N19, → P3 | — | ✅ Exists, extended in V8 |
| U3 | P2 | viewer | empty library state | render | — | — | ✅ Exists |
| U4 | P3 | viewer | audio player | render | — | — | ✅ Exists, repositioned in V8 |
| U5 | P3 | viewer | transcript search input | type | → N15 | — | ✅ Exists |
| U6 | P3 | viewer | transcript panel | render | → U7 | — | ✅ Exists |
| U7 | P3 | viewer | transcript segment | click | → N16 | — | ✅ Exists |
| U8 | P3 | viewer | summary panel | render | — | — | New (V3) |
| U9 | P2 | viewer | theme toggle | click | → N17 | — | New (V7) |
| U10 | P3 | viewer | back-to-list button | click | → N19 | → U1 | New (V8) |
| U11 | P3 | viewer | desktop empty state | render | — | — | New (V8) |
| U12 | P2 | viewer | warning banner | render | — | — | ✅ Exists, repositioned in V8 |

### Code Affordances

| # | Place | Component | Affordance | Control | Wires Out | Returns To | Status |
|---|-------|-----------|------------|---------|-----------|------------|--------|
| N1 | P1 | trigger-api | `POST /jobs` handler | call | → N2, → N3 | — | New (V1) |
| N2 | P1 | trigger-api | `validateTriggerRequest()` | call | — | → N1 | New (V1) |
| N3 | P1 | jobs | `job state` write | write | → S1 | → N4, → N11, → N12 | New (V1) |
| N4 | P1 | worker | job runner subscription | observe | → N5, → N3, → N6, → N7, → N9 | — | New (V1) |
| N5 | P1 | worker | `cassini doctor` | call | — | → N4 | New (V2) |
| N6 | P1 | worker | `cassini record/build artifact` | call | → S2 | → N4 | New (V2) |
| N7 | P1 | worker | `buildMeetingSummary()` | call | → N8, → S2 | → N4 | New (V4) |
| N8 | P1 | summary-provider | summary model/API call | call | — | → N7 | New (V4) |
| N9 | P1 | publisher | `refreshPublishedLibrary()` | call | → S2, → S3 | → N4 | New (V1) |
| N10 | P1 | site-server | `servePublishedFiles()` | call | → S3 | → N13, → N14 | ✅ Exists |
| N11 | P1 | trigger-api | `GET /jobs/:id` | call | → S1 | — | New (V1) |
| N12 | P1 | trigger-api | `POST /jobs/:id/rerun` | call | → N3, → N4 | — | New (V5) |
| N13 | P2 | viewer | `fetchCatalog()` | call | → N10 | → U1, → U3 | ✅ Exists |
| N14 | P3 | viewer | `loadMeetingArtifact()` | call | → N10 | → U4, → U6, → U8, → N15 | ✅ Exists, extended in V3 and V8 |
| N15 | P3 | viewer | `filterTranscript(query)` | call | — | → U6 | ✅ Exists |
| N16 | P3 | viewer | `seekAudio(time)` | call | — | → U4 | ✅ Exists |
| N17 | P2 | viewer | `setTheme(nextTheme)` | call | → S4, → S5 | → U9 | New (V7) |
| N18 | P2 | viewer | `syncThemeFromSystem()` | observe | → S4 | → U9 | New (V7) |
| N19 | P2/P3 | viewer | `writeMeetingSelectionUrl()` | call | → S6 | → N14, → U1 | New (V8) |
| N20 | P2/P3 | viewer | `reconcileMeetingSelectionFromUrl()` | observe | → S6, → N14 | → U1, → P3 | New (V8) |

### Data Stores

| # | Place | Store | Description |
|---|-------|-------|-------------|
| S1 | P1 | `job records` | Request payload, status, logs, and artifact paths for each trigger job. |
| S2 | P1 | `meeting artifacts` | Processed meeting outputs used as publish inputs, including transcript/readable data and summary output. |
| S3 | P1 | `published site root` | Static site output (`index.html`, `catalog.json`, `meetings/*`) served to browsers. **Already exists.** |
| S4 | P2 | `theme mode` | Active DaisyUI theme state used by the viewer (`light`, `dark`, or system-derived default). |
| S5 | P2 | `localStorage['cassini-theme']` | Persisted explicit user theme choice when it differs from system default. |
| S6 | P2/P3 | `browser URL/history` | `?meeting=<id>` + optional `#t=<ms>` and browser history state that drive Meeting List / Meeting View navigation. |

### What this breadboard clarifies

- The publish/serve/browser foundation is verified and working, but V7 and V8 are now explicit viewer-shell slices rather than “already done” scope.
- The MVP still does **not** need a large new operator UI; the first operator surface can remain the trigger/status API.
- The worker stays thin by orchestrating the existing Cassini flow rather than reimplementing capture or publishing.
- Summary work still splits cleanly into two slices: the designer defines the markdown shape and summary panel UX first (V3), then backend generation targets that contract (V4).
- V7 is the styling/theming foundation for all subsequent viewer work; V8 is the place/navigation refinement on top of that foundation.
- Static publish/serve remains in place, so summary and viewer-shell work do not require a new server implementation.
- Summary generation is a post-processing step that enriches the meeting artifact before publish refresh.
- Rerun capability belongs in the same trigger/backend surface as job status because both depend on persisted job records and artifact paths.

---

## Execution implications of shape B

The implementation breakdown lives in:

- `planning/initiatives/mvp/slices.md`

The remaining MVP work (V0–V8):

0. **V0 — Prep** — commit two full demo meeting artifacts and a summary `.md` template to the repo. Unblocks V3 and V4.
1. **V1 — Trigger + jobs** — add the HTTP/job surface so recording can be started remotely.
2. **V2 — Live capture** — wire the worker to the real Cassini recording pipeline.
3. **V3 — Summary display UX** — build the summary panel in the viewer against V0 demo data on top of V7's design-system foundation.
4. **V4 — Summary generation** — extend the core Cassini pipeline with LLM summary generation in V0 template format.
5. **V5 — Failure handling** — make failed jobs inspectable and rerunnable.
6. **V6 — Packaging + docs** — bundle everything into a deployable, documented MVP.
7. **V7 — Viewer design-system foundation** — migrate the viewer to stock DaisyUI/Tailwind + light/dark themes while preserving current interactions. Blocks V3.
8. **V8 — Viewer UX restructure** — split Meeting List / Meeting View, add mobile back-to-list flow, and make URL/history the navigation source of truth.

V0, V1, and V7 start in parallel. V3 starts after both V0 and V7. V4 starts after V0. V2 and V5 start after V1. V8 starts after V7. V6 starts last after the rest stabilize.

---

## Out of scope for the first MVP

These may be good follow-ons, but they are not required for MVP completion:

- LLM chat over meeting history / RAG
- cross-meeting semantic search
- Google Meet or Jitsi support
- full calendar automation
- packaged Nextcloud marketplace app
- billing / hosted multi-tenant control plane
- real-time transcription or stream processing
- Cassini-managed auth beyond documenting reverse-proxy/operator responsibility

---

## MVP done criteria

We should call the MVP done when all of the following are true:

1. A self-hosting operator can deploy Cassini with a documented setup path.
2. The operator can start a Nextcloud Talk recording through the lightweight trigger surface.
3. The worker completes the meeting-to-artifact flow using the existing Cassini pipeline, without manual artifact repair.
4. The resulting hosted output includes a recording, transcript, readable transcript/captions, and one minimal summary.
5. Users can open a stable URL, browse published meetings, open a single meeting page, search within that meeting, and navigate cleanly across desktop/mobile with working deep links and browser back/forward behavior.
6. Failed runs leave enough job/artifact state to inspect and rerun them.
7. The docs are good enough for a serious self-hosted pilot conversation.
8. The viewer ships on the refactored DaisyUI light/dark theme foundation and the split Meeting List / Meeting View shell, rather than the older bespoke single-layout UI.
