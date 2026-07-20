---
shaping: true
---

# D-420 — Unify Cassini into one app (viewer as reusable component + shareable embed) — Shaping

## Source (verbatim)

Team decision relayed by Chris (2026-07-09):

> we talked about this yesterday as a team and agreed that we are happy to proceed
> rolling everything together into one central cassini exapp, it makes the most
> sense, we don't need to be delicate about it — the comment in 420 "The point is a
> shell + seam, not a merged mega-app", doesn't necessarily stand anymore. The one
> thing we should do is to maintain the viewer as a separate embed layer that can be
> used to share a meeting as a single self-contained bundle. The surface of this
> embed is just the meeting view itself, not the list. This means the list
> functionality should be pulled into the central app, the viewer surface stripped
> down and consuming its data interface, and the viewer used as a component in the
> main app. It's probably sensible to maintain interfaces for what each part of the
> app consumes, but the importance of the data seam is really more uniquely about
> the viewing layer (do you agree?).
>
> Open questions: 1 — good solution already laid out. 2 — one bundle. 3 — the admin
> check on the exapp isn't required anymore. 4 — can't we use some shallow routing
> here to enable history with navigation, add a basic nav at the top of the App
> shell to go between operator or viewer.

---

## Problem

Today the ExApp registers **two** Nextcloud top-menu entries backed by **two** Svelte
projects with **two** embedded builds:

- **"Cassini"** — `viewer` entry, `AdminRequired:0`, all users → `cassini-viewer` IIFE (list + meeting-view).
- **"Cassini Admin"** — `control-panel` entry, `AdminRequired:1`, admins → `cassini-control-panel` IIFE (operator: jobs, start/stop/rerun, settings, live SSE).

Two nav entries for one product is the wrong end-state. The team has decided to merge
into one app. The one thing to preserve: the **meeting-view** must still be buildable as
a standalone, self-contained **single-meeting share** (view only, no list) — this is the
"embed layer."

## Outcome

One "Cassini" entry → one in-NC app whose surfaces are role-gated (non-admin: browse +
view; admin: + operator), with the meeting-view extracted as a reusable component that
also compiles into a standalone single-meeting share.

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | One "Cassini" top-menu entry backed by one in-NC app, replacing the two entries. | Core goal |
| R1 | Role-gated surfaces in one app: non-admin gets browse (list) + meeting-view; admin also gets the operator (recording control). | Must-have |
| R2 | Meeting-view (one meeting: transcript + audio + summary) is a reusable component that also builds as a standalone single-meeting **share** bundle — surface = view only, no list. | Must-have |
| R3 | The operator recording API stays admin-only at the **real** boundary (`info.xml` per-route `ADMIN`). Collapsing to one `USER` entry must not let a non-admin drive recordings. | Must-have (security) |
| R4 | In-app navigation between surfaces gives browser history/back-forward + deep links, **without** a pathname/history router that desyncs from the AppAPI embedded route. | Must-have |
| R5 | The in-NC app ships as one dynamic-import-free IIFE (`assert-embedded-single-bundle` stays green) — which means the two Svelte projects consolidate into one build. | Must-have |
| R6 | The shell hosts N surfaces over time (dashboard, summary-templates, integrations/workflows, meeting grouping/sharing) — an open surface set, not a hardcoded 2-way switch. | Nice-to-have (leave room) |
| R7 | Typed data access per surface. The meeting-view's source is **polymorphic** (in-NC / published web / portable share); list and operator each have a single typed source. | Must-have |
| R8 | Minimal & reversible; no Postgres, no server-side meeting model, no ACL logic now. | Must-have |

---

## Shapes

### CURRENT
Two entries, two Svelte projects, two IIFEs. Viewer `App.svelte` holds list + meeting-view
and already does hash-only history via `pushState` (fragment-only). Operator is a separate
project talking to an `ADMIN`-gated JSON API. Meeting-view-only already exists as a runtime
*mode* (`__CASSINI_VIEWER_ARTIFACT_MODE__ === "bundled"` → `loadBundledArtifact`, no catalog).

### A: Thin shell, keep two projects (the OLD ticket shape)
Drop the 2nd entry; a thin shell mounts viewer-or-operator; defer the bundle merge; keep the
two projects and choose which to mount at runtime. "Shell + seam, not a merged mega-app."
→ **Superseded by the team decision.** Kept only for the fit-check contrast.

### B: One consolidated app + build; meeting-view as component + standalone share (SELECTED)

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | **Collapse to one entry** — drop "Cassini Admin" from `topMenuEntries`; single "Cassini" (`AdminRequired:0`) hosts the shell. Reversible. | |
| **B2** 🟡 | **Promote operator → `cassini-app`, consuming `cassini-viewer` as a layer** (Chris's D2). `cassini-viewer` stays a package = the **viewing layer** (exports `MeetingView` + `MeetingList` + the `DataProvider` seam + loaders); it keeps its own standalone builds (share + published web archive). `cassini-control-panel` is **renamed/promoted to `cassini-app`** — the in-NC shell — which depends on `cassini-viewer` and adds the operator surface + shell + nav. `cassini-app`'s one `vite.embedded` build emits the single in-NC IIFE (all surfaces). The NC-embedding infra (`embedded.ts`: shadow root, NC theme bridge, base capture) **moves from `cassini-viewer` to `cassini-app`** — the pure viewing layer no longer mounts itself into NC. Operator serves one dist at `/ui/viewer.{js,css}`; `/ui/control-panel.{js,css}` + its info.xml asset routes are dropped. **New infra required:** a root workspace (pnpm — a stray `pnpm-lock.yaml` already appeared in control-panel; standardise) + an `exports` map on `cassini-viewer` so `cassini-app` imports its components at build time into one IIFE (no dynamic import). | ⚠️ |
| **B3** | **Extract `MeetingView` component** — pull the meeting-view (right pane) out of `App.svelte` into a component that takes its artifact via the seam. In-NC/web `App` composes List + MeetingView; the standalone single-meeting share mounts MeetingView directly (formalises today's "bundled" mode). | |
| **B4** | **App shell + nav** — shell renders a surface set; a top nav (tabs) shows when >1 surface is available; surfaces = { browse (List+MeetingView), operator (admin) }; registry is open-ended for future surfaces (R6). | |
| **B5** | **Surface routing via hash** — extend `hashRouting` with a `surface` segment; `pushState` (fragment-only) already yields history/back-forward + deep links. **No** pathname/history router (see `hashRouting.ts`: it would desync from the embedded route and break NC's own back button). | |
| **B6** | **Admin detection** — derive admin-ness from the **boundary**: attempt an operator endpoint, `200` → show operator, `403` → hide it; use `OC.isUserAdmin()` (if present) only as an optimistic hint to avoid a flash. Client gating is UX; the 403 is the brace. | ⚠️ spike |
| **B7** | **Security boundary unchanged** — `info.xml` operator JSON API routes (`/jobs`, `/events`, `/settings`, doctor) stay `ADMIN`. Only the *entry* `AdminRequired` flag and the *control-panel asset* `ADMIN` gating dissolve (operator UI code now rides in the `USER` viewer bundle — harmless; the **data** stays gated). | |
| **B8** | **Seam threading** — keep #122's `DataProvider`; MeetingView consumes its meeting-loading half; List consumes the catalog half; operator keeps its own `OperatorClient` (not part of the seam — single source, no polymorphism needed). | |

---

## Fit Check: R × (A, B)

| Req | Requirement | Status | A | B |
|-----|-------------|--------|---|---|
| R0 | One entry / one in-NC app replacing the two entries | Core goal | ✅ | ✅ |
| R1 | Role-gated surfaces (non-admin browse+view; admin +operator) | Must-have | ✅ | ✅ |
| R2 | Meeting-view reusable + standalone single-meeting share (no list) | Must-have | ❌ | ✅ |
| R3 | Operator API stays admin-only at the real boundary | Must-have (sec) | ✅ | ✅ |
| R4 | History/back-forward + deep links, no pathname router | Must-have | ✅ | ✅ |
| R5 | One dynamic-import-free IIFE (projects consolidate) | Must-have | ❌ | ✅ |
| R6 | Open surface set for future surfaces | Nice-to-have | ⚠️→❌ | ✅ |
| R7 | Typed per-surface data; meeting-view polymorphic | Must-have | ❌ | ✅ |
| R8 | Minimal & reversible; no speculative infra | Must-have | ✅ | ✅ |

**Notes:**
- A fails **R2/R7**: keeping two projects and mounting one at runtime never extracts a reusable MeetingView or a clean per-surface data interface — the share stays a runtime flag, not a component.
- A fails **R5** by definition (it defers/avoids the merge).
- A on **R6**: a two-way runtime switch is not an open surface set → ❌.
- B's only ⚠️s: **B2** (the real restructure — biggest, least-reversible piece) and **B6** (admin-detection spike).

---

## Adversarial notes / where I push back

1. **"admin check on the exapp isn't required anymore" (Q3) — true for the *entry*, NOT for the *API* (R3/B7).** Dropping the 2nd entry drops its `AdminRequired` flag, and folding operator UI into the `USER` bundle drops the control-panel *asset* gating. But the operator **JSON API** (`/jobs`, `/events`, `/settings`) is protected *only* by `info.xml`'s per-route `ADMIN` — the frontend does zero auth. If that gating is dropped too, **any logged-in user could start/stop recordings.** Keep it. This is a hard flag.

2. **Q1 admin detection is smaller than the ticket says.** Because R3 puts the real boundary server-side, client detection is UX-only. Don't bet the design on whether `OC.isUserAdmin()` exists on the embedded page — **probe the boundary** (403 → hide operator). That dissolves the "load-bearing unknown"; the `OC.isUserAdmin()` check becomes a nice-to-have anti-flash hint.

3. **"one bundle" (Q2) needs precision.** One IIFE for the **in-NC app** — which forces consolidating the two Svelte projects (overriding the ticket's "no monorepo restructure" non-goal; the team has authorised this). The **standalone single-meeting share** and the **published web archive** remain their own builds — they're different entry points/surfaces. So: *one in-NC bundle; the shareable embed is a separate build sharing MeetingView source.*

4. **Gentle correction: the list isn't unique to the central app.** The **published web archive** (served at `/published` + the microsite) also browses a multi-meeting list. So the list becomes a **shared surface component** used by the in-NC app *and* the standalone web build — but never by the single-meeting share. Don't strand the web archive without a list.

5. **Do I agree the data seam is "uniquely about the viewing layer"? Mostly yes (R7/B8).** The seam's *polymorphism* is earned almost entirely by MeetingView, which must run in ≥3 data contexts (in-NC, published web, portable share). The list needs a typed catalog source but not a multi-impl abstraction; the operator needs a typed client, not a seam. So keep #122's one `DataProvider`, thread its meeting half into MeetingView — don't build speculative parallel seams for list/operator.

6. **Q4 routing — agree, and it's cheaper than it sounds.** "Shallow routing for history + nav" is *already the pattern*: the viewer does `pushState` on a fragment-only URL today, giving history/back-forward. Add a `surface` segment to that same hash; a top-nav flips it. **Not** a pathname router (`hashRouting.ts` documents why that breaks the NC embedded route).

7. **Dependency: #122 is still OPEN, not merged.** D-420 sits on top of the D-415 `DataProvider` seam, which is PR **#122** (open, base `main`; `dataProvider.ts` currently only in a worktree). Sequence: land #122 (and #119, the D-485 test fix) first, or branch D-420 off #122. (The team-memory note that #122 "shipped" is stale.)

8. **One accepted downside of merging:** the `USER` bundle now carries operator UI code a non-admin can't use (mild size bump; reveals the admin API shape to anyone reading the bundle). For an OSS app whose API is already public, this is a non-issue — but it's the price of "one bundle," and **B2 is the least-reversible step**, so it should land last, after the shell's value is proven.

---

## Decisions (locked 2026-07-09)

- **D1 — Operator API boundary (R3):** ✅ **Keep `ADMIN`.** operator JSON API stays admin-only in `info.xml`; in-app gating is UX only. No product change to who can drive recordings.
- **D2 — Project layout (B2):** ✅ **`cassini-app` (promoted from `cassini-control-panel`) consumes `cassini-viewer` as a layer.** `cassini-viewer` stays the viewing layer + shareable-embed/web-archive builds; `cassini-app` is the in-NC shell. Needs a root workspace + `cassini-viewer` `exports`.
- **D3 — Admin detection (B6/Q1):** ✅ **Probe the boundary (403).** `OC.isUserAdmin()` only as an optional anti-flash hint.
- **Routing (B5):** hash `surface` segment + top-nav; no pathname router. Resolved.
- **Seam (B8):** keep #122's single `DataProvider`, thread the meeting half into `MeetingView`. Resolved.

---

## Next: slice (reversible-first, irreversible-last)

Depends on **#122** (D-415 seam) landing first — build on top of it.

- **V1 — Viewing-layer refactor (in `cassini-viewer`, B3/B8):** split `App.svelte` into `MeetingList` + `MeetingView`; `MeetingView` consumes the seam's meeting half; add an `exports` map. Standalone + share + web-archive builds stay green. *Pure refactor, no NC changes.* Demo: standalone viewer + single-meeting share still render; `MeetingView` mounts on its own.
- **V2 — Workspace + `cassini-app` shell skeleton (B1/B2):** root pnpm workspace; promote `cassini-control-panel` → `cassini-app` depending on `cassini-viewer`; move NC-embedding infra (`embedded.ts`) into `cassini-app`; `cassini-app` renders the browse surface as the single in-NC IIFE; drop "Cassini Admin", point "Cassini" at `cassini-app`. Operator not yet a surface. Demo: one NC entry, browse works, `assert-embedded-single-bundle` green.
- **V3 — Operator surface + nav + probe (B4/B5/B6/B7):** operator becomes an admin surface in `cassini-app`; top-nav via hash `surface` segment (history/back-forward); admin-probe (403) gates the operator tab; `info.xml` keeps operator API `ADMIN`, drops control-panel asset routes. Demo: admin sees Browse|Operator w/ back-forward; non-admin browse-only; operator 403s for non-admin.
- **V4 — Formalise the shareable embed (R2/R3):** `cassini-viewer` share build mounts `MeetingView`-only cleanly; confirm the published web archive still lists. Demo: single-meeting share renders view-only.
