---
shaping: true
---

# Viewer UX Restructure — Shaping

Follow-on to V7. Splits the viewer into two distinct areas — **Meeting List** and **Meeting View** — that sit side-by-side on desktop and transition one-at-a-time on mobile.

## Source

From the user (verbatim):

> Here are the key improvements to the UX that need shaping:
>
> 1. The meeting list, this needs to become its own area, not baked into the layout of the meeting artefact. This will become the default loading screen on mobile, while on desktop it will appear on the left side and the meeting area will have an empty state. I call it a sidebar, but its just more of a transition between these two areas, you need to select a meeting to open it, then once on the meeting you can navigate back to the meeting list to select another.
> 2. Create a content area for the meeting artefacts, move the metadata header, the transcript, the player into this area. This will be on the right side but take up the majority width.
> 3. With these two changes, mobile then cascades nicely, sidebar area to select the meeting, main content area to view the meeting info.

## Problem

Today the meeting catalog lives as a panel inside `<aside class="sidebar">`, alongside the warning banner (and, post-V7, the theme toggle). The `<main>` meeting content area renders a "Select a meeting..." placeholder when none is selected.

The sidebar is always visible. At narrow viewports it eats horizontal space from the transcript it's meant to support. There's no separation between "browsing the library" and "viewing a meeting" as contexts — they're welded together, which is fine on desktop and increasingly bad as the viewport narrows.

## Outcome

The viewer has two distinct **places**:

- **Meeting List** — focused catalog view where you browse and select a meeting.
- **Meeting View** — metadata + transcript + player for the selected meeting.

Behavior:

- **Desktop (≥ breakpoint)**: both places visible side-by-side. Meeting List on the left (fixed width), Meeting View on the right (majority width). No meeting selected → Meeting View shows an empty state.
- **Mobile (< breakpoint)**: one place visible at a time. Initial load shows Meeting List. Selecting a meeting transitions to Meeting View. A "back to list" affordance in Meeting View returns to Meeting List.

The warning banner and theme toggle live in the Meeting List (they're always present, not coupled to a selected meeting).

## Requirements (R)

| ID | Requirement | Status |
|---|---|---|
| **R0** | Meeting List is its own place, not embedded inside the Meeting View layout | Core goal |
| **R1** | **Desktop behavior** | |
| R1.1 | Both places visible side-by-side; Meeting View on the right takes majority width | Core goal |
| R1.2 | Empty state is a minimal centered message ("Select a meeting to view") when no meeting is selected | Must-have |
| **R2** | **Mobile behavior** | |
| R2.1 | One place visible at a time; Meeting List is the default; selecting a meeting transitions to Meeting View; back returns | Core goal |
| R2.2 | Back-to-list button is placed **top-left** of Meeting View and is **sticky/fixed** so it's always accessible regardless of transcript scroll position | Must-have |
| R2.3 | Transitions between Meeting List and Meeting View use a **slide/push** animation (native-app feel), not an instant swap | Must-have |
| **R3** | **Preservation** | |
| R3.1 | All existing interactions preserved — click-to-seek, playback, scroll-lock, theme toggle, warning rendering, meeting selection | Must-have |
| R3.2 | All existing vitest + `audit:portable-token` tests pass | Must-have |
| R3.3 | Static-build SPA shape preserved — one `index.html`, one bundle, static-hostable | Must-have |
| R3.4 | Player unmounts on return to Meeting List — does not persist across contexts | Must-have |
| **R4** | **Foundation constraints** | |
| R4.1 | No routing library added — use URL query params (already in place via `?meeting=`) or pure state | Must-have |
| R4.2 | Builds on V7's DaisyUI foundation — layout uses Tailwind responsive utilities + Daisy primitives where appropriate | Must-have |
| R4.3 | Breakpoint is **`md` = 768px** (Tailwind standard) — the single split point between mobile and desktop behavior | Must-have |
| **R5** | Mobile browser back button returns to Meeting List, not exits the viewer | Must-have |
| **R6** | Deep linking — `?meeting=<id>` (and optional `#t=<ms>` seek) lands correctly; refresh preserves; sharing a URL works | Must-have |

## Shapes

### CURRENT: Single-page with sidebar + main

| Part      | Mechanism                                                                             |
| --------- | ------------------------------------------------------------------------------------- |
| CURRENT.1 | `.shell` → `.masthead` + `.layout` (`.sidebar` + `.main`) + `.player-dock` footer     |
| CURRENT.2 | Meeting catalog is a panel inside the sidebar                                         |
| CURRENT.3 | `<main>` shows "Select a meeting..." when nothing loaded, meeting content when loaded |
| CURRENT.4 | `.sidebar` is always visible at every viewport width                                  |
| CURRENT.5 | `?meeting=<id>` URL param is read on initial load to pre-select a meeting             |

---

### A: CSS-responsive, state-driven

Both places always present in the DOM. Visibility driven by viewport breakpoint + `selectedMeetingId` state via Tailwind responsive utilities.

| Part | Mechanism | Flag |
|---|---|:---:|
| **A1** | **Two sibling regions in App.svelte** | |
| A1.1 | `<section class="meeting-list">` — catalog, warning, theme toggle | |
| A1.2 | `<section class="meeting-viewer">` — back button, masthead, transcript, metadata, player | |
| **A2** | **Layout** — Tailwind `grid-cols-1 md:grid-cols-[340px_1fr]` on the outer container. Breakpoint = `md` (768px) per R4.3. | |
| **A3** | **Mobile visibility** — at `<md`, visibility toggled by `selectedMeetingId`: null → list only; set → view only. Implemented with `md:block` / `hidden` + state classes. Transitions use Svelte `fly` with `x: 100%` / easing to produce the slide/push animation (R2.3). | |
| **A4** | **Desktop empty state** — at `≥md` with no selection, Meeting View renders a minimal centered message "Select a meeting to view" (R1.2). No welcome block, no masthead — just the empty shell. | |
| **A5** | **"Back to list" affordance** — icon button (Daisy `btn btn-ghost btn-sm` with a chevron-left icon) placed **top-left of Meeting View**, **sticky** (`sticky top-0` within the scroll container, or fixed with z-index) so it's always visible while scrolling the transcript. Visible only at `<md` (desktop doesn't need it since both regions are visible). Clears `selectedMeetingId` on click. (R2.2) | |
| **A6** | **URL sync** — differentiated write strategy: (a) **initial hydration** — if page loads with `?meeting=<id>` already present, use `replaceState` to normalize (no spurious history entry). (b) **user-initiated selection** — `pushState` with `?meeting=<id>` so browser back creates a navigable history entry. (c) **back-to-list** — call `history.back()` (not a direct state mutation); popstate then drives the state change. | |
| **A7** | **popstate listener** — on back/forward, parse the current URL, diff against current `selectedMeetingId`, and reconcile. Guard against duplicate loads (compare before dispatching). Re-applies `#t=<ms>` hash as `pendingSeekMs` if present on the new URL. (R5, R6) | |
| **A6a** | **Hash (`#t=<ms>`) behavior on meeting change** — cleared when selection changes (timestamp is meeting-scoped). Preserved on refresh (part of the deep link). | |
| **A6b** | **Invalid meeting ID in URL** — existing behavior carries over: error banner shows "Meeting not found in catalog: X", stays on Meeting List. No crash, no fallback auto-select unless catalog has exactly one meeting (existing behavior). | |
| **A8** | **Player footer placement** — lives inside the Meeting View region (not global). Unmounts when the user returns to Meeting List, so audio stops and DOM is clean (R3.4). | |
| **A9** | **Warning + ThemeToggle placement** — stay in Meeting List region (they're context-independent of any specific meeting). | |

### B: URL-first master-detail

URL is the source of truth. State derives from URL. Same DOM structure as A, but the data flow is inverted — clicks change URL, URL changes drive state, state drives rendering.

| Part   | Mechanism                                                                                                                  | Flag |
| ------ | -------------------------------------------------------------------------------------------------------------------------- | :--: |
| **B1** | Same two regions as A1                                                                                                     |      |
| **B2** | Same Tailwind responsive grid as A2                                                                                        |      |
| **B3** | **URL as primary state** — `selectedMeetingId` is a derived read from `?meeting=<id>`; not an independent writable store   |      |
| **B4** | Meeting card click → `history.pushState({meeting: id})` → `popstate` handler reads URL → state derives → rendering follows |      |
| **B5** | Back-to-list at `<md` → `history.back()` (not a direct state mutation)                                                     |      |
| **B6** | Same empty state as A4                                                                                                     |      |
| **B7** | Same player footer placement as A8                                                                                         |      |

### C: Daisy Drawer component

Uses DaisyUI's `drawer` primitive directly — designed exactly for side-by-side / overlay-on-mobile patterns.

| Part   | Mechanism                                                                                      | Flag |
| ------ | ---------------------------------------------------------------------------------------------- | :--: |
| **C1** | Wrap layout in `<div class="drawer md:drawer-open">`                                           |      |
| **C2** | Meeting list goes in `<div class="drawer-side">`                                               |      |
| **C3** | Meeting view goes in `<div class="drawer-content">`                                            |      |
| **C4** | Mobile: drawer opens as an **overlay** on top of meeting view, triggered by a hamburger button |  ⚠️  |
| **C5** | Desktop: `drawer-open` class keeps drawer persistently visible                                 |      |

⚠️ **C4 is the reason C doesn't quite fit the user's mental model.** The drawer pattern _overlays_ temporarily — the meeting view stays behind it. The user described a _transition between two areas_ ("you need to select a meeting to open it, then once on the meeting you can navigate back"). That's master-detail, not drawer.

---

## Fit Check

Re-evaluated after decisions. All of A's parts (A1–A9) are now mandatory, including URL sync (A6+A7).

| Req | Requirement | Status | A | B | C |
|---|---|---|:---:|:---:|:---:|
| R0 | Meeting List is its own place | Core goal | ✅ | ✅ | ❌ |
| R1.1 | Desktop side-by-side, Meeting View majority width | Core goal | ✅ | ✅ | ✅ |
| R1.2 | Desktop empty state — minimal centered message | Must-have | ✅ | ✅ | ✅ |
| R2.1 | Mobile single place, list default, transitions | Core goal | ✅ | ✅ | ❌ |
| R2.2 | Back-to-list sticky top-left | Must-have | ✅ | ✅ | ❌ |
| R2.3 | Slide/push transition animation | Must-have | ✅ | ✅ | ❌ |
| R3.1 | Interactions preserved | Must-have | ✅ | ✅ | ✅ |
| R3.2 | Tests pass | Must-have | ✅ | ✅ | ✅ |
| R3.3 | Static SPA shape | Must-have | ✅ | ✅ | ✅ |
| R3.4 | Player unmounts on return to list | Must-have | ✅ | ✅ | ⚠️ |
| R4.1 | No routing library | Must-have | ✅ | ✅ | ✅ |
| R4.2 | Builds on V7 (Tailwind + Daisy) | Must-have | ✅ | ✅ | ✅ |
| R4.3 | Breakpoint = `md` (768px) | Must-have | ✅ | ✅ | ✅ |
| R5 | Mobile back returns to list | Must-have | ✅ | ✅ | ❌ |
| R6 | Deep linking (+ hash) | Must-have | ✅ | ✅ | ❌ |

**Notes:**

- **C** fails R0, R2.1, R2.2, R2.3, and R5 — the drawer overlay model doesn't match the "transition between two areas" mental model the user specified.
- **A** and **B** both pass every requirement. A is closer to the existing codebase (already uses `selectedMeetingId` state + URL-param reading). B inverts the model — valid alternative, more work, no practical benefit.

**Selected: Shape A** with all parts A1–A9 included as mandatory.

---

## Decisions

All 7 shaping questions resolved.

1. ✅ **R5 — mobile back button** is **Must-have**. URL sync (A6+A7) is mandatory scope, not optional.
2. ✅ **Breakpoint** — `md` = 768px (Tailwind standard).
3. ✅ **Player persistence** — player **unmounts on return to Meeting List** (R3.4). Simpler; no global player. If cross-context persistent playback becomes desirable later, it's a separate follow-on.
4. ✅ **Desktop empty state** — minimal centered message "Select a meeting to view". No welcome block, no masthead, no instructions. Just the empty shell.
5. ✅ **Back-to-list button** — top-left of Meeting View, **sticky/fixed** so it remains accessible when the transcript is scrolled deep. Daisy `btn btn-ghost btn-sm` with a chevron-left icon. Visible only at `<md`.
6. ✅ **Transition animation** — slide/push (native-app feel). Implemented with Svelte `fly` transitions or equivalent.
7. ✅ **Zero-meeting catalog empty state** — existing muted message behavior carried over as-is. No redesign in this ticket.
8. ✅ **R6 deep linking promoted to Must-have.** Mechanics verified by investigation (no spike needed — see below).

---

## Verified During Shaping — Deep-Linking Mechanics

Investigation of the current code resolved the uncertainty around R6, so no formal spike is needed.

**Current behavior (already implemented in App.svelte):**

- `onMount` reads `?meeting=<id>` from `window.location` and pre-selects that meeting.
- Reads `#t=<ms>` hash via `parseTimeHash` → stored as `pendingSeekMs`, applied once the artifact loads (see also the `pendingSeekMs` state and the `seekTo(pendingSeekMs)` call after load).
- On any in-app meeting selection, `history.replaceState` updates URL to `?meeting=<id>` so refresh works.
- Invalid meeting ID → `errorMessage = "Meeting not found in catalog: X"`, user stays on the catalog.
- Existing test coverage for URL resolution lives in `loadArtifact.test.ts` (5 tests asserting `?meeting=<id>` → correct artifact).

**What the ticket changes (small delta):**

- Switch `replaceState` → `pushState` for **user-initiated** in-app selections (so browser back creates a navigable history entry; needed for R5).
- Keep `replaceState` for **initial hydration** only (so opening a deep link doesn't create a spurious first history entry).
- Add a `popstate` listener that parses the URL on back/forward, diffs against current state, and reconciles (guarding against duplicate loads when URL and state already match).
- Clear `#t=<ms>` hash when selection changes; preserve on refresh.
- Back-to-list button calls `history.back()` (lets popstate drive the state change, keeps the mechanism consistent).

**Why no spike:** the mechanics are documented above, already partly in place, and backed by tests. Execution risk is low — it's one `replaceState` → conditional `pushState`/`replaceState`, plus one `popstate` listener. Any subtleties that arise (e.g., race between `popstate` and in-flight artifact load) surface during implementation and can be handled inline.

---

## Breadboard (Target State — Shape A)

Two places, side-by-side at `≥md`, one-at-a-time at `<md` with slide transitions. URL is the navigation source of truth; state reconciles to it via `popstate`.

### Places

| # | Place | Description |
|---|---|---|
| **P1** | **Meeting List** | Focused catalog view. Contains the meeting cards + warning banner + theme toggle. At `<md`, is the default loading screen; selecting a meeting transitions to P2. |
| **P2** | **Meeting View** | Selected meeting's content: masthead, transcript, metadata, player. Plus (at `<md`) the sticky back-to-list button. Shows a minimal empty state at `≥md` when no meeting is selected. |

Blocking test: at `<md`, P1 and P2 are mutually exclusive (single-column). At `≥md` they render side-by-side — but user interaction is still one place at a time (clicking a meeting in P1 navigates to P2's content).

### UI Affordances

🟢 = new in this ticket · 📦 = existing, relocated to its new place · (no marker) = existing, position unchanged

**P1: Meeting List**

| # | Component | Affordance | Control | Wires Out | Notes |
|---|---|---|---|---|---|
| U1 | MeetingList | Heading ("Meetings") | render | — | 🟢 |
| U2 | MeetingList | Meeting card list | render | — | 📦 |
| U3 | MeetingList | Meeting card button | click | → N1 | 📦 |
| U4 | MeetingList | Zero-meeting empty-state message | render | — | 📦 (existing muted message) |
| U5 | MeetingList | Warning banner (load failures) | render | — | 📦 |
| U6 | MeetingList | Theme toggle button (bottom-left) | click | → N2 | from V7 |

**P2: Meeting View**

| # | Component | Affordance | Control | Wires Out | Notes |
|---|---|---|---|---|---|
| U7 | MeetingView | Back-to-list button (sticky top-left, `<md` only) | click | → N3 | 🟢 |
| U8 | MeetingView | Desktop empty-state ("Select a meeting to view") | render | — | 🟢 |
| U9 | MeetingView | Masthead title `<h1>` | render | — | 📦 |
| U10 | MeetingView | Meta summary (speakers · passages · duration) | render | — | 📦 |
| U11 | MeetingView | Artifact-mode pill | render | — | 📦 |
| U12 | MeetingView | Timing-precision pill | render | — | 📦 |
| U13 | MeetingView | Current-time / duration pill | render | — | 📦 |
| U14 | MeetingView | Transcript header description | render | — | 📦 |
| U15 | MeetingView | "Auto-scroll paused" indicator | render | — | 📦 |
| U16 | MeetingView | Segment list | render | — | 📦 |
| U17 | MeetingView | Speaker tag button | click | → N4 | 📦 |
| U18 | MeetingView | Token/word button | click | → N4 | 📦 |
| U19 | MeetingView | Wheel/touchmove on list | scroll | → N5 | 📦 |
| U20 | MeetingView | Metadata `<details>` sections | render/click | — | 📦 |
| U21 | MeetingView | Player meta text | render | — | 📦 |
| U22 | MeetingView | `<audio>` element | playback events | → N8 | 📦 |
| U23 | MeetingView | Play/pause button | click | → N9 | 📦 |
| U24 | MeetingView | Timeline stats | render | — | 📦 |
| U25 | MeetingView | Timeline slider | input | → N10 | 📦 |
| U26 | MeetingView | Auto-scroll switch | click | → N11 | 📦 |
| U27 | MeetingView | Exact-words toggle | click | → N12 | 📦 |

### Code Affordances

**P1: Meeting List**

| # | Affordance | Control | Wires Out | Returns To | Notes |
|---|---|---|---|---|---|
| N1 | `loadCatalogMeeting(meeting)` | call | → S6, → N15, → N16 (push) | — | 📦 modified (URL write behavior changes) |
| N2 | `setTheme(mode)` | call | → S7, → S8 | — | from V7 |
| Nh | `hydrateCatalogMeetingMetadata` | async | → S1 (enrich) | — | 📦 existing |

**P2: Meeting View**

| # | Affordance | Control | Wires Out | Returns To | Notes |
|---|---|---|---|---|---|
| N3 | `handleBackToList()` — calls `history.back()` | call | → N17 (via popstate) | — | 🟢 |
| N4 | `seekTo(ms)` | call | → S3, → U22 (audio.currentTime) | — | 📦 |
| N5 | Engage `manualScrollLock` | write | → S5 | — | 📦 |
| N6 | `activeSegment` / `activeToken` derived | derive | — | → U16 (highlight) | 📦 |
| N7 | Auto-scroll active segment into view | effect | — | — | 📦 |
| N8 | Audio event handlers (`timeupdate`, `play`, `pause`, `durationchange`, `loadedmetadata`, `ended`) | observe | → S3, → S4 | — | 📦 |
| N9 | `togglePlayback()` | call | → S4, → U22 | — | 📦 |
| N10 | `handleTimelineInput()` | call | → S3, → U22 | — | 📦 |
| N11 | `toggleFollowPlayback()` | call | → S5 | — | 📦 |
| N12 | Toggle `showExactWords` | call | → S9 | — | 📦 |

**App (root — orchestration)**

| # | Affordance | Control | Wires Out | Returns To | Notes |
|---|---|---|---|---|---|
| N13 | `onMount()` — initial hydration | init | → N14, reads S11, sets S10 | — | 📦 modified (hydration-only URL normalization via `replaceState`) |
| N14 | `loadMeetingCatalog()` | call | — | → S1 | 📦 |
| N15 | `loadArtifact*()` (directory/bundled/portable dispatch) | call | — | → S2 | 📦 |
| N16 | URL write — `pushState` (user selection) or `replaceState` (hydration) | call | → S11 | — | 🟢 split from existing replaceState-only |
| N17 | `popstate` listener — parse URL, diff, reconcile | observe | → S6, → N15 (if meeting changed) | — | 🟢 |
| N18 | `prefers-color-scheme` listener | observe | → S7 | — | from V7 |
| N19 | `parseTimeHash` + pending-seek apply | call | → S10 → N4 | — | 📦 existing |

### Stores

Local (Svelte `let` bindings in App.svelte, passed into regions via props/context or direct reference):

| # | Store | Description | Notes |
|---|---|---|---|
| S1 | `catalogMeetings` | `MeetingCatalogEntry[]` | 📦 |
| S2 | `loadedArtifact` | Current `LoadedArtifact` | 📦 |
| S3 | `currentTimeMs` | Playback position | 📦 |
| S4 | `playing` | Playback on/off | 📦 |
| S5 | `followPlayback`, `manualScrollLock` | Auto-scroll state | 📦 |
| S6 | `selectedMeetingId` | Which meeting is active — drives which place shows on `<md` | 📦 modified (drives viewport-reactive visibility) |
| S7 | `themeMode` | `'light' \| 'dark'` | from V7 |
| S9 | `showExactWords` | Transcript mode | 📦 |
| S10 | `pendingSeekMs` | Seek to apply after artifact loads | 📦 existing |

External (browser):

| # | Store | Description | Notes |
|---|---|---|---|
| S8 | `localStorage['cassini-theme']` | Persisted theme | from V7 |
| S11 | **Browser URL** (`?meeting=<id>` + `#t=<ms>`) | Navigation + deep-link state | 🟢 elevated to first-class (was read-on-init only; now read on popstate too) |
| S12 | **Browser history stack** | Drives back/forward | 🟢 used by N16, N17, N3 |

### Target-State Mermaid

Focuses on the **new navigation flows** (selection, back, popstate). Existing affordances grouped into zones for readability.

```mermaid
flowchart TB
    subgraph P1["P1: Meeting List"]
        U1["U1: heading"]
        U2["U2: meeting list"]
        U3["U3: meeting card button"]
        U4["U4: zero-meeting empty"]
        U5["U5: warning banner"]
        U6["U6: theme toggle (V7)"]
        N1["N1: loadCatalogMeeting"]
        N2["N2: setTheme (V7)"]
    end

    subgraph P2["P2: Meeting View"]
        U7["🟢 U7: back-to-list (sticky, <md)"]
        U8["🟢 U8: desktop empty state"]
        masthead["📦 Masthead zone (U9–U13)"]
        transcript["📦 Transcript zone (U14–U19)<br/>seek, scroll-lock, active segment"]
        metaZone["📦 Metadata zone (U20)"]
        playerZone["📦 Player zone (U21–U27)<br/>audio, transport, timeline, switches"]
        N3["🟢 N3: handleBackToList"]
        N4["N4: seekTo"]
        N8["N8: audio events"]
    end

    subgraph app["App root — orchestration"]
        N13["N13: onMount hydrate"]
        N14["N14: loadMeetingCatalog"]
        N15["N15: loadArtifact*"]
        N16["🟢 N16: URL write (push/replace)"]
        N17["🟢 N17: popstate listener"]
        N18["N18: prefers-color-scheme (V7)"]
        N19["N19: parseTimeHash + pendingSeek"]
    end

    subgraph stores["Stores"]
        S1["S1: catalogMeetings"]
        S2["S2: loadedArtifact"]
        S3["S3: currentTimeMs"]
        S6["S6: selectedMeetingId"]
        S7["S7: themeMode"]
        S10["S10: pendingSeekMs"]
    end

    subgraph browser["Browser (external)"]
        S11["🟢 S11: URL (?meeting=<id> #t=<ms>)"]
        S12["🟢 S12: history stack"]
        S8["S8: localStorage[cassini-theme]"]
    end

    %% Init / hydration
    N13 --> N14
    N14 --> S1
    N13 --> N19
    N19 --> S10
    S11 -.->|read on init| N13
    N13 -->|if ?meeting present| N15
    N13 -->|normalize URL| N16
    N15 --> S2

    %% User selects a meeting (P1 → P2)
    U3 --> N1
    N1 --> S6
    N1 --> N15
    N1 --> N16
    N16 -->|pushState| S11
    N16 --> S12

    %% User clicks back-to-list (<md) (P2 → P1)
    U7 --> N3
    N3 -->|history.back| S12
    S12 -->|popstate fires| N17
    N17 --> S6
    N17 --> S11

    %% Playback / seek (preserved)
    U3 -.-> N4
    N4 --> S3
    U22_["U22: audio"] --> N8
    N8 --> S3

    %% Theme (from V7)
    U6 --> N2
    N2 --> S7
    N2 --> S8

    %% Data flow to displays
    S1 -.-> U2
    S6 -.-> U3
    S2 -.-> masthead
    S2 -.-> transcript
    S2 -.-> playerZone
    S6 -.->|null → list, set → view (<md)| P1
    S6 -.->|null → empty, set → content (≥md)| P2

    classDef new fill:#90EE90,stroke:#228B22,color:#000
    classDef existing fill:#d3d3d3,stroke:#808080,color:#000
    classDef store fill:#e6e6fa,stroke:#9370db,color:#000
    classDef ext fill:#b3e5fc,stroke:#0288d1,color:#000

    class U7,U8,N3,N16,N17 new
    class U1,U2,U3,U4,U5,U6,U9,U22_,N1,N2,N4,N8,N13,N14,N15,N18,N19,masthead,transcript,metaZone,playerZone existing
    class S1,S2,S3,S6,S7,S10 store
    class S11,S12,S8 ext
```

### Summary of what's NEW

Short list of affordances this ticket adds. Everything else is either relocation (📦) or inherited from V7.

- **U7** — back-to-list button (sticky, top-left, `<md` only)
- **U8** — desktop empty-state message
- **N3** — `handleBackToList()` (calls `history.back()`)
- **N16** — URL write split into `pushState` (user selection) vs `replaceState` (hydration). Current code does replaceState-only.
- **N17** — `popstate` listener that parses URL and reconciles state
- **S11, S12** — browser URL + history stack are elevated to first-class state stores in the breadboard (they existed before as initial-load inputs; now they're the navigation source of truth)

Plus one viewport-reactive layout concern (not an affordance per se): Tailwind grid + `<md` visibility toggles driven by `S6` + Svelte `fly` transitions for slide/push.

---

## Next Steps — Slicing

Breadboard is done. Ready to **slice**: same approach as V7 — one companion slices doc with per-slice affordance assignments and verification checklists.

Rough slice sketch (from earlier):

1. **Restructure layout** — split App.svelte into two sibling regions with Tailwind grid. Meeting List content moves into its region; Meeting View content into its region. Desktop behavior stays visually similar; on `<md` both regions are still stacked statically (no reactive visibility yet). Add the sticky back-to-list button in Meeting View (hidden on desktop via `md:hidden`).
2. **Viewport-reactive visibility** — add `<md` conditional rendering (or `hidden` class toggling) driven by `selectedMeetingId`: null → list, set → view. Desktop still side-by-side. Back-to-list clears `selectedMeetingId` on `<md`.
3. **URL sync + browser back** — `history.pushState` on selection; `popstate` listener restores state; initial load reads URL. Mobile back returns to list.
4. **Transitions + desktop empty state** — add Svelte `fly` transitions for the `<md` swap; add the "Select a meeting to view" minimal empty state for `≥md` no-selection; final audit.

---

## Not in This Ticket

Explicitly out of scope (may become separate follow-ons):

- Persistent player that keeps playing when navigating to the list — deliberately chose the unmount behavior (R3.4). If a product need emerges later, it's a separate follow-on.
- Meeting filter / search within the catalog — existing catalog UX is carried over as-is.
- Keyboard shortcuts (e.g., Esc to return to list on mobile).
- Multi-meeting comparison or split view.
- Accessibility pass (ARIA landmarks for the two places, focus management on transitions).
- Redesign of the zero-meeting-catalog empty state — kept as-is for now (decision 7).
