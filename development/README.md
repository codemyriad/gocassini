# Development

Planning and development tracking for Cassini. This directory replaces the old
`planning/` tree and follows the taxonomy specified in
[`../DEVELOPMENT.md`](../DEVELOPMENT.md).

## Taxonomy (TL;DR)

The ontology is **not strictly hierarchical** — entities prefer many-to-one but
allow many-to-many, and links (not directory nesting) express the relationships.

| Entity | What it is | Naming |
|--------|------------|--------|
| **Initiative** | The broad effort whose vision crystallizes over time. Cassini MVP. Not tied to a fixed time horizon. | [`initiative-cassini-mvp.md`](./initiative-cassini-mvp.md) |
| **Phase** | A real-world time interval containing a collection of work. | `PHASE-{n}` |
| **Project** | A contained chunk of initiative work with a business definition and a clear "what success looks like". | `P{n}-{short-name}` |
| **Goal** | A unit of functionality smaller than a project. May belong to one/more projects, or be orphaned (cross-cutting hygiene). | `G{n}-{short-name}` |
| **Task** | A contained chunk of development toward a goal, mapped to a Linear ticket. No forward backlog — lineage is ex-post only. | `D-{ticket}-{short-name}` |

Within a phase, **projects, goals and tasks live side-by-side**. A goal links
up to its project(s) and down to its tasks; a task is a directory keeping the
strict per-task document set defined in `DEVELOPMENT.md`.

### Document convention

- Each **project** and **goal** is a single **standalone `.md` file** named after
  the entity (e.g. `P1-cassini-internal-mvp.md`, `G1-operator-control-plane.md`).
  Where an entity also carries reference/shaping docs, they sit in a same-named
  sibling folder (e.g. `P1-cassini-internal-mvp/`) that the standalone file links into.
- Every project/goal file is a living **status report** and carries, near the top,
  a **`Last updated:`** date, then **Work done**, **Work TODO**, and **Gaps to
  completion** sections — a project framed in terms of its goals, a goal in terms
  of its tasks. These are refreshed by an explicit **reflection** pass (run on
  request); `Last updated` records the date of that pass.

## PHASE-1

Everything to date is associated with PHASE-1. Nothing is archived yet —
Phase-1 still has lingering work (notably **D-249** viewer UX restructure and
the **D-246** deployment-bundle implementation).

### Projects

- [**P1 — Cassini internal MVP**](./PHASE-1/P1-cassini-internal-mvp.md) — the end-to-end Nextcloud-Talk meeting-to-artifact MVP (old `mvp` initiative).
- [**P2 — Nextcloud marketplace readiness**](./PHASE-1/P2-nextcloud-marketplace-readiness.md) — distribution: ExApp packaging + runtime images.

### Goals

| Goal | Project | Status |
|------|---------|--------|
| [G1 — Operator control plane](./PHASE-1/G1-operator-control-plane.md) | P1 | ✅ |
| [G2 — Live Nextcloud Talk capture](./PHASE-1/G2-live-meeting-capture.md) | P1 | ✅ |
| [G3 — Meeting summary](./PHASE-1/G3-meeting-summary.md) | P1 | ✅ |
| [G4 — Viewer / delivery surface](./PHASE-1/G4-viewer-delivery-surface.md) | P1 | ◑ (D-249 pending) |
| [G5 — Failure inspection, rerun & reliability](./PHASE-1/G5-failure-rerun-reliability.md) | P1 | ✅ |
| [G6 — Self-host deployment & docs](./PHASE-1/G6-self-host-deployment.md) | P1 | ◑ (build pending) |
| [G7 — Unified product CLI & DX foundation](./PHASE-1/G7-product-cli-foundation.md) | _orphaned / cross-cutting_ | ✅ (phase 1) |

### Tasks

Mapped to Linear tickets; grouped here by goal for navigation.

- **G1:** `D-233-job-scheduler-setup`, `D-266-operator-control-panel`
- **G2:** `D-263-nextcloud-app-recording`, `D-234-live-nextcloud-talk-recording`, `D-283-nextcloud-internal-audio-capture`, `D-288-play-commands`, `D-288-1-1-meeting`
- **G3:** `D-250-demo-data-prep`, `D-241-summary-display-ux`, `D-242-summary-generation`
- **G4:** `D-248-viewer-design-system`, `D-249-viewer-ux-restructure` ⏳
- **G5:** `D-243-failure-inspection-and-rerun-flow`, `D-280-rerun-only-postprocessing-jobs`, `D-281-publish-dest-conflict`
- **G6:** `D-246-deployment-bundle-1` ◑

Cross-cutting: `qa-2026-05-04/` — repo-alignment QA audit.

> **Migration note (2026-06):** the `V0`–`V8` slice directories were renamed to
> their Linear tickets (V0→D-250, V1→D-233, V2→D-234, V3→D-241, V4→D-242,
> V5→D-243, V7→D-248, V8→D-249). Per-task documents were **not** backfilled to
> the new strict spec — each task keeps the documents it originally had.

Legend: ✅ complete · ◑ in progress · ⏳ not started
