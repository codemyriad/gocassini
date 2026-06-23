# G4 — Viewer / meeting artifact delivery surface

> A polished, responsive, deep-linkable meeting library and single-meeting viewer that turns published Cassini artifacts into a usable end-user product surface.

- **Project:** [P1 — Cassini internal MVP](P1-cassini-internal-mvp.md) · **Phase:** PHASE-1 · **Requirements:** R4, R4.1, R4.2, R4.3
- **Status:** ◑ In progress — blocked only on D-249
- **Last updated:** 2026-06-23

## What this goal is

G4 owns the viewer — the surface that presents published meeting artifacts as a browsable library and a single-meeting view. The publish/serve/browse baseline already existed and is verified: a meeting list, a single-meeting page, transcript click-to-seek, and search all function against published output.

This goal advances that baseline on two axes: presentation quality (migrate onto a stock, themeable design system) and navigation structure (split into a Meeting List and a Meeting View with URL-driven navigation, deep-linking, and a mobile-friendly flow).

## Why it matters

The viewer is where every other goal's output becomes visible to a human — recordings, transcripts, and summaries are only as valuable as the surface delivering them. A responsive, deep-linkable library is what makes Cassini feel like a product.

## Definition of done

- Viewer runs on a stock DaisyUI/Tailwind design system with a working light/dark theme toggle, behavior preserved. ✅
- Meeting List and Meeting View are cleanly separated surfaces.
- Navigation is URL-as-source-of-truth, making single meetings deep-linkable.
- Mobile usage works, including back-to-list navigation.
- The existing list / single-meeting / click-to-seek / search baseline remains intact.

## Work done

| Task | What it delivered |
| --- | --- |
| [D-248 — viewer-design-system](D-248-viewer-design-system) | Slice V7: migrated the viewer to stock DaisyUI/Tailwind with a light/dark theme toggle, in place, behavior preserved. The foundation D-249 builds on. |

The pre-existing publish/serve/browse baseline (meeting list, single-meeting page, click-to-seek, search) is verified working and underpins this goal.

## Work TODO

| Task | Status | Outstanding |
| --- | --- | --- |
| [D-249 — viewer-ux-restructure](D-249-viewer-ux-restructure) | ⏳ Not started (shaping + slices done) | Split Meeting List / Meeting View, URL-as-source-of-truth navigation (deep-linkable meetings), mobile back-to-list. |

## Gaps to completion

D-249 is the only thing between current state and goal completion. Its shaping and slices are complete; implementation has not started. It is one of the two principal remaining Phase-1 work items (the other is D-246 under [G6](G6-self-host-deployment.md)).

- Implement the Meeting List / Meeting View split and player relocation (slice 1).
- URL sync + `popstate` deep-linking (slice 2).
- Transitions, desktop empty state, polish (slice 3).

See related [qa-2026-05-04](qa-2026-05-04) findings that touch this surface (QA-01/02/03).
