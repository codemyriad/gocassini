# G4 — Viewer / Meeting Artifact Delivery Surface

> A polished, responsive, deep-linkable meeting library and single-meeting viewer that turns published Cassini artifacts into a usable end-user product surface.

- **Project:** [P1 — Cassini internal MVP](../P1-cassini-internal-mvp/spec.md) · **Phase:** PHASE-1 · **Status:** ◑ In progress (blocked only on D-249) · **Requirements:** R4, R4.1, R4.2, R4.3

## What this goal is

G4 owns the viewer — the user-facing surface that presents published meeting artifacts as a browsable library and a single-meeting view. The publish/serve/browse baseline already existed and is verified: a meeting list, a single-meeting page, click-to-seek on the transcript, and search all function against the artifact pipeline's published output.

This goal advances that baseline along two axes: presentation quality and navigation structure. First, migrate the viewer onto a stock, themeable design system. Second, restructure it into a true two-surface application — a Meeting List and a Meeting View — with URL-driven navigation that makes individual meetings deep-linkable and the experience usable on mobile.

## Why it matters

The viewer is where every other goal's output becomes visible to a human — recordings, transcripts, and summaries are only as valuable as the surface that delivers them. A responsive, deep-linkable library is what makes Cassini feel like a product rather than a generated directory of files.

## Definition of done / success signals

- Viewer runs on a stock DaisyUI/Tailwind design system with a working light/dark theme toggle, behavior preserved (✅ delivered).
- Meeting List and Meeting View are cleanly separated surfaces.
- Navigation is URL-as-source-of-truth, making single meetings deep-linkable.
- Mobile usage works, including back-to-list navigation.
- The existing list / single-meeting / click-to-seek / search baseline remains intact throughout.

## Tasks

| Task | Status | What it delivered |
| --- | --- | --- |
| [D-248 — viewer design system](../D-248-viewer-design-system) | ✅ | Slice V7. Migrated the viewer to stock DaisyUI/Tailwind with a light/dark theme toggle, done in place with behavior preserved. The design-system foundation D-249 builds on. |
| [D-249 — viewer UX restructure](../D-249-viewer-ux-restructure) | ⏳ | Slice V8. Shaping and slices are done; implementation is pending. Will split Meeting List / Meeting View, make navigation URL-as-source-of-truth, and add mobile back-to-list. The principal remaining Phase-1 work item. |

## Lineage & remaining work

The publish/serve/browse baseline — meeting list, single-meeting page, click-to-seek, and search — predates these tasks and is verified working. **D-248** then re-platformed that baseline onto a stock DaisyUI/Tailwind design system with theming, in place and without changing behavior, establishing the foundation for further UX work.

**D-249** is the one outstanding piece. Its shaping and slices are complete, but implementation has not started: splitting the viewer into separate Meeting List and Meeting View surfaces, adopting URL-as-source-of-truth navigation (deep-linkable meetings), and handling mobile (back-to-list). It is the lingering Phase-1 work item — this goal is in progress and blocked only on D-249. See related QA findings in [qa-2026-05-04](../qa-2026-05-04) that touch this surface alongside G5 and G6.
